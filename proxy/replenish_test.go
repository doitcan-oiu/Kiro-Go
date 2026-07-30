package proxy

// 补号框架的测试（与具体供应商无关的那一层）。
//
// 覆盖：
//  1. newReplenishSupplier 按标识选出正确的客户端，未知标识报错。
//  2. newEnabledReplenishSuppliers 的「两家并行」语义：一家配错不拖累另一家。
//  3. replenishTrigger 的两个触发条件（全部凭证禁用 / 低水位）。
//  4. CredentialHealth.AllDisabled 与各供应商配置访问器。
//
// 两家客户端自身的请求构造/响应解析测试见 replenish_kiross_test.go 与
// replenish_kiroappio_test.go。

import (
	"os"
	"testing"

	"kiro-go/config"
)

// --- 供应商工厂 ---

func TestNewReplenishSupplierSelectsByProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		sc       config.SupplierConfig
		wantName string
		wantErr  bool
	}{
		{
			name:     "kiross",
			provider: "kiross",
			sc:       config.SupplierConfig{BaseURL: "https://vendor.example.com", ApiKey: "usr-x"},
			wantName: config.ReplenishProviderKiross,
		},
		{
			// 旧配置里这家叫 "default"，升级后应仍能识别。
			name:     "legacy default alias maps to kiross",
			provider: "default",
			sc:       config.SupplierConfig{BaseURL: "https://vendor.example.com", ApiKey: "usr-x"},
			wantName: config.ReplenishProviderKiross,
		},
		{
			name:     "kiroappio",
			provider: "kiroappio",
			sc:       config.SupplierConfig{ApiKey: "km_1"},
			wantName: config.ReplenishProviderKiroappio,
		},
		{
			name:     "kiroapp.io alias",
			provider: "kiroapp.io",
			sc:       config.SupplierConfig{ApiKey: "km_1"},
			wantName: config.ReplenishProviderKiroappio,
		},
		{
			// kiross 没有官方默认地址，缺 baseURL 必须报错而不是打一个空地址。
			name:     "kiross without baseUrl errors",
			provider: "kiross",
			sc:       config.SupplierConfig{ApiKey: "usr-x"},
			wantErr:  true,
		},
		{
			name:     "kiross without apiKey errors",
			provider: "kiross",
			sc:       config.SupplierConfig{BaseURL: "https://vendor.example.com"},
			wantErr:  true,
		},
		{
			// kiroappio 有默认地址，所以只缺令牌时报错。
			name:     "kiroappio without token errors",
			provider: "kiroappio",
			sc:       config.SupplierConfig{},
			wantErr:  true,
		},
		{
			name:     "kiroappcc",
			provider: "kiroappcc",
			sc:       config.SupplierConfig{ApiKey: "cc_1"},
			wantName: config.ReplenishProviderKiroappcc,
		},
		{
			name:     "kiroapp.cc alias",
			provider: "kiroapp.cc",
			sc:       config.SupplierConfig{ApiKey: "cc_1"},
			wantName: config.ReplenishProviderKiroappcc,
		},
		{
			// 这家也有默认地址，同样只缺密钥时报错。
			name:     "kiroappcc without key errors",
			provider: "kiroappcc",
			sc:       config.SupplierConfig{},
			wantErr:  true,
		},
		{
			name:     "unknown provider errors",
			provider: "nope",
			sc:       config.SupplierConfig{ApiKey: "x"},
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newReplenishSupplier(tc.provider, tc.sc)
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

// 核心语义：两家并行。只启用一家时只构造一家；一家凭证配错时另一家照常可用。
func TestNewEnabledReplenishSuppliers(t *testing.T) {
	good := config.SupplierConfig{Enabled: true, BaseURL: "https://vendor.example.com", ApiKey: "usr-x"}
	goodIO := config.SupplierConfig{Enabled: true, ApiKey: "km_1"}

	t.Run("both enabled yields both", func(t *testing.T) {
		rc := config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross:    good,
			config.ReplenishProviderKiroappio: goodIO,
		}}
		clients, errs, err := newEnabledReplenishSuppliers(rc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(clients) != 2 {
			t.Fatalf("got %d clients, want 2", len(clients))
		}
		if len(errs) != 0 {
			t.Errorf("errs = %v, want empty", errs)
		}
	})

	t.Run("disabled provider is skipped", func(t *testing.T) {
		rc := config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross:    good,
			config.ReplenishProviderKiroappio: {Enabled: false, ApiKey: "km_1"},
		}}
		clients, _, err := newEnabledReplenishSuppliers(rc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(clients) != 1 || clients[0].ProviderName() != config.ReplenishProviderKiross {
			t.Fatalf("got %d clients (%v), want only kiross", len(clients), clients)
		}
	})

	// 关键用例：一家配错不能让另一家也补不了号。
	t.Run("one misconfigured still yields the other", func(t *testing.T) {
		rc := config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross:    {Enabled: true, ApiKey: "usr-x"}, // 缺 baseURL
			config.ReplenishProviderKiroappio: goodIO,
		}}
		clients, errs, err := newEnabledReplenishSuppliers(rc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(clients) != 1 || clients[0].ProviderName() != config.ReplenishProviderKiroappio {
			t.Fatalf("got %d clients, want only kiroappio", len(clients))
		}
		if errs[config.ReplenishProviderKiross] == "" {
			t.Error("the misconfigured provider's reason should be reported")
		}
	})

	t.Run("none enabled errors", func(t *testing.T) {
		if _, _, err := newEnabledReplenishSuppliers(config.ReplenishConfig{}); err == nil {
			t.Fatal("expected an error when no supplier is enabled")
		}
	})

	t.Run("all enabled but misconfigured errors", func(t *testing.T) {
		rc := config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross: {Enabled: true},
		}}
		if _, _, err := newEnabledReplenishSuppliers(rc); err == nil {
			t.Fatal("expected an error when every enabled supplier fails to construct")
		}
	})
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

