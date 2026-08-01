package proxy

// 两家供应商客户端的请求构造与响应解析测试（HTTP 打桩，不打真实上游）。
//
// 重点覆盖与花钱直接相关的语义：
//   - 认证头各家不同（kiross: X-API-Key；kiroappio: Authorization: Bearer）。
//   - purchase 必须带上调用方给的 client_order_id，不能自己另造一个——否则
//     webhook 重试会绕过上游幂等而二次扣费。
//   - kiroappio 的 order_id（开号批次）只在提供时才带上。
//   - 上游错误原因要透传，否则面板只能显示裸状态码。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-go/config"
)

// initTestConfig 初始化全局配置。客户端的 do() 会读全局出站代理配置
// （config.GetProxyURL），未初始化时会 nil deref。
func initTestConfig(t *testing.T) {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
}

// --- kiross（X-API-Key, /api/my/*）---

func TestKirossClaimSendsOrderIDAndAuth(t *testing.T) {
	initTestConfig(t)

	var gotAuth, gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"client_order_id": "abc123",
			"purchased":       2,
			"remaining":       80,
			"keys": []map[string]string{
				{"key": "ksk_a"},
				{"key": " "},
				{"key": "ksk_b"},
			},
		})
	}))
	defer srv.Close()

	c, err := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	if err != nil {
		t.Fatalf("newKirossClient: %v", err)
	}
	claim, err := c.Claim(supplierClaimRequest{Count: 2, ClientOrderID: "abc123"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if gotAuth != "usr-x" {
		t.Errorf("X-API-Key = %q, want usr-x", gotAuth)
	}
	if gotPath != "/api/my/purchase" {
		t.Errorf("path = %q, want /api/my/purchase", gotPath)
	}
	// 幂等键必须原样上报。
	if got, _ := gotBody["client_order_id"].(string); got != "abc123" {
		t.Errorf("client_order_id = %q, want the caller's abc123", got)
	}
	if got, _ := gotBody["count"].(float64); int(got) != 2 {
		t.Errorf("count = %v, want 2", gotBody["count"])
	}
	// 空白 key 应被丢弃。
	if len(claim.Keys) != 2 || claim.Keys[0] != "ksk_a" || claim.Keys[1] != "ksk_b" {
		t.Errorf("Keys = %v, want [ksk_a ksk_b]", claim.Keys)
	}
	if claim.Purchased != 2 {
		t.Errorf("Purchased = %d, want 2", claim.Purchased)
	}
	if claim.Remaining != 80 {
		t.Errorf("Remaining = %v, want 80", claim.Remaining)
	}
}

func TestKirossClaimGeneratesOrderIDWhenAbsent(t *testing.T) {
	initTestConfig(t)

	var gotOrderID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		gotOrderID, _ = body["client_order_id"].(string)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"purchased": 1,
			"keys":      []map[string]string{{"key": "ksk_x"}},
		})
	}))
	defer srv.Close()

	c, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	if _, err := c.Claim(supplierClaimRequest{Count: 1}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// 接口必填该字段，缺失会 400，所以框架没给时客户端要自己生成 32 位十六进制。
	if len(gotOrderID) != 32 {
		t.Errorf("generated client_order_id = %q, want 32 hex chars", gotOrderID)
	}
	if strings.TrimLeft(gotOrderID, "0123456789abcdef") != "" {
		t.Errorf("client_order_id = %q, want lowercase hex only", gotOrderID)
	}
}

// kiross 没有库存与档案接口（取号文档只有 purchase 与 webhook）。
// Stock 必须返回 -1 表示「上限未知」——若误返回 0，编排层会把它当成「没货」而
// 跳过补号，这家就永远补不到号。
func TestKirossStockIsUnknown(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Stock() must not call upstream, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	stock, err := c.Stock()
	if err != nil {
		t.Fatalf("Stock: %v", err)
	}
	if stock != -1 {
		t.Errorf("Stock() = %d, want -1 (unknown, so the caller does not clamp)", stock)
	}
}

