package config

// 账号生命周期时间戳（CreatedAt / DisabledAt）的行为约束。
//
// 这些字段驱动面板上的「添加时间」与「存活时长」两列。它们错了不会报错、不会崩，
// 只会显示一个看起来合理但错误的数字——比如把三个月前导入的账号显示成「刚刚添加」，
// 或者让存活时长随每次状态刷新而缩短。因此把口径钉进测试。

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestConfig 初始化一份隔离的配置，供单个测试独占。
func newTestConfig(t *testing.T) {
	t.Helper()
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func getAccount(t *testing.T, id string) Account {
	t.Helper()
	for _, a := range GetAccounts() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("account %s not found", id)
	return Account{}
}

func TestAddAccountStampsCreatedAt(t *testing.T) {
	newTestConfig(t)

	before := time.Now().Unix()
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	after := time.Now().Unix()

	got := getAccount(t, "a1")
	if got.CreatedAt < before || got.CreatedAt > after {
		t.Errorf("CreatedAt=%d outside [%d,%d]: import time must be stamped centrally "+
			"so all nine import paths get it without each remembering to",
			got.CreatedAt, before, after)
	}
	if got.DisabledAt != 0 {
		t.Errorf("DisabledAt=%d, want 0 for an account imported as enabled", got.DisabledAt)
	}
}

// 导入方已经给了 CreatedAt（例如从导出文件恢复）时不得覆盖，否则恢复备份会把
// 全部账号的添加时间重置为「今天」。
func TestAddAccountPreservesExplicitCreatedAt(t *testing.T) {
	newTestConfig(t)

	const original = 1700000000
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1", CreatedAt: original}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if got := getAccount(t, "a1").CreatedAt; got != original {
		t.Errorf("CreatedAt=%d, want %d preserved (restoring a backup must not "+
			"reset every account's import time to now)", got, original)
	}
}