// 两家的连接信息必须完全独立：读某一家不会串到另一家，改一家也不影响另一家。
// 这是「两家同时买」的前提，串了就会用错密钥打错地址。
func TestSupplierConfigIsolation(t *testing.T) {
	rc := config.ReplenishConfig{
		Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross: {
				Enabled: true, BaseURL: "https://kiro.ss.example.com", ApiKey: "usr-x",
			},
			config.ReplenishProviderKiroappio: {
				Enabled: true, ApiKey: "km_1",
			},
		},
	}

	if got := rc.SupplierBaseURL(config.ReplenishProviderKiross); got != "https://kiro.ss.example.com" {
		t.Errorf("kiross SupplierBaseURL() = %q", got)
	}
	if got := rc.Supplier(config.ReplenishProviderKiross).ApiKey; got != "usr-x" {
		t.Errorf("kiross ApiKey = %q", got)
	}
	// kiroappio 的 baseURL 留空，应回落到官方默认地址。
	if got := rc.SupplierBaseURL(config.ReplenishProviderKiroappio); got != config.DefaultKiroappioBaseURL {
		t.Errorf("kiroappio SupplierBaseURL() = %q, want default", got)
	}
	if got := rc.Supplier(config.ReplenishProviderKiroappio).ApiKey; got != "km_1" {
		t.Errorf("kiroappio ApiKey = %q", got)
	}
	// kiross 没有官方默认地址，留空必须返回空串而不是瞎猜一个。
	rc.Suppliers[config.ReplenishProviderKiross] = config.SupplierConfig{Enabled: true, ApiKey: "usr-x"}
	if got := rc.SupplierBaseURL(config.ReplenishProviderKiross); got != "" {
		t.Errorf("kiross with empty baseURL = %q, want empty", got)
	}
}