// 「测试连接」只能靠文档里的 POST /api/my/webhook/test 探连通性：
// 200 说明密钥有效。该家不返回余额/配额，因此不能声称有这些字段。
func TestKirossAccountProbesWebhookTest(t *testing.T) {
	initTestConfig(t)

	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	acc, err := c.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/my/webhook/test" {
		t.Errorf("probe = %s %s, want POST /api/my/webhook/test", gotMethod, gotPath)
	}
	if gotAuth != "usr-x" {
		t.Errorf("X-API-Key = %q, want usr-x", gotAuth)
	}
	// 没有余额/配额就不要谎报有，否则面板会显示一个凭空的 0。
	if acc.HasQuota || acc.HasPrice {
		t.Errorf("account = %+v, want no quota/price claims for kiross", acc)
	}
}

// 密钥无效时「测试连接」必须失败，而不是被当成连通成功。
func TestKirossAccountFailsOnUnauthorized(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid api key"})
	}))
	defer srv.Close()

	c, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "bad"})
	if _, err := c.Account(); err == nil {
		t.Fatal("expected an error on HTTP 401")
	}
}

func TestKirossSetWebhook(t *testing.T) {
	initTestConfig(t)

	var gotMethod, gotPath, gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		gotURL = body["webhook_url"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	if err := c.SetWebhook("https://me.example.com/replenish/webhook/kiross/s3cr3t"); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/my/webhook" {
		t.Errorf("%s %s, want PUT /api/my/webhook", gotMethod, gotPath)
	}
	if gotURL != "https://me.example.com/replenish/webhook/kiross/s3cr3t" {
		t.Errorf("webhook_url = %q", gotURL)
	}
}

func TestKirossErrorSurfacesUpstreamReason(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "count mismatch for order"})
	}))
	defer srv.Close()

	c, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	_, err := c.Claim(supplierClaimRequest{Count: 3, ClientOrderID: "dup"})
	if err == nil {
		t.Fatal("expected error on HTTP 409")
	}
	if !strings.Contains(err.Error(), "count mismatch for order") {
		t.Errorf("error %q should carry the upstream reason", err)
	}
}

// --- kiroappio（Bearer km_, /api/me/*）---

func TestKiroappioClaimSendsIdempotencyAndBatch(t *testing.T) {
	initTestConfig(t)

	var gotAuth, gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me/profile" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"user": map[string]interface{}{"name": "bob", "balance": 1870},
			})
			return
		}
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"purchased":   2,
			"requested":   2,
			"remaining":   115,
			"total_debit": 68,
			"order_id":    "batch-9",
			"keys": []map[string]interface{}{
				{"key": "sk-a", "price": 30},
				{"key": "sk-b", "price": 38},
			},
		})
	}))
	defer srv.Close()

	c, err := newKiroappioClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "km_tok"})
	if err != nil {
		t.Fatalf("newKiroappioClient: %v", err)
	}
	claim, err := c.Claim(supplierClaimRequest{
		Count: 2, ClientOrderID: "d5c4fd94", BatchOrderID: "batch-9",
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if gotAuth != "Bearer km_tok" {
		t.Errorf("Authorization = %q, want Bearer km_tok", gotAuth)
	}
	if gotPath != "/api/me/purchase" {
		t.Errorf("path = %q, want /api/me/purchase", gotPath)
	}
	// 推送里的两个 id 必须原样带上：client_order_id 保幂等，order_id 限定批次。
	if got, _ := gotBody["client_order_id"].(string); got != "d5c4fd94" {
		t.Errorf("client_order_id = %q, want d5c4fd94", got)
	}
	if got, _ := gotBody["order_id"].(string); got != "batch-9" {
		t.Errorf("order_id = %q, want batch-9", got)
	}
	if len(claim.Keys) != 2 {
		t.Errorf("Keys = %v, want 2 keys", claim.Keys)
	}
	// total_debit 是权威扣费数字，不是 count × 单价。
	if claim.Spent != 68 {
		t.Errorf("Spent = %v, want 68 from total_debit", claim.Spent)
	}
	// remaining 在该家响应里是剩余库存；余额要另查档案。
	if claim.Remaining != 1870 {
		t.Errorf("Remaining = %v, want 1870 (balance from profile)", claim.Remaining)
	}
}

func TestKiroappioClaimOmitsBatchWhenAbsent(t *testing.T) {
	initTestConfig(t)

	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me/profile" {
			json.NewEncoder(w).Encode(map[string]interface{}{"user": map[string]interface{}{"balance": 10}})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"purchased": 1,
			"keys":      []map[string]interface{}{{"key": "sk-x"}},
		})
	}))
	defer srv.Close()

	c, _ := newKiroappioClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "km_tok"})
	if _, err := c.Claim(supplierClaimRequest{Count: 1, ClientOrderID: "abc"}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// 没有批次时不能带空 order_id，否则可能被上游当成「只取某个不存在的批次」。
	if _, present := gotBody["order_id"]; present {
		t.Errorf("order_id should be omitted when no batch id is given, got %v", gotBody["order_id"])
	}
}

