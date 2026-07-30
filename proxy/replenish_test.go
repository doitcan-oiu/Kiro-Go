package proxy

// 补号框架的测试。
//
// 三个供应商客户端已移除，因此这里不再有任何上游 HTTP 打桩，只覆盖与供应商无关的
// 框架层：
//  1. newReplenishSupplier 在没有任何实现时返回错误（而不是 panic 或静默不补）。
//  2. replenishTrigger 的两个触发条件（全部凭证禁用 / 低水位）。
//  3. CredentialHealth.AllDisabled 与补号配置访问器。
//
// 接回供应商时，把客户端自身的请求构造/响应解析测试放到各自的
// replenish_<provider>_test.go 里，本文件只测框架。

import (
	"testing"

	"kiro-go/config"
)

// --- 供应商工厂 ---

// 没有任何供应商实现时，工厂必须报错。这是当前唯一正确的行为：宁可让手动补号
// 返回可见的错误、让后台轮询把错误写进运行态，也不能静默地「成功但没补到号」。
func TestNewReplenishSupplierWithNoProvidersErrors(t *testing.T) {
	// 覆盖旧配置可能残留的各种 provider 值：都不该再选出实现。
	for _, provider := range []string{"", "default", "kiroapp", "kiroapp.cc", "kiroappio", "kiroapp.io", "whatever"} {
		t.Run("provider="+provider, func(t *testing.T) {
			rc := config.ReplenishConfig{
				Provider: provider,
				// 连接信息齐全也不该改变结果：没有实现就是没有实现。
				BaseURL: "https://vendor.example.com", ApiKey: "usr-x",
				KiroappApiKey: "k-1", KiroappioApiKey: "km_1",
			}
			client, err := newReplenishSupplier(rc)
			if err == nil {
				t.Fatalf("expected an error, got client %T", client)
			}
			if client != nil {
				t.Errorf("client = %T, want nil alongside the error", client)
			}
		})
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
