package config

// 利润核算的配置层测试。
//
// 覆盖三件容易出错、且出错后表现为「数字看着合理但其实是错的」的事：
//
//  1. ProfitMultiplier 用指针存储，为的是区分「从未配置」与「配置成 0」。
//     若退化成裸 float64，旧配置反序列化后是 0，所有收入会被乘成 0，
//     面板上每个账号的利润都变成负的采购成本 —— 静默且极难归因。
//  2. Cost 绑定在账号上，必须能穿过 UpdateAccount 的整体替换而不丢失。
//     面板保存权重/标签走的正是那条路径。
//  3. Revenue 是运行态累计值，同样不能被面板的一次保存清零。

import (
	"path/filepath"
	"testing"
)

func newProfitTestConfig(t *testing.T) {
	t.Helper()
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
}

func getAcc(t *testing.T, id string) Account {
	t.Helper()
	for _, a := range GetAccounts() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("account %s not found", id)
	return Account{}
}

// ── 倍率 ─────────────────────────────────────────────────────────────────────

func TestProfitMultiplierDefaultsToOne(t *testing.T) {
	newProfitTestConfig(t)
	// 未配置时必须是 1.0（按原价计收入），而不是 0。
	if got := GetProfitMultiplier(); got != DefaultProfitMultiplier {
		t.Errorf("GetProfitMultiplier() = %v, want %v on a fresh config",
			got, DefaultProfitMultiplier)
	}
}

func TestProfitMultiplierRoundTrips(t *testing.T) {
	newProfitTestConfig(t)
	if err := UpdateProfitMultiplier(2.5); err != nil {
		t.Fatalf("UpdateProfitMultiplier: %v", err)
	}
	if got := GetProfitMultiplier(); got != 2.5 {
		t.Errorf("GetProfitMultiplier() = %v, want 2.5", got)
	}
}

func TestProfitMultiplierRejectsNonPositive(t *testing.T) {
	newProfitTestConfig(t)
	if err := UpdateProfitMultiplier(2); err != nil {
		t.Fatalf("setup: %v", err)
	}

	for _, bad := range []float64{0, -1, -0.5} {
		if err := UpdateProfitMultiplier(bad); err == nil {
			t.Errorf("UpdateProfitMultiplier(%v) should be rejected: 0 makes every "+
				"revenue zero (looks like the feature is broken) and a negative "+
				"multiplier makes profit fall as usage rises", bad)
		}
	}
	// 被拒绝后原值必须保持不变，而不是被写坏。
	if got := GetProfitMultiplier(); got != 2 {
		t.Errorf("multiplier = %v after rejected writes, want the previous 2", got)
	}
}

func TestLegacyZeroMultiplierFallsBackToDefault(t *testing.T) {
	newProfitTestConfig(t)
	// 模拟旧配置/手工编辑：指针存在但值非法。
	zero := 0.0
	cfgLock.Lock()
	cfg.ProfitMultiplier = &zero
	cfgLock.Unlock()

	if got := GetProfitMultiplier(); got != DefaultProfitMultiplier {
		t.Errorf("GetProfitMultiplier() = %v with a persisted 0, want the default %v: "+
			"treating 0 as valid would zero out every account's revenue",
			got, DefaultProfitMultiplier)
	}
}

// ── 单 Key 成本 ──────────────────────────────────────────────────────────────

func TestUpdateAccountCostRoundTrips(t *testing.T) {
	newProfitTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if err := UpdateAccountCost("a1", 1.75); err != nil {
		t.Fatalf("UpdateAccountCost: %v", err)
	}
	if got := getAcc(t, "a1").Cost; got != 1.75 {
		t.Errorf("Cost = %v, want 1.75", got)
	}
}

func TestUpdateAccountCostRejectsNegative(t *testing.T) {
	newProfitTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1", Cost: 2}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if err := UpdateAccountCost("a1", -1); err == nil {
		t.Error("a negative cost must be rejected: it would inflate profit")
	}
	if got := getAcc(t, "a1").Cost; got != 2 {
		t.Errorf("Cost = %v after a rejected write, want the previous 2", got)
	}
}

func TestUpdateAccountCostAllowsZero(t *testing.T) {
	newProfitTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1", Cost: 5}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	// 0 是合法成本：自建/赠送的号确实没有采购成本。
	if err := UpdateAccountCost("a1", 0); err != nil {
		t.Fatalf("cost 0 must be allowed (free/self-provisioned keys): %v", err)
	}
	if got := getAcc(t, "a1").Cost; got != 0 {
		t.Errorf("Cost = %v, want 0", got)
	}
}