func TestKiroappioStockAndAccount(t *testing.T) {
	initTestConfig(t)

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

	c, _ := newKiroappioClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "km_tok"})

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
		t.Errorf("Account = %+v, want alice/2060", acc)
	}
	// 阶梯定价：下限取 price_min，上限单独给出。
	if !acc.HasPrice || acc.KeyPrice != 30 || acc.PriceMax != 65 {
		t.Errorf("price = %v~%v (has=%v), want 30~65", acc.KeyPrice, acc.PriceMax, acc.HasPrice)
	}
	// 该家没有配额概念，不应假装有。
	if acc.HasQuota {
		t.Error("HasQuota = true, want false for kiroappio")
	}
}

func TestKiroappioDefaultBaseURL(t *testing.T) {
	c, err := newKiroappioClient(config.SupplierConfig{ApiKey: "km_1"})
	if err != nil {
		t.Fatalf("newKiroappioClient: %v", err)
	}
	if c.baseURL != config.DefaultKiroappioBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, config.DefaultKiroappioBaseURL)
	}

	// 显式配置时去掉末尾斜杠，避免拼出 //api/me/stock。
	c2, _ := newKiroappioClient(config.SupplierConfig{BaseURL: "https://mirror.example.com/", ApiKey: "km_1"})
	if c2.baseURL != "https://mirror.example.com" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c2.baseURL)
	}
}

func TestKiroappioNoKeysIsError(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"purchased": 0, "keys": []interface{}{}})
	}))
	defer srv.Close()

	c, _ := newKiroappioClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "km_tok"})
	// 出 0 个 Key 必须报错，否则会被当成一次成功的空补号而不被察觉。
	if _, err := c.Claim(supplierClaimRequest{Count: 3, ClientOrderID: "abc"}); err == nil {
		t.Fatal("expected an error when purchase returns no keys")
	}
}

func TestKiroappioErrorSurfacesUpstreamReason(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "insufficient balance"})
	}))
	defer srv.Close()

	c, _ := newKiroappioClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "km_tok"})
	_, err := c.Stock()
	if err == nil {
		t.Fatal("expected error on HTTP 403")
	}
	if !strings.Contains(err.Error(), "insufficient balance") {
		t.Errorf("error %q should carry the upstream reason", err)
	}
}

func TestClaimRejectsNonPositiveCount(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP call expected for non-positive count")
	}))
	defer srv.Close()

	ks, _ := newKirossClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	if _, err := ks.Claim(supplierClaimRequest{Count: 0}); err == nil {
		t.Error("kiross: expected error for count=0")
	}
	io2, _ := newKiroappioClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "km_tok"})
	if _, err := io2.Claim(supplierClaimRequest{Count: 0}); err == nil {
		t.Error("kiroappio: expected error for count=0")
	}
}

