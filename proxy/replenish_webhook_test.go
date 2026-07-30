package proxy

// 推送式补号的鉴权与路由测试。
//
// 这是整套补号里唯一「外部请求能直接触发花钱」的入口，因此重点覆盖：
//  1. 每家一个专属密钥，密钥能反查出推送方（决定向谁下单）。
//  2. 跨供应商隔离：拿 A 的密钥不能触发买 B。
//  3. 每家的推送购买数量独立生效。

import (
	"testing"

	"kiro-go/config"
)

// --- 回调地址格式 ---

func TestReplenishWebhookPath(t *testing.T) {
	got := replenishWebhookPath("https://proxy.example.com", "kiross", "abc123")
	want := "https://proxy.example.com/replenish/webhook/kiross/abc123"
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	// 公网地址带尾斜杠时不能拼出双斜杠。
	if got := replenishWebhookPath("https://proxy.example.com/", "kiroappio", "def"); got != "https://proxy.example.com/replenish/webhook/kiroappio/def" {
		t.Errorf("trailing slash not trimmed: %q", got)
	}
}

// --- 密钥反查与隔离 ---

// 每家应拿到不同的密钥，且能各自反查回自己。这是「只买推送方」的前提：
// 若两家共用一个密钥，就无法判断是谁在推，只能猜。
func TestPerProviderWebhookSecretsAreDistinctAndResolvable(t *testing.T) {
	initTestConfig(t)

	ks, err := config.GetOrCreateSupplierWebhookSecret(config.ReplenishProviderKiross)
	if err != nil {
		t.Fatalf("secret for kiross: %v", err)
	}
	io, err := config.GetOrCreateSupplierWebhookSecret(config.ReplenishProviderKiroappio)
	if err != nil {
		t.Fatalf("secret for kiroappio: %v", err)
	}

	if ks == "" || io == "" {
		t.Fatal("secrets must not be empty")
	}
	if ks == io {
		t.Fatal("each provider must get its own secret; sharing one makes the pusher unidentifiable")
	}

	if got := config.FindProviderByWebhookSecret(ks); got != config.ReplenishProviderKiross {
		t.Errorf("lookup(kiross secret) = %q, want kiross", got)
	}
	if got := config.FindProviderByWebhookSecret(io); got != config.ReplenishProviderKiroappio {
		t.Errorf("lookup(kiroappio secret) = %q, want kiroappio", got)
	}

	// 重复调用应返回同一密钥，不能每次重新生成——否则已注册的回调地址会失效。
	again, _ := config.GetOrCreateSupplierWebhookSecret(config.ReplenishProviderKiross)
	if again != ks {
		t.Errorf("secret regenerated: %q != %q", again, ks)
	}
}

func TestFindProviderByWebhookSecretRejectsUnknown(t *testing.T) {
	initTestConfig(t)
	if _, err := config.GetOrCreateSupplierWebhookSecret(config.ReplenishProviderKiross); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	for _, bad := range []string{"", "nope", "deadbeef"} {
		if got := config.FindProviderByWebhookSecret(bad); got != "" {
			t.Errorf("lookup(%q) = %q, want empty", bad, got)
		}
	}
}

// 轮换一家的密钥不应影响另一家：旧地址只对被轮换的那家失效。
func TestResetSecretIsolatedPerProvider(t *testing.T) {
	initTestConfig(t)

	oldKs, _ := config.GetOrCreateSupplierWebhookSecret(config.ReplenishProviderKiross)
	io, _ := config.GetOrCreateSupplierWebhookSecret(config.ReplenishProviderKiroappio)

	newKs, err := config.ResetSupplierWebhookSecret(config.ReplenishProviderKiross)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if newKs == oldKs {
		t.Error("reset must produce a different secret")
	}
	// 旧密钥立即失效。
	if got := config.FindProviderByWebhookSecret(oldKs); got != "" {
		t.Errorf("old secret still resolves to %q", got)
	}
	// 另一家不受影响。
	if got := config.FindProviderByWebhookSecret(io); got != config.ReplenishProviderKiroappio {
		t.Errorf("other provider's secret broke: lookup = %q", got)
	}
}

// --- 每家独立的推送购买数量 ---

// 这是本次需求的核心：某家推送时，按「该家」配置的数量买。
func TestWebhookCountIsPerProvider(t *testing.T) {
	rc := config.ReplenishConfig{
		BatchCount: 5, // 全局兜底
		Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross:    {Enabled: true, WebhookCount: 3},
			config.ReplenishProviderKiroappio: {Enabled: true, WebhookCount: 20},
		},
	}

	if got := rc.EffectiveWebhookCount(config.ReplenishProviderKiross); got != 3 {
		t.Errorf("kiross webhook count = %d, want its own 3", got)
	}
	if got := rc.EffectiveWebhookCount(config.ReplenishProviderKiroappio); got != 20 {
		t.Errorf("kiroappio webhook count = %d, want its own 20", got)
	}

	// 未单独设置的那家回退到全局 BatchCount，而不是 0（0 会导致收到推送却不买）。
	rc.Suppliers[config.ReplenishProviderKiross] = config.SupplierConfig{Enabled: true}
	if got := rc.EffectiveWebhookCount(config.ReplenishProviderKiross); got != 5 {
		t.Errorf("unset webhook count = %d, want fallback to batchCount 5", got)
	}
}
