package proxy

// 补号的供应商选择与触发策略测试。
//
// 覆盖四块：
//  1. newReplenishSupplier 按 Provider 选择正确的客户端。
//  2. kiroappClient 对 /openapi/* 的请求构造与两种 claim 响应形态的解析。
//  3. kiroappioClient 对 /api/me/* 的请求构造、幂等键与阶梯定价字段的解析。
//  4. replenishTrigger 的两个触发条件（全部凭证禁用 / 低水位）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-go/config"
)

// --- 供应商选择 ---

func TestNewReplenishSupplierSelectsByProvider(t *testing.T) {
	tests := []struct {
		name     string
		rc       config.ReplenishConfig
		wantName string
		wantErr  bool
	}{
		{
			name:     "empty provider falls back to vendor",
			rc:       config.ReplenishConfig{BaseURL: "https://vendor.example.com", ApiKey: "usr-x"},
			wantName: config.ReplenishProviderVendor,
		},
		{
			name:     "explicit vendor",
			rc:       config.ReplenishConfig{Provider: "default", BaseURL: "https://vendor.example.com", ApiKey: "usr-x"},
			wantName: config.ReplenishProviderVendor,
		},
		{
			name:     "kiroapp",
			rc:       config.ReplenishConfig{Provider: "kiroapp", KiroappApiKey: "k-1"},
			wantName: config.ReplenishProviderKiroapp,
		},
		{
			name:     "kiroapp.cc alias",
			rc:       config.ReplenishConfig{Provider: "kiroapp.cc", KiroappApiKey: "k-1"},
			wantName: config.ReplenishProviderKiroapp,
		},
		{
			// 切到 kiroapp 后只校验 kiroapp 的密钥，vendor 的缺失不该报错。
			name:     "kiroapp ignores missing vendor credentials",
			rc:       config.ReplenishConfig{Provider: "kiroapp", KiroappApiKey: "k-1"},
			wantName: config.ReplenishProviderKiroapp,
		},
		{
			name:    "kiroapp without key errors",
			rc:      config.ReplenishConfig{Provider: "kiroapp"},
			wantErr: true,
		},
		{
			name:     "kiroappio",
			rc:       config.ReplenishConfig{Provider: "kiroappio", KiroappioApiKey: "km_1"},
			wantName: config.ReplenishProviderKiroappio,
		},
		{
			name:     "kiroapp.io alias",
			rc:       config.ReplenishConfig{Provider: "kiroapp.io", KiroappioApiKey: "km_1"},
			wantName: config.ReplenishProviderKiroappio,
		},
		{
			// 三个供应商的密钥互不干扰：只校验当前选中的那个。
			name:     "kiroappio ignores other providers' credentials",
			rc:       config.ReplenishConfig{Provider: "kiroappio", KiroappioApiKey: "km_1", KiroappApiKey: "", BaseURL: ""},
			wantName: config.ReplenishProviderKiroappio,
		},
		{
			name:    "kiroappio without token errors",
			rc:      config.ReplenishConfig{Provider: "kiroappio"},
			wantErr: true,
		},
		{
			name:    "vendor without baseUrl errors",
			rc:      config.ReplenishConfig{ApiKey: "usr-x"},
			wantErr: true,
		},
		{
			name:    "vendor without apiKey errors",
			rc:      config.ReplenishConfig{BaseURL: "https://vendor.example.com"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newReplenishSupplier(tc.rc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got client %T", client)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := client.ProviderName(); got != tc.wantName {
				t.Errorf("ProviderName() = %q, want %q", got, tc.wantName)
			}
		})
	}
}