// 导入时即为禁用状态：存活计时从导入那一刻就停止，DisabledAt 必须一起补上，
// 否则前端会用 0 计算出「存活至今」。
func TestAddAccountStampsDisabledAtWhenImportedDisabled(t *testing.T) {
	newTestConfig(t)

	if err := AddAccount(Account{ID: "a1", Enabled: false, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	got := getAccount(t, "a1")
	if got.DisabledAt == 0 {
		t.Error("DisabledAt=0 for an account imported already disabled: " +
			"alive-duration would be computed as running-until-now")
	}
	if got.DisabledAt != got.CreatedAt {
		t.Errorf("DisabledAt=%d should equal CreatedAt=%d when imported disabled",
			got.DisabledAt, got.CreatedAt)
	}
}

func TestAddAccountsBatchSharesOneTimestamp(t *testing.T) {
	newTestConfig(t)

	added, _, err := AddAccounts([]Account{
		{ID: "b1", Enabled: true, RefreshToken: "rt-b1"},
		{ID: "b2", Enabled: true, RefreshToken: "rt-b2"},
		{ID: "b3", Enabled: true, RefreshToken: "rt-b3"},
	})
	if err != nil {
		t.Fatalf("AddAccounts: %v", err)
	}
	if added != 3 {
		t.Fatalf("added=%d, want 3", added)
	}

	// 同一批导入的时间戳必须完全一致，否则按添加时间排序的顺序会不稳定。
	first := getAccount(t, "b1").CreatedAt
	if first == 0 {
		t.Fatal("batch import did not stamp CreatedAt")
	}
	for _, id := range []string{"b2", "b3"} {
		if got := getAccount(t, id).CreatedAt; got != first {
			t.Errorf("%s CreatedAt=%d, want %d (one batch = one timestamp)", id, got, first)
		}
	}
}

func TestSetAccountEnabledStampsAndClearsDisabledAt(t *testing.T) {
	newTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if err := SetAccountEnabled("a1", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	disabledAt := getAccount(t, "a1").DisabledAt
	if disabledAt == 0 {
		t.Fatal("DisabledAt not stamped on disable")
	}

	if err := SetAccountEnabled("a1", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := getAccount(t, "a1").DisabledAt; got != 0 {
		t.Errorf("DisabledAt=%d after re-enable, want 0: a re-enabled account is "+
			"alive again and must not keep a stale disable marker", got)
	}
}

// 重复禁用不得刷新 DisabledAt。这是本组测试里最重要的一条：SetAccountBanStatus
// 会被自动封禁流程反复调用，若每次都刷新时间戳，「已禁用多久」会被无限重置，
// 面板上永远显示「刚刚禁用」。
func TestRepeatedDisableKeepsFirstDisabledAt(t *testing.T) {
	newTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if err := SetAccountEnabled("a1", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	firstDisabledAt := getAccount(t, "a1").DisabledAt

	// 人为把时间戳推到过去，模拟「已经禁用了一小时」。
	if err := forceDisabledAt("a1", firstDisabledAt-3600); err != nil {
		t.Fatalf("forceDisabledAt: %v", err)
	}
	want := getAccount(t, "a1").DisabledAt

	// 再次禁用（自动封禁流程的典型行为）。
	if err := SetAccountEnabled("a1", false); err != nil {
		t.Fatalf("re-disable: %v", err)
	}
	if got := getAccount(t, "a1").DisabledAt; got != want {
		t.Errorf("DisabledAt moved from %d to %d on a repeat disable: "+
			"the panel would show 'just disabled' forever", want, got)
	}

	// SetAccountBanStatus 走的是另一条分支，同样不能刷新。
	if err := SetAccountBanStatus("a1", "BANNED", "test"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if got := getAccount(t, "a1").DisabledAt; got != want {
		t.Errorf("SetAccountBanStatus moved DisabledAt from %d to %d on an "+
			"already-disabled account", want, got)
	}
}

func TestSetAccountBanStatusStampsDisabledAtOnTransition(t *testing.T) {
	newTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if err := SetAccountBanStatus("a1", "BANNED", "quota"); err != nil {
		t.Fatalf("ban: %v", err)
	}

	got := getAccount(t, "a1")
	if got.Enabled {
		t.Error("account should be disabled after BANNED")
	}
	if got.DisabledAt == 0 {
		t.Error("DisabledAt not stamped when a ban disables an enabled account")
	}
}

// SUSPENDED 不禁用账号（自动隔离会稍后恢复），因此不应打 DisabledAt。
func TestSuspendDoesNotStampDisabledAt(t *testing.T) {
	newTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if err := SetAccountBanStatus("a1", "SUSPENDED", autoQuarantineSuspicious429Reason); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	if got := getAccount(t, "a1"); got.DisabledAt != 0 {
		t.Errorf("DisabledAt=%d for SUSPENDED, want 0: suspension is temporary "+
			"and the account stays in the pool", got.DisabledAt)
	}
}

// UpdateAccount 是整体替换，管理面板的启用/禁用开关走这条路径。
// 它必须自己维护 DisabledAt，否则前端 PUT 过来的对象里该字段是旧值。
func TestUpdateAccountMaintainsDisabledAt(t *testing.T) {
	newTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	// 模拟面板 PUT {enabled:false}：读出整体、改字段、写回。
	acc := getAccount(t, "a1")
	acc.Enabled = false
	if err := UpdateAccount("a1", acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if got := getAccount(t, "a1").DisabledAt; got == 0 {
		t.Error("UpdateAccount did not stamp DisabledAt on the enabled->disabled transition")
	}

	// 再启用回来。
	acc = getAccount(t, "a1")
	acc.Enabled = true
	if err := UpdateAccount("a1", acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if got := getAccount(t, "a1").DisabledAt; got != 0 {
		t.Errorf("DisabledAt=%d after re-enable via UpdateAccount, want 0", got)
	}
}

// UpdateAccount 不得覆盖既有的 CreatedAt——它是整体替换，最容易在这里丢字段。
func TestUpdateAccountPreservesCreatedAt(t *testing.T) {
	newTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	want := getAccount(t, "a1").CreatedAt

	acc := getAccount(t, "a1")
	acc.Weight = 5
	if err := UpdateAccount("a1", acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	if got := getAccount(t, "a1").CreatedAt; got != want {
		t.Errorf("CreatedAt=%d after update, want %d", got, want)
	}
}

// 老配置里没有 CreatedAt 的账号必须保持 0，不能在加载时回填成「现在」——
// 那会把所有历史账号显示成刚刚添加。
func TestLegacyAccountsKeepZeroCreatedAt(t *testing.T) {
	newTestConfig(t)

	// 直接以 CreatedAt=0 写入，模拟旧版本持久化的数据；再走一次读取。
	if err := AddAccount(Account{ID: "legacy", Enabled: true, RefreshToken: "rt-legacy", CreatedAt: 0}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	// AddAccount 会补齐新导入的账号，所以这里手工清零来模拟历史数据。
	if err := forceCreatedAt("legacy", 0); err != nil {
		t.Fatalf("forceCreatedAt: %v", err)
	}

	// 一次无关的更新不应该顺手把它填上。
	acc := getAccount(t, "legacy")
	acc.Weight = 2
	if err := UpdateAccount("legacy", acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	if got := getAccount(t, "legacy").CreatedAt; got != 0 {
		t.Errorf("CreatedAt=%d for a legacy account, want 0 (unknown): "+
			"back-filling would claim every old account was just added", got)
	}
}

// ── 测试辅助 ────────────────────────────────────────────────────────────────

func forceDisabledAt(id string, ts int64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == id {
			cfg.Accounts[i].DisabledAt = ts
			return Save()
		}
	}
	return nil
}

func forceCreatedAt(id string, ts int64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == id {
			cfg.Accounts[i].CreatedAt = ts
			return Save()
		}
	}
	return nil
}