// --- kiroapp.cc 客户端 ---
//
// 与另两家的关键差异，也是这些用例要钉住的点：
//   1. 错误信封是嵌套的 {"error":{"type","message"}}，不是平坦的 {"error":"..."}。
//   2. claim 无幂等键，重试会重复扣费，因此调用方绝不能自动重试。
//   3. 单个提取返回 {"key":...}，批量返回 {"keys":[...]}，两种形态都要认。

func TestKiroappccClaimSingleAndBatch(t *testing.T) {
	initTestConfig(t)

	var gotAuth, gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/balance" {
			json.NewEncoder(w).Encode(map[string]float64{"balance": 12})
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotBody = nil
		json.NewDecoder(r.Body).Decode(&gotBody)
		if gotBody != nil && gotBody["count"] != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"keys": []string{"ksk_a", " ", "ksk_b"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"key": "ksk_one"})
	}))
	defer srv.Close()

	c, err := newKiroappccClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "k-1"})
	if err != nil {
		t.Fatalf("newKiroappccClient: %v", err)
	}

	// 单个提取：不带请求体。
	claim, err := c.Claim(supplierClaimRequest{Count: 1})
	if err != nil {
		t.Fatalf("Claim(1): %v", err)
	}
	if gotAuth != "Bearer k-1" {
		t.Errorf("Authorization = %q, want Bearer k-1", gotAuth)
	}
	if gotPath != "/openapi/claim" {
		t.Errorf("path = %q, want /openapi/claim", gotPath)
	}
	if len(claim.Keys) != 1 || claim.Keys[0] != "ksk_one" {
		t.Errorf("Keys = %v, want [ksk_one]", claim.Keys)
	}
	// 无幂等键的供应商不应伪造订单号，否则面板会显示一个没有意义的值。
	if claim.OrderID != "" {
		t.Errorf("OrderID = %q, want empty (kiroapp.cc has no idempotency key)", claim.OrderID)
	}

	// 批量提取：带 {"count":N}，且空白项要被丢掉。
	claim2, err := c.Claim(supplierClaimRequest{Count: 2})
	if err != nil {
		t.Fatalf("Claim(2): %v", err)
	}
	if got, ok := gotBody["count"].(float64); !ok || int(got) != 2 {
		t.Errorf("body count = %v, want 2", gotBody["count"])
	}
	if len(claim2.Keys) != 2 || claim2.Keys[0] != "ksk_a" || claim2.Keys[1] != "ksk_b" {
		t.Errorf("Keys = %v, want [ksk_a ksk_b]", claim2.Keys)
	}
}

func TestKiroappccNestedErrorSurfacesMessage(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "180")
		w.WriteHeader(http.StatusTooManyRequests)
		// 该家独有的嵌套信封。
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"type": "rate_limit_exceeded", "message": "too many requests", "retryAfter": 180,
			},
		})
	}))
	defer srv.Close()

	c, _ := newKiroappccClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "k-1"})
	_, err := c.Stock()
	if err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
	// 上游原因必须透传，否则面板只显示裸状态码，用户无法判断是限流还是密钥错。
	if !strings.Contains(err.Error(), "too many requests") {
		t.Errorf("error %q should carry the upstream message", err)
	}
	// 限流是可恢复的，退避秒数要能看到。
	if !strings.Contains(err.Error(), "180") {
		t.Errorf("error %q should carry retryAfter for backoff", err)
	}
}

func TestKiroappccStockAndAccount(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/stock":
			json.NewEncoder(w).Encode(map[string]interface{}{"availableKeys": 7, "keyPrice": 1.5})
		case "/openapi/balance":
			json.NewEncoder(w).Encode(map[string]float64{"balance": 99})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := newKiroappccClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "k-1"})

	stock, err := c.Stock()
	if err != nil {
		t.Fatalf("Stock: %v", err)
	}
	if stock != 7 {
		t.Errorf("Stock() = %d, want 7", stock)
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
	// 该家不提供配额，不应假装有。
	if acc.HasQuota {
		t.Error("HasQuota = true, want false for kiroapp.cc")
	}
}

