package proxy

// 单家手动补号（runReplenishOnce 传入 provider）的守卫测试。
//
// 这条路径花的是真钱，因此断言集中在「什么情况下必须拒绝下单」上：
//
//   - 未知供应商标识必须报错，绝不能静默退化成「补所有家」——那会动到每一家的
//     余额，而用户以为只补了一家。
//   - 未启用 / 没填密钥的家必须报错，而不是返回一个 purchased=0 的成功结构：
//     后者在面板上表现为「点了没反应」，用户只会重复点击。
//   - count 的回退顺序：显式入参 > 配置 BatchCount > 报错。
//
// 真正的下单动作（replenishOneProvider → Claim → ImportApiKeys）依赖上游 HTTP，
// 已由 replenish_clients_test.go 覆盖到各家客户端一层；这里只验证在到达下单之前
// 的判定是否正确，因此所有用例都刻意停在「还没发出请求」的状态。

import (
	"strings"
	"testing"

	"kiro-go/config"
)

// newSingleReplenishTestConfig 初始化一份空配置，并按需写入某家供应商。
func newSingleReplenishTestConfig(t *testing.T) {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
}

// setSupplier 写入一家供应商的启用状态与密钥。
func setSupplier(t *testing.T, provider string, enabled bool, apiKey, baseURL string, batchCount int) {
	t.Helper()
	rc := config.GetReplenishConfig()
	rc.BatchCount = batchCount
	up := config.SupplierUpdate{
		Enabled: &enabled,
		ApiKey:  &apiKey,
	}
	if baseURL != "" {
		up.BaseURL = &baseURL
	}
	if err := config.UpdateReplenishSettings(rc, map[string]config.SupplierUpdate{
		provider: up,
	}); err != nil {
		t.Fatalf("UpdateReplenishSettings: %v", err)
	}
}

func TestRunReplenishOnceRejectsUnknownProvider(t *testing.T) {
	newSingleReplenishTestConfig(t)
	h := &Handler{}

	_, err := h.runReplenishOnce("not-a-vendor", 1)
	if err == nil {
		t.Fatal("expected an error for an unknown provider: silently falling back to " +
			"'replenish every supplier' would spend money on vendors the user did not pick")
	}
	if !strings.Contains(err.Error(), "unknown replenish provider") {
		t.Errorf("error = %q, want it to name the unknown-provider cause", err)
	}
}

func TestRunReplenishOnceRejectsDisabledProvider(t *testing.T) {
	newSingleReplenishTestConfig(t)
	// 有密钥但开关是关的。
	setSupplier(t, config.ReplenishProviderKiroappio, false, "km_test", "", 5)

	h := &Handler{}
	_, err := h.runReplenishOnce(config.ReplenishProviderKiroappio, 1)
	if err == nil {
		t.Fatal("expected an error for a disabled supplier: returning a purchased=0 " +
			"success reads as 'the button did nothing'")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error = %q, want it to mention the supplier being disabled", err)
	}
}

func TestRunReplenishOnceRejectsMissingApiKey(t *testing.T) {
	newSingleReplenishTestConfig(t)
	// 开关开了但没填密钥。
	setSupplier(t, config.ReplenishProviderKiroappio, true, "", "", 5)

	h := &Handler{}
	_, err := h.runReplenishOnce(config.ReplenishProviderKiroappio, 1)
	if err == nil {
		t.Fatal("expected an error when the supplier has no api key")
	}
	if !strings.Contains(err.Error(), "api key") {
		t.Errorf("error = %q, want it to mention the missing api key", err)
	}
}

// count <= 0 且配置里的 BatchCount 也 <= 0 时必须报错，不能按 0 下单。
func TestRunReplenishOnceRejectsNonPositiveCount(t *testing.T) {
	newSingleReplenishTestConfig(t)
	setSupplier(t, config.ReplenishProviderKiroappio, true, "km_test", "", 0)

	h := &Handler{}
	_, err := h.runReplenishOnce(config.ReplenishProviderKiroappio, 0)
	if err == nil {
		t.Fatal("expected an error when neither the request nor the config supplies a count")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error = %q, want it to mention the count having to be positive", err)
	}
}

// 别名必须与正式标识等价：面板传 "kiroapp.io"、旧配置传 "default" 都应命中同一家。
func TestRunReplenishOnceAcceptsProviderAliases(t *testing.T) {
	for _, alias := range []string{"kiroapp.io", "KiroAppIO"} {
		t.Run(alias, func(t *testing.T) {
			newSingleReplenishTestConfig(t)
			// 刻意留空密钥：这样调用会在「校验密钥」这一步失败，而不是真的去下单。
			// 我们要断言的是别名被解析成了 kiroappio（错误信息里点名了这家），
			// 而不是被当成未知标识拒掉。
			setSupplier(t, config.ReplenishProviderKiroappio, true, "", "", 5)

			h := &Handler{}
			_, err := h.runReplenishOnce(alias, 1)
			if err == nil {
				t.Fatal("expected the missing-api-key error")
			}
			if !strings.Contains(err.Error(), config.ReplenishProviderKiroappio) {
				t.Errorf("error = %q, want the alias %q resolved to %q",
					err, alias, config.ReplenishProviderKiroappio)
			}
			if strings.Contains(err.Error(), "unknown") {
				t.Errorf("alias %q was rejected as unknown; NormalizeReplenishProvider "+
					"should map it to %q", alias, config.ReplenishProviderKiroappio)
			}
		})
	}
}

// provider 为空必须走「所有启用的供应商」这条老路径，不能被当成未知标识拒掉，
// 否则后台轮询与面板上的「全部补号」会一起失效。
func TestRunReplenishOnceEmptyProviderStillMeansAll(t *testing.T) {
	newSingleReplenishTestConfig(t)
	// 一家都没启用：replenishAll 应当以「没有启用的供应商」报错，
	// 而不是以「未知供应商」报错——两者指向完全不同的排查方向。
	h := &Handler{}
	_, err := h.runReplenishOnce("", 1)
	if err == nil {
		t.Fatal("expected an error when no supplier is enabled")
	}
	if strings.Contains(err.Error(), "unknown replenish provider") {
		t.Errorf("error = %q: an empty provider must mean 'all suppliers', "+
			"not an unknown one", err)
	}
	if !strings.Contains(err.Error(), "enabled") {
		t.Errorf("error = %q, want it to mention that no supplier is enabled", err)
	}
}
