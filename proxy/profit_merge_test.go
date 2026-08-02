package proxy

// apiGetAccounts 把两个数据源拼起来：持久化配置（config.GetAccounts）与运行态
// 池副本（pool.GetAllAccounts）。这个接缝此前没有任何测试覆盖，而利润显示错误
// 恰好就出在这里 —— config 侧与 pool 侧各自的单测都是绿的。
//
// 关键约束：池里只有「可调度」的账号（Reload 会剔除已禁用与超额受限的），所以
// map 查不到是常态而非异常，必须显式回落到持久值。Go 的 map 在键不存在时静默
// 返回零值，这个静默是 bug 的全部成因。

import (
	"encoding/json"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http/httptest"
	"testing"
)

// getAccountsJSON 调 apiGetAccounts 并按 id 索引返回结果。
func getAccountsJSON(t *testing.T, h *Handler) map[string]map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	h.apiGetAccounts(rec, httptest.NewRequest("GET", "/admin/api/accounts", nil))

	var got []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	byID := make(map[string]map[string]interface{}, len(got))
	for _, a := range got {
		id, _ := a["id"].(string)
		byID[id] = a
	}
	return byID
}

func num(t *testing.T, row map[string]interface{}, key string) float64 {
	t.Helper()
	v, _ := row[key].(float64)
	return v
}

// 禁用账号不在池里，其收入必须仍来自持久化配置。
//
// 否则收入读成 0、成本仍是真实值，面板上每个禁用账号的利润都是 -成本；且因为
// 成本非零，前端 hasData 为真，会当成一个确定的红色数字显示而不是「—」。禁用
// 正是淘汰耗尽 Key 的手段，这个误差随时间单调累积。
func TestApiGetAccountsKeepsRevenueForDisabledAccounts(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	// A retired key: disabled, but it earned money and cost money while alive.
	if err := config.AddAccount(config.Account{
		ID: "retired", AuthMethod: "api_key", KiroApiKey: "k-retired",
		AccessToken: "k-retired", Enabled: false,
		Cost: 2.0, Revenue: 50.0,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pool := accountpool.GetPool()
	pool.Reload() // disabled account is (correctly) absent from the routing pool
	h := &Handler{pool: pool}

	row := getAccountsJSON(t, h)["retired"]
	if row == nil {
		t.Fatal("retired account missing from the response")
	}
	if got := num(t, row, "cost"); got != 2.0 {
		t.Errorf("cost = %v, want 2.0", got)
	}
	if got := num(t, row, "revenue"); got != 50.0 {
		t.Errorf("revenue = %v, want 50.0; a disabled account is absent from the "+
			"routing pool, so the statsMap lookup returns a zero-value Account and "+
			"revenue reads 0 -> the panel shows profit = -cost", got)
	}
}

// 同一个零值查询会一并抹掉其余四个统计字段，它们同样是持久化的。
func TestApiGetAccountsKeepsUsageStatsForDisabledAccounts(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID: "retired", AuthMethod: "api_key", KiroApiKey: "k-retired",
		AccessToken: "k-retired", Enabled: false,
		RequestCount: 120, ErrorCount: 3, TotalTokens: 45000,
		TotalCredits: 7.5, LastUsed: 1700000000,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pool := accountpool.GetPool()
	pool.Reload()
	h := &Handler{pool: pool}

	row := getAccountsJSON(t, h)["retired"]
	if row == nil {
		t.Fatal("retired account missing from the response")
	}
	for _, c := range []struct {
		key  string
		want float64
	}{
		{"requestCount", 120},
		{"errorCount", 3},
		{"totalTokens", 45000},
		{"totalCredits", 7.5},
		{"lastUsed", 1700000000},
	} {
		if got := num(t, row, c.key); got != c.want {
			t.Errorf("%s = %v, want %v (persisted value must survive absence from the pool)",
				c.key, got, c.want)
		}
	}
}

// 启用账号仍须优先取池内副本：它比持久值新（最多领先一个落盘周期）。
// 回落逻辑不能反过来把新鲜数据换成旧的。
func TestApiGetAccountsPrefersPoolRevenueForEnabledAccounts(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID: "live", AuthMethod: "api_key", KiroApiKey: "k-live",
		AccessToken: "k-live", Enabled: true,
		Cost: 1.0, Revenue: 10.0, // 持久值：上一次 flush 的结果
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pool := accountpool.GetPool()
	pool.Reload()
	pool.AddRevenue("live", 5.0) // 尚未落盘，只存在于池内
	h := &Handler{pool: pool}

	row := getAccountsJSON(t, h)["live"]
	if row == nil {
		t.Fatal("live account missing from the response")
	}
	if got := num(t, row, "revenue"); got != 15.0 {
		t.Errorf("revenue = %v, want 15.0 (persisted 10 + unflushed 5); "+
			"enabled accounts must read the fresher in-memory total", got)
	}
}