// 只有 kiross 能通过 API 注册回调；kiroapp.io 必须手工填，不能谎报有该能力，
// 否则面板会给出一个点了必然失败的按钮。
func TestSupportsWebhookAutoRegister(t *testing.T) {
	if !config.SupportsWebhookAutoRegister(config.ReplenishProviderKiross) {
		t.Error("kiross should support webhook auto-register")
	}
	if config.SupportsWebhookAutoRegister(config.ReplenishProviderKiroappio) {
		t.Error("kiroappio has no register API; must not claim auto-register")
	}
}

// EnabledProviders 只返回启用的那些，且顺序稳定（决定补号与面板顺序）。
func TestEnabledProviders(t *testing.T) {
	tests := []struct {
		name string
		rc   config.ReplenishConfig
		want []string
	}{
		{
			name: "none configured",
			rc:   config.ReplenishConfig{},
			want: nil,
		},
		{
			name: "only kiroappio",
			rc: config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
				config.ReplenishProviderKiross:    {Enabled: false, ApiKey: "usr-x"},
				config.ReplenishProviderKiroappio: {Enabled: true, ApiKey: "km_1"},
			}},
			want: []string{config.ReplenishProviderKiroappio},
		},
		{
			// 三家全开：顺序必须与 ReplenishProviders() 声明一致，
			// 否则「先买哪家」会随 map 遍历顺序漂移，日志与摘要都对不上。
			name: "all three enabled keeps declared order",
			rc: config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
				config.ReplenishProviderKiroappcc: {Enabled: true, ApiKey: "cc_1"},
				config.ReplenishProviderKiroappio: {Enabled: true, ApiKey: "km_1"},
				config.ReplenishProviderKiross:    {Enabled: true, ApiKey: "usr-x"},
			}},
			want: []string{
				config.ReplenishProviderKiross,
				config.ReplenishProviderKiroappio,
				config.ReplenishProviderKiroappcc,
			},
		},
		{
			name: "both enabled keeps declared order",
			rc: config.ReplenishConfig{Suppliers: map[string]config.SupplierConfig{
				config.ReplenishProviderKiroappio: {Enabled: true, ApiKey: "km_1"},
				config.ReplenishProviderKiross:    {Enabled: true, ApiKey: "usr-x"},
			}},
			want: []string{config.ReplenishProviderKiross, config.ReplenishProviderKiroappio},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rc.EnabledProviders()
			if len(got) != len(tc.want) {
				t.Fatalf("EnabledProviders() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("EnabledProviders() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// 每家推送时买几个：优先用该家自己的设置，未设置才回退到全局 BatchCount。
// 这是本次需求的核心配置项，必须两家互不影响。
func TestEffectiveWebhookCount(t *testing.T) {
	rc := config.ReplenishConfig{
		BatchCount: 5,
		Suppliers: map[string]config.SupplierConfig{
			config.ReplenishProviderKiross:    {Enabled: true, WebhookCount: 3},
			config.ReplenishProviderKiroappio: {Enabled: true}, // 未设置 -> 回退
		},
	}
	if got := rc.EffectiveWebhookCount(config.ReplenishProviderKiross); got != 3 {
		t.Errorf("kiross EffectiveWebhookCount() = %d, want 3 (per-supplier)", got)
	}
	if got := rc.EffectiveWebhookCount(config.ReplenishProviderKiroappio); got != 5 {
		t.Errorf("kiroappio EffectiveWebhookCount() = %d, want 5 (global fallback)", got)
	}
}

// 旧版单选配置必须能自动迁移，否则升级后用户的密钥「凭空消失」还要重填。
func TestMigrateLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	// 手写一份旧结构：Provider 选中 kiroapp.io，两家的平铺凭证都在。
	legacy := `{"replenish":{"provider":"kiroappio",` +
		`"baseUrl":"https://vendor.example.com","apiKey":"usr-x",` +
		`"kiroappioApiKey":"km_1","webhookMaxCount":7,` +
		`"webhookSecret":"deadbeef","enabled":true,"batchCount":4}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	if err := config.Init(path); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	rc := config.GetReplenishConfig()

	// 被选中的那家应启用，并继承旧的单次提取上限作为推送购买数。
	ioCfg := rc.Supplier(config.ReplenishProviderKiroappio)
	if !ioCfg.Enabled {
		t.Error("previously selected provider should be enabled after migration")
	}
	if ioCfg.ApiKey != "km_1" {
		t.Errorf("kiroappio ApiKey = %q, want km_1 preserved", ioCfg.ApiKey)
	}
	if ioCfg.WebhookCount != 7 {
		t.Errorf("kiroappio WebhookCount = %d, want 7 from legacy webhookMaxCount", ioCfg.WebhookCount)
	}
	if ioCfg.WebhookSecret != "deadbeef" {
		t.Errorf("kiroappio WebhookSecret = %q, want the legacy secret reused", ioCfg.WebhookSecret)
	}

	// 未被选中的那家：凭证要保住，但不能自动启用——否则升级即开始向它扣费。
	ks := rc.Supplier(config.ReplenishProviderKiross)
	if ks.ApiKey != "usr-x" {
		t.Errorf("kiross ApiKey = %q, want usr-x preserved", ks.ApiKey)
	}
	if ks.BaseURL != "https://vendor.example.com" {
		t.Errorf("kiross BaseURL = %q, want preserved", ks.BaseURL)
	}
	if ks.Enabled {
		t.Error("provider that was not selected must stay disabled after migration")
	}

	// 策略字段保持原样。
	if !rc.Enabled || rc.BatchCount != 4 {
		t.Errorf("policy fields lost: enabled=%v batchCount=%d", rc.Enabled, rc.BatchCount)
	}
}

// --- webhook 事件名归一化 ---

// 各家事件命名不统一：kiross/kiroapp.io 用 new_keys_available，kiroapp.cc 实测推的是
// "stock"。曾因白名单里没有 "stock" 而整条推送被拒、错过补号，这里锁住该行为。
func TestClassifyWebhookEvent(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		event    string
		want     webhookEventKind
	}{
		// 回归用例：kiroapp.cc 真实推送的事件名。
		{"kiroappcc stock is a restock", config.ReplenishProviderKiroappcc, "stock", webhookEventNewKeys},
		{"kiroappcc new_stock", config.ReplenishProviderKiroappcc, "new_stock", webhookEventNewKeys},
		{"kiroappcc mixed case", config.ReplenishProviderKiroappcc, "  Stock  ", webhookEventNewKeys},
		// 无幂等键的一家：文档没给事件名清单，未知事件按「到货」处理，宁可多买一次
		// 也不要漏补。下单量取本地配置，不依赖载荷字段，所以这是安全的。
		{"kiroappcc unknown falls back to restock", config.ReplenishProviderKiroappcc, "whatever", webhookEventNewKeys},
		// 但探测类事件绝不能下单。
		{"kiroappcc test stays a probe", config.ReplenishProviderKiroappcc, "test", webhookEventProbe},
		{"kiroappcc ping stays a probe", config.ReplenishProviderKiroappcc, "ping", webhookEventProbe},

		// 有幂等键的两家事件名有明确文档，保持严格匹配。
		{"kiross documented event", config.ReplenishProviderKiross, "new_keys_available", webhookEventNewKeys},
		{"kiross unknown is rejected", config.ReplenishProviderKiross, "stock_weird", webhookEventUnknown},
		{"kiroappio unknown is rejected", config.ReplenishProviderKiroappio, "mystery", webhookEventUnknown},
		{"kiroappio revoked", config.ReplenishProviderKiroappio, "key_revoked_abuse", webhookEventRevoked},
		{"all dead", config.ReplenishProviderKiross, "all_keys_dead", webhookEventAllDead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWebhookEvent(tc.provider, tc.event); got != tc.want {
				t.Errorf("classifyWebhookEvent(%q, %q) = %v, want %v", tc.provider, tc.event, got, tc.want)
			}
		})
	}
}