func TestKiroappccDefaultBaseURL(t *testing.T) {
	c, err := newKiroappccClient(config.SupplierConfig{ApiKey: "k-1"})
	if err != nil {
		t.Fatalf("newKiroappccClient: %v", err)
	}
	if c.baseURL != config.DefaultKiroappccBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, config.DefaultKiroappccBaseURL)
	}
	// 末尾斜杠要去掉，避免拼出 //openapi/claim。
	c2, _ := newKiroappccClient(config.SupplierConfig{BaseURL: "https://mirror.example.com/", ApiKey: "k-1"})
	if c2.baseURL != "https://mirror.example.com" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c2.baseURL)
	}
}

func TestKiroappccNoKeysIsError(t *testing.T) {
	initTestConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": []string{}})
	}))
	defer srv.Close()

	c, _ := newKiroappccClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "k-1"})
	// 出 0 个 Key 必须报错，否则会被当成一次成功的空补号而不被察觉。
	if _, err := c.Claim(supplierClaimRequest{Count: 3}); err == nil {
		t.Fatal("expected an error when claim returns no keys")
	}
}

// --- kiro.ceo 客户端 ---

// 核心语义：zone 必须显式带上。文档明确「不传 zone 默认只从美国区取号，想要欧洲区
// 必须显式传 zone: eu」，所以漏传会静默买错区——那是花钱的错，必须锁死。
func TestKiroceoClaimSendsZoneAndOrderID(t *testing.T) {
	initTestConfig(t)

	var gotAuth, gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/my/stock" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"max":   50,
				"zones": []map[string]interface{}{{"zone": "eu", "price": 15, "available": 50}},
			})
			return
		}
		gotAuth = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"client_order_id": gotBody["client_order_id"],
			"purchased":       2,
			"remaining":       4500,
			"zone":            "eu",
			"unit_price":      15,
			"total_credits":   30,
			"keys": []map[string]string{
				{"key": "kiro-a", "account": "a@example.com"},
				{"key": "kiro-b", "account": "b@example.com"},
			},
		})
	}))
	defer srv.Close()

	c, err := newKiroceoClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x", Zone: "eu"})
	if err != nil {
		t.Fatalf("newKiroceoClient: %v", err)
	}
	claim, err := c.Claim(supplierClaimRequest{Count: 2, ClientOrderID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if gotAuth != "usr-x" {
		t.Errorf("X-API-Key = %q, want usr-x", gotAuth)
	}
	if gotPath != "/api/my/purchase" {
		t.Errorf("path = %q, want /api/my/purchase", gotPath)
	}
	if got := gotBody["zone"]; got != "eu" {
		t.Errorf("body zone = %v, want eu (europe requires an explicit zone)", got)
	}
	if got := gotBody["client_order_id"]; got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("client_order_id = %v, want the caller's idempotency key verbatim", got)
	}
	if len(claim.Keys) != 2 {
		t.Errorf("Keys = %v, want 2", claim.Keys)
	}
	// Zone 要回传，编排层据此决定导入区域。
	if claim.Zone != "eu" {
		t.Errorf("claim.Zone = %q, want eu", claim.Zone)
	}
	if claim.Spent != 30 {
		t.Errorf("Spent = %v, want 30 (total_credits)", claim.Spent)
	}
}

