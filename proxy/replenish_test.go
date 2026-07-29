package proxy

// 补号的供应商选择与触发策略测试。
//
// 覆盖三块：
//  1. newReplenishSupplier 按 Provider 选择正确的客户端。
//  2. kiroappClient 对 /openapi/* 的请求构造与两种 claim 响应形态的解析。
//  3. replenishTrigger 的两个触发条件（全部凭证禁用 / 低水位）。

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

	claim, err := newTestKiroapp(t, srv).Claim(1, "ignored-order-id")
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

	claim, err := newTestKiroapp(t, srv).Claim(2, "")
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
	if _, err := newTestKiroapp(t, srv).Claim(3, ""); err == nil {
		t.Fatal("expected error when claim returns no keys")
	}
}

func TestKiroappClaimRejectsNonPositiveCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP call expected for non-positive count")
	}))
	defer srv.Close()

	if _, err := newTestKiroapp(t, srv).Claim(0, ""); err == nil {
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
	if rc.ApiKey != "usr-x" {
		t.Error("switching provider must not clear the other provider's key")
	}
}