func TestKiroappDefaultBaseURL(t *testing.T) {
	// baseURL 留空时应回落到官方地址，用户只需填密钥。
	c, err := newKiroappClient(config.ReplenishConfig{Provider: "kiroapp", KiroappApiKey: "k-1"})
	if err != nil {
		t.Fatalf("newKiroappClient: %v", err)
	}
	if c.baseURL != config.DefaultKiroappBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, config.DefaultKiroappBaseURL)
	}

	// 显式配置时应去掉末尾斜杠，避免拼出 //openapi/claim。
	c2, err := newKiroappClient(config.ReplenishConfig{
		Provider:       "kiroapp",
		KiroappBaseURL: "https://mirror.example.com/",
		KiroappApiKey:  "k-1",
	})
	if err != nil {
		t.Fatalf("newKiroappClient: %v", err)
	}
	if c2.baseURL != "https://mirror.example.com" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c2.baseURL)
	}
}

// --- kiroapp 客户端 ---

// newTestKiroapp 指向一个测试服务器，绕过供应商配置。
//
// 仍需 config.Init：do() 会读全局出站代理配置（config.GetProxyURL），未初始化时
// 会 nil deref。用 t.TempDir 隔离，避免测试之间互相污染配置文件。
func newTestKiroapp(t *testing.T, srv *httptest.Server) *kiroappClient {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	return &kiroappClient{baseURL: srv.URL, apiKey: "test-key"}
}

func TestKiroappClaimSingle(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/balance" {
			json.NewEncoder(w).Encode(map[string]float64{"balance": 42.5})
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		json.NewEncoder(w).Encode(map[string]string{"key": "ksk_single"})
	}))
	defer srv.Close()

	claim, err := newTestKiroapp(t, srv).Claim(supplierClaimRequest{Count: 1, ClientOrderID: "ignored-order-id"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotPath != "/openapi/claim" {
		t.Errorf("path = %q, want /openapi/claim", gotPath)
	}
	// 单个提取按文档不带请求体。
	if gotBody != "" {
		t.Errorf("single claim sent body %q, want none", gotBody)
	}
	if len(claim.Keys) != 1 || claim.Keys[0] != "ksk_single" {
		t.Errorf("Keys = %v, want [ksk_single]", claim.Keys)
	}
	if claim.PurchasedCount() != 1 {
		t.Errorf("PurchasedCount() = %d, want 1", claim.PurchasedCount())
	}
	// claim 响应不含余额，应由 balance 补齐。
	if claim.Remaining != 42.5 {
		t.Errorf("Remaining = %v, want 42.5", claim.Remaining)
	}
	// kiroapp 没有幂等订单号，不应伪造一个。
	if claim.OrderID != "" {
		t.Errorf("OrderID = %q, want empty (kiroapp has no idempotency key)", claim.OrderID)
	}
}

func TestKiroappClaimBatch(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/balance" {
			json.NewEncoder(w).Encode(map[string]float64{"balance": 10})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string][]string{"keys": {"ksk_a", " ", "ksk_b"}})
	}))
	defer srv.Close()

	claim, err := newTestKiroapp(t, srv).Claim(supplierClaimRequest{Count: 2})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got, ok := gotBody["count"].(float64); !ok || int(got) != 2 {
		t.Errorf("body count = %v, want 2", gotBody["count"])
	}
	// 空白项应被丢弃。
	if len(claim.Keys) != 2 || claim.Keys[0] != "ksk_a" || claim.Keys[1] != "ksk_b" {
		t.Errorf("Keys = %v, want [ksk_a ksk_b]", claim.Keys)
	}
}

func TestKiroappClaimNoKeysIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string][]string{"keys": {}})
	}))
	defer srv.Close()

	// 出 0 个 Key 必须报错，否则会被当成一次成功的空补号而不被察觉。
	if _, err := newTestKiroapp(t, srv).Claim(supplierClaimRequest{Count: 3}); err == nil {
		t.Fatal("expected error when claim returns no keys")
	}
}

func TestKiroappClaimRejectsNonPositiveCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP call expected for non-positive count")
	}))
	defer srv.Close()

	if _, err := newTestKiroapp(t, srv).Claim(supplierClaimRequest{Count: 0}); err == nil {
		t.Fatal("expected error for count=0")
	}
}

func TestKiroappStockAndAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/stock":
			json.NewEncoder(w).Encode(map[string]interface{}{"availableKeys": 12, "keyPrice": 1.5})
		case "/openapi/balance":
			json.NewEncoder(w).Encode(map[string]float64{"balance": 99})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestKiroapp(t, srv)

	stock, err := c.Stock()
	if err != nil {
		t.Fatalf("Stock: %v", err)
	}
	if stock != 12 {
		t.Errorf("Stock() = %d, want 12", stock)
	}

	acc, err := c.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acc.Remaining != 99 {
		t.Errorf("Remaining = %v, want 99", acc.Remaining)
	}
	if !acc.HasPrice || acc.KeyPrice != 1.5 {
		t.Errorf("KeyPrice = %v (has=%v), want 1.5", acc.KeyPrice, acc.HasPrice)
	}
	// kiroapp 不提供配额，不应假装有。
	if acc.HasQuota {
		t.Error("HasQuota = true, want false for kiroapp")
	}
}

func TestKiroappErrorResponseSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "insufficient balance"})
	}))
	defer srv.Close()

	_, err := newTestKiroapp(t, srv).Stock()
	if err == nil {
		t.Fatal("expected error on HTTP 403")
	}
	// 上游原因要透传出来，否则面板只能显示一个裸状态码。
	if !strings.Contains(err.Error(), "insufficient balance") {
		t.Errorf("error %q should contain the upstream message", err)
	}
}

// --- kiroapp.io 客户端 ---

// newTestKiroappio 指向一个测试服务器，绕过供应商配置。config.Init 的理由同上。
func newTestKiroappio(t *testing.T, srv *httptest.Server) *kiroappioClient {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	return &kiroappioClient{baseURL: srv.URL, apiKey: "km_test"}
}

func TestKiroappioDefaultBaseURL(t *testing.T) {
	c, err := newKiroappioClient(config.ReplenishConfig{Provider: "kiroappio", KiroappioApiKey: "km_1"})
	if err != nil {
		t.Fatalf("newKiroappioClient: %v", err)
	}
	if c.baseURL != config.DefaultKiroappioBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, config.DefaultKiroappioBaseURL)
	}

	c2, err := newKiroappioClient(config.ReplenishConfig{
		Provider:         "kiroappio",
		KiroappioBaseURL: "https://mirror.example.com/",
		KiroappioApiKey:  "km_1",
	})
	if err != nil {
		t.Fatalf("newKiroappioClient: %v", err)
	}
	if c2.baseURL != "https://mirror.example.com" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c2.baseURL)
	}
}

func TestKiroappioClaimSendsIdempotencyKey(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me/profile" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"user": map[string]interface{}{"name": "alice", "balance": 1870},
			})
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"purchased":   2,
			"requested":   2,
			"remaining":   115,
			"unit_price":  38,
			"total_debit": 76,
			"order_id":    "batch-xyz",
			"keys": []map[string]interface{}{
				{"key": "sk-aaa", "price": 30},
				{"key": " ", "price": 0},
				{"key": "sk-bbb", "price": 46},
			},
		})
	}))
	defer srv.Close()

	claim, err := newTestKiroappio(t, srv).Claim(supplierClaimRequest{Count: 2})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if gotAuth != "Bearer km_test" {
		t.Errorf("Authorization = %q, want Bearer km_test", gotAuth)
	}
	if gotPath != "/api/me/purchase" {
		t.Errorf("path = %q, want /api/me/purchase", gotPath)
	}
	if got, ok := gotBody["count"].(float64); !ok || int(got) != 2 {
		t.Errorf("body count = %v, want 2", gotBody["count"])
	}
	// client_order_id 必填且必须是 32 位十六进制，否则供应商拒收。
	orderID, _ := gotBody["client_order_id"].(string)
	if len(orderID) != 32 {
		t.Errorf("client_order_id = %q, want 32 hex chars", orderID)
	}
	// 未指定批次时不应带 order_id，否则会被限制到某个不存在的批次。
	if _, ok := gotBody["order_id"]; ok {
		t.Error("order_id must be omitted when no batch is requested")
	}

	// 空白 key 应被丢弃。
	if len(claim.Keys) != 2 || claim.Keys[0] != "sk-aaa" || claim.Keys[1] != "sk-bbb" {
		t.Errorf("Keys = %v, want [sk-aaa sk-bbb]", claim.Keys)
	}
	// 计费以 total_debit 为准，而不是 count × unit_price。
	if claim.Spent != 76 {
		t.Errorf("Spent = %v, want 76 (total_debit)", claim.Spent)
	}
	if claim.PurchasedCount() != 2 {
		t.Errorf("PurchasedCount() = %d, want 2", claim.PurchasedCount())
	}
	// purchase 响应的 remaining 是剩余库存，余额须来自 profile。
	if claim.Remaining != 1870 {
		t.Errorf("Remaining = %v, want 1870 (balance, not stock)", claim.Remaining)
	}
	// 回传的是我方幂等键，重试时要复用它。
	if claim.OrderID != orderID {
		t.Errorf("OrderID = %q, want the client_order_id %q", claim.OrderID, orderID)
	}
}