// zone 留空时按上游默认（us）处理，但仍显式传——不依赖上游的隐式默认。
func TestKiroceoDefaultsToUSZone(t *testing.T) {
	initTestConfig(t)

	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/my/stock" {
			json.NewEncoder(w).Encode(map[string]interface{}{"max": 10})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"purchased": 1, "keys": []map[string]string{{"key": "kiro-a"}},
		})
	}))
	defer srv.Close()

	c, _ := newKiroceoClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x"})
	if c.zone != config.SupplierZoneUS {
		t.Fatalf("zone = %q, want us by default", c.zone)
	}
	if _, err := c.Claim(supplierClaimRequest{Count: 1}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := gotBody["zone"]; got != "us" {
		t.Errorf("body zone = %v, want an explicit us", got)
	}
}

// 非法 zone 必须在构造时就拒绝：上游对非法 zone 直接 400，早失败比发一次必败请求好。
func TestKiroceoRejectsInvalidZone(t *testing.T) {
	if _, err := newKiroceoClient(config.SupplierConfig{
		BaseURL: "https://kiro.ceo", ApiKey: "usr-x", Zone: "apac",
	}); err == nil {
		t.Fatal("expected an error for an unsupported zone")
	}
}

// Stock 要取本区的 available，而不是跨区聚合的 max——采购按区隔离，
// 用 max 夹取会放过一个本区必然缺货的请求。
func TestKiroceoStockPrefersZoneAvailability(t *testing.T) {
	initTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"max": 100, // 跨区聚合
			"zones": []map[string]interface{}{
				{"zone": "us", "price": 20, "available": 95},
				{"zone": "eu", "price": 15, "available": 3},
			},
		})
	}))
	defer srv.Close()

	c, _ := newKiroceoClient(config.SupplierConfig{BaseURL: srv.URL, ApiKey: "usr-x", Zone: "eu"})
	stock, err := c.Stock()
	if err != nil {
		t.Fatalf("Stock: %v", err)
	}
	if stock != 3 {
		t.Errorf("Stock() = %d, want 3 (the eu zone's availability, not max=100)", stock)
	}

	acc, err := c.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if !acc.HasPrice || acc.KeyPrice != 15 {
		t.Errorf("KeyPrice = %v (has=%v), want the eu unit price 15", acc.KeyPrice, acc.HasPrice)
	}
}

// zone → 导入区域的映射是这次需求的关键：买欧洲区的号必须以 eu-central-1 导入，
// 否则 Key 带着错误区域进池，请求会打到错的 endpoint。
func TestEffectiveImportRegionFollowsZone(t *testing.T) {
	rc := config.ReplenishConfig{
		Region: "ap-northeast-1", // 全局设置，分区供应商不应受它影响
		Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiroceo: {Enabled: true, ApiKey: "usr-x", Zone: "eu"},
			config.ReplenishProviderKiross:  {Enabled: true, BaseURL: "https://v", ApiKey: "usr-y"},
		},
	}

	if got := rc.EffectiveImportRegion(config.ReplenishProviderKiroceo); got != "eu-central-1" {
		t.Errorf("kiro.ceo eu import region = %q, want eu-central-1", got)
	}
	// 无区域概念的家沿用全局设置。
	if got := rc.EffectiveImportRegion(config.ReplenishProviderKiross); got != "ap-northeast-1" {
		t.Errorf("kiross import region = %q, want the global region", got)
	}

	rc.Suppliers[config.ReplenishProviderKiroceo] = config.SupplierConfig{
		Enabled: true, ApiKey: "usr-x", Zone: "us",
	}
	if got := rc.EffectiveImportRegion(config.ReplenishProviderKiroceo); got != "us-east-1" {
		t.Errorf("kiro.ceo us import region = %q, want us-east-1", got)
	}

	// zone 留空按 us 处理，而不是回落到全局区域。
	rc.Suppliers[config.ReplenishProviderKiroceo] = config.SupplierConfig{Enabled: true, ApiKey: "usr-x"}
	if got := rc.EffectiveImportRegion(config.ReplenishProviderKiroceo); got != "us-east-1" {
		t.Errorf("kiro.ceo default import region = %q, want us-east-1", got)
	}
}