// ── 整体替换不能丢账 ─────────────────────────────────────────────────────────

func TestUpdateAccountPreservesCostAndRevenue(t *testing.T) {
	newProfitTestConfig(t)
	if err := AddAccount(Account{
		ID: "a1", Enabled: true, RefreshToken: "rt-1", Cost: 1.5, Revenue: 12.25,
	}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	// 面板保存权重时会把整个结构写回，且前端并不携带 revenue 字段。
	acc := getAcc(t, "a1")
	acc.Weight = 3
	acc.Revenue = 0 // 模拟前端漏传
	if err := UpdateAccount("a1", acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	got := getAcc(t, "a1")
	if got.Weight != 3 {
		t.Errorf("Weight = %d, want the edit to apply", got.Weight)
	}
	if got.Revenue != 12.25 {
		t.Errorf("Revenue = %v, want 12.25 preserved: an unrelated panel save must "+
			"not wipe the running revenue total", got.Revenue)
	}
	if got.Cost != 1.5 {
		t.Errorf("Cost = %v, want 1.5 preserved", got.Cost)
	}
}

func TestUpdateAccountCanStillChangeCost(t *testing.T) {
	newProfitTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1", Cost: 1}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	// 成本是用户可编辑的字段，整体替换必须让它生效（与 Revenue 相反）。
	acc := getAcc(t, "a1")
	acc.Cost = 4
	if err := UpdateAccount("a1", acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if got := getAcc(t, "a1").Cost; got != 4 {
		t.Errorf("Cost = %v, want the edit to apply (cost is user-editable)", got)
	}
}

func TestUpdateAccountRevenueAccumulates(t *testing.T) {
	newProfitTestConfig(t)
	if err := AddAccount(Account{ID: "a1", Enabled: true, RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	// pool.AddRevenue 传的是累计后的总额（不是增量），这里验证按此语义存储。
	if err := UpdateAccountRevenue("a1", 3.5); err != nil {
		t.Fatalf("UpdateAccountRevenue: %v", err)
	}
	if err := UpdateAccountRevenue("a1", 9.75); err != nil {
		t.Fatalf("UpdateAccountRevenue: %v", err)
	}
	if got := getAcc(t, "a1").Revenue; got != 9.75 {
		t.Errorf("Revenue = %v, want 9.75 (the accumulator passes totals, not deltas)", got)
	}
}

// ── 供应商单价 ───────────────────────────────────────────────────────────────

func TestSupplierKeyPriceRoundTrips(t *testing.T) {
	newProfitTestConfig(t)
	p := 1.25
	if err := UpdateReplenishSettings(GetReplenishConfig(), map[string]SupplierUpdate{
		ReplenishProviderKiross: {KeyPrice: &p},
	}); err != nil {
		t.Fatalf("UpdateReplenishSettings: %v", err)
	}
	if got := GetReplenishConfig().Supplier(ReplenishProviderKiross).KeyPrice; got != 1.25 {
		t.Errorf("KeyPrice = %v, want 1.25", got)
	}
}

func TestSupplierKeyPriceClampsNegative(t *testing.T) {
	newProfitTestConfig(t)
	neg := -3.0
	if err := UpdateReplenishSettings(GetReplenishConfig(), map[string]SupplierUpdate{
		ReplenishProviderKiross: {KeyPrice: &neg},
	}); err != nil {
		t.Fatalf("UpdateReplenishSettings: %v", err)
	}
	if got := GetReplenishConfig().Supplier(ReplenishProviderKiross).KeyPrice; got != 0 {
		t.Errorf("KeyPrice = %v for a negative input, want 0: a negative cost "+
			"would silently inflate profit", got)
	}
}

func TestSupplierKeyPriceOmittedKeepsStoredValue(t *testing.T) {
	newProfitTestConfig(t)
	p := 2.0
	if err := UpdateReplenishSettings(GetReplenishConfig(), map[string]SupplierUpdate{
		ReplenishProviderKiross: {KeyPrice: &p},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// 只改别的字段时（KeyPrice 为 nil），单价必须保持不变 —— 部分更新语义。
	on := true
	if err := UpdateReplenishSettings(GetReplenishConfig(), map[string]SupplierUpdate{
		ReplenishProviderKiross: {Enabled: &on},
	}); err != nil {
		t.Fatalf("UpdateReplenishSettings: %v", err)
	}
	if got := GetReplenishConfig().Supplier(ReplenishProviderKiross).KeyPrice; got != 2 {
		t.Errorf("KeyPrice = %v after an unrelated patch, want 2 preserved", got)
	}
}