func TestKiroappioClaimReusesGivenOrderIDs(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me/profile" {
			json.NewEncoder(w).Encode(map[string]interface{}{"user": map[string]interface{}{}})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"purchased": 1,
			"keys":      []map[string]interface{}{{"key": "sk-a"}},
		})
	}))
	defer srv.Close()

	// 推送式补号：webhook 载荷里的两个 id 都要原样带上。
	_, err := newTestKiroappio(t, srv).Claim(supplierClaimRequest{
		Count:         1,
		ClientOrderID: "d5c4fd9460b70fb8e944bd7faa519896",
		BatchOrderID:  "0d9f-batch",
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := gotBody["client_order_id"]; got != "d5c4fd9460b70fb8e944bd7faa519896" {
		t.Errorf("client_order_id = %v, want the supplied idempotency key", got)
	}
	if got := gotBody["order_id"]; got != "0d9f-batch" {
		t.Errorf("order_id = %v, want the supplied batch id", got)
	}
}

func TestKiroappioClaimNoKeysIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"purchased": 0, "keys": []interface{}{}})
	}))
	defer srv.Close()

	if _, err := newTestKiroappio(t, srv).Claim(supplierClaimRequest{Count: 3}); err == nil {
		t.Fatal("expected error when purchase returns no keys")
	}
}

func TestKiroappioStockAndAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me/stock":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"stock": 120, "price": 30, "price_min": 30, "price_max": 65, "balance": 2060,
			})
		case "/api/me/profile":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"user": map[string]interface{}{"name": "alice", "balance": 2060},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestKiroappio(t, srv)

	stock, err := c.Stock()
	if err != nil {
		t.Fatalf("Stock: %v", err)
	}
	if stock != 120 {
		t.Errorf("Stock() = %d, want 120", stock)
	}

	acc, err := c.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acc.Name != "alice" || acc.Remaining != 2060 {
		t.Errorf("Account() = %+v, want name=alice remaining=2060", acc)
	}
	// 阶梯定价：下限取 price_min，上限只在确有区间时给出。
	if !acc.HasPrice || acc.KeyPrice != 30 {
		t.Errorf("KeyPrice = %v (has=%v), want 30", acc.KeyPrice, acc.HasPrice)
	}
	if acc.PriceMax != 65 {
		t.Errorf("PriceMax = %v, want 65", acc.PriceMax)
	}
	// kiroapp.io 不提供配额概念。
	if acc.HasQuota {
		t.Error("HasQuota = true, want false for kiroappio")
	}
}

func TestKiroappioFlatPriceOmitsRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me/stock":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"stock": 5, "price": 30, "price_min": 30, "price_max": 30,
			})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{"user": map[string]interface{}{"balance": 10}})
		}
	}))
	defer srv.Close()

	acc, err := newTestKiroappio(t, srv).Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	// 单一价位时不该报出 "30~30" 这样的伪区间。
	if acc.PriceMax != 0 {
		t.Errorf("PriceMax = %v, want 0 when price_max == price_min", acc.PriceMax)
	}
}

func TestKiroappioErrorResponseSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "token expired"})
	}))
	defer srv.Close()

	_, err := newTestKiroappio(t, srv).Stock()
	if err == nil {
		t.Fatal("expected error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "token expired") {
		t.Errorf("error %q should contain the upstream reason", err)
	}
}

// --- 触发策略 ---

func TestReplenishTrigger(t *testing.T) {
	const (
		batch   = 5
		allDead = 8
	)
	base := config.ReplenishConfig{MinPoolSize: 3, BatchCount: batch}

	tests := []struct {
		name      string
		rc        config.ReplenishConfig
		available int
		health    config.CredentialHealth
		wantCount int
	}{
		{
			name:      "pool healthy: no trigger",
			rc:        base,
			available: 10,
			health:    config.CredentialHealth{Total: 10, Enabled: 10},
			wantCount: 0,
		},
		{
			name:      "low water mark fires with batchCount",
			rc:        base,
			available: 1,
			health:    config.CredentialHealth{Total: 10, Enabled: 10},
			wantCount: batch,
		},
		{
			name:      "at threshold does not fire",
			rc:        base,
			available: 3,
			health:    config.CredentialHealth{Total: 10, Enabled: 10},
			wantCount: 0,
		},
		{
			name:      "minPoolSize 0 disables low-water trigger",
			rc:        config.ReplenishConfig{MinPoolSize: 0, BatchCount: batch},
			available: 0,
			health:    config.CredentialHealth{Total: 10, Enabled: 10},
			wantCount: 0,
		},
		{
			// 全部凭证禁用但开关未开：只应走低水位。
			name:      "all dead but trigger disabled falls back to low water",
			rc:        base,
			available: 0,
			health:    config.CredentialHealth{Total: 4},
			wantCount: batch,
		},
		{
			name: "all dead fires with allDeadCount, taking precedence",
			rc: config.ReplenishConfig{
				MinPoolSize: 3, BatchCount: batch,
				AllDeadReplenish: true, AllDeadCount: allDead,
			},
			available: 0,
			health:    config.CredentialHealth{Total: 4},
			wantCount: allDead,
		},
		{
			// allDeadCount 未设置时回退到 batchCount。
			name: "all dead with unset count falls back to batchCount",
			rc: config.ReplenishConfig{
				MinPoolSize: 3, BatchCount: batch,
				AllDeadReplenish: true,
			},
			available: 0,
			health:    config.CredentialHealth{Total: 4},
			wantCount: batch,
		},
		{
			// 空池是全新安装，不是「号全死了」，交给低水位处理。
			name: "empty credential store does not count as all-dead",
			rc: config.ReplenishConfig{
				MinPoolSize: 0, BatchCount: batch,
				AllDeadReplenish: true, AllDeadCount: allDead,
			},
			available: 0,
			health:    config.CredentialHealth{},
			wantCount: 0,
		},
		{
			// 关键用例：账号只是临时隔离、会自动恢复，不应买号。
			name: "all disabled but pending auto-restore does not fire",
			rc: config.ReplenishConfig{
				MinPoolSize: 0, BatchCount: batch,
				AllDeadReplenish: true, AllDeadCount: allDead,
			},
			available: 0,
			health:    config.CredentialHealth{Total: 3, PendingAutoRestore: 3},
			wantCount: 0,
		},
		{
			// 还有一个活着就不算全死。
			name: "one credential alive does not fire all-dead",
			rc: config.ReplenishConfig{
				MinPoolSize: 0, BatchCount: batch,
				AllDeadReplenish: true, AllDeadCount: allDead,
			},
			available: 1,
			health:    config.CredentialHealth{Total: 4, Enabled: 1},
			wantCount: 0,
		},
		{
			// 全死且两个 count 都是 0：无从判断买多少，不应触发。
			name: "all dead with zero counts does not fire",
			rc: config.ReplenishConfig{
				MinPoolSize: 0, AllDeadReplenish: true,
			},
			available: 0,
			health:    config.CredentialHealth{Total: 4},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			count, reason := replenishTrigger(tc.rc, tc.available, tc.health)
			if count != tc.wantCount {
				t.Errorf("count = %d, want %d (reason=%q)", count, tc.wantCount, reason)
			}
			if count > 0 && reason == "" {
				t.Error("a firing trigger must report a reason")
			}
		})
	}
}

func TestCredentialHealthAllDisabled(t *testing.T) {
	tests := []struct {
		name string
		h    config.CredentialHealth
		want bool
	}{
		{"empty store", config.CredentialHealth{}, false},
		{"all enabled", config.CredentialHealth{Total: 3, Enabled: 3}, false},
		{"partially enabled", config.CredentialHealth{Total: 3, Enabled: 1}, false},
		{"all dead", config.CredentialHealth{Total: 3}, true},
		{"all quarantined", config.CredentialHealth{Total: 3, PendingAutoRestore: 3}, false},
		{"mixed dead and quarantined", config.CredentialHealth{Total: 3, PendingAutoRestore: 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.h.AllDisabled(); got != tc.want {
				t.Errorf("AllDisabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- 配置访问器 ---

func TestReplenishConfigActiveConnection(t *testing.T) {
	rc := config.ReplenishConfig{
		BaseURL: "https://vendor.example.com", ApiKey: "usr-x",
		KiroappBaseURL: "", KiroappApiKey: "k-1",
		KiroappioBaseURL: "", KiroappioApiKey: "km_1",
	}

	// 默认（空 Provider）走 vendor。
	if got := rc.ActiveBaseURL(); got != "https://vendor.example.com" {
		t.Errorf("vendor ActiveBaseURL() = %q", got)
	}
	if got := rc.ActiveApiKey(); got != "usr-x" {
		t.Errorf("vendor ActiveApiKey() = %q", got)
	}
	if !rc.SupportsWebhook() {
		t.Error("vendor should support webhook")
	}
	// 只有 vendor 能通过 API 自动注册回调。
	if !rc.SupportsWebhookAutoRegister() {
		t.Error("vendor should support webhook auto-register")
	}

	// 切到 kiroapp 后连接信息应整体切换，且 vendor 的密钥仍保留在配置里。
	rc.Provider = "kiroapp"
	if got := rc.ActiveBaseURL(); got != config.DefaultKiroappBaseURL {
		t.Errorf("kiroapp ActiveBaseURL() = %q, want default", got)
	}
	if got := rc.ActiveApiKey(); got != "k-1" {
		t.Errorf("kiroapp ActiveApiKey() = %q", got)
	}
	if rc.SupportsWebhook() {
		t.Error("kiroapp must not report webhook support")
	}

	// kiroapp.io 能收推送，但回调地址只能在其站点后台手填。
	rc.Provider = "kiroappio"
	if got := rc.ActiveBaseURL(); got != config.DefaultKiroappioBaseURL {
		t.Errorf("kiroappio ActiveBaseURL() = %q, want default", got)
	}
	if got := rc.ActiveApiKey(); got != "km_1" {
		t.Errorf("kiroappio ActiveApiKey() = %q", got)
	}
	if !rc.SupportsWebhook() {
		t.Error("kiroappio should support webhook (inbound push)")
	}
	if rc.SupportsWebhookAutoRegister() {
		t.Error("kiroappio has no register API; must not claim auto-register")
	}

	// 三个供应商的凭证互不覆盖。
	if rc.ApiKey != "usr-x" || rc.KiroappApiKey != "k-1" {
		t.Error("switching provider must not clear the other providers' keys")
	}
}
