package pool

// 收入累计与批量落盘的测试。
//
// 这部分逻辑有两个容易写错、且错了很难察觉的点：
//
//  1. weight >= 2 的账号在 p.accounts 里有多份副本（见 Reload）。只更新第一份
//     会让后续轮询取到的副本带着旧的累计值，面板上的收入随轮询位置来回跳动。
//  2. 落盘必须是「攒够一批再写」。最初的实现是每次成功请求都 go 一次
//     config.UpdateAccountRevenue，而那会重写整个 config.json —— 高 QPS 下等于
//     把请求量翻译成同等数量的全量文件写入，同时也让单测的 TempDir 清理与这些
//     后台写入竞争而随机失败。

import (
	"os"
	"path/filepath"
	"testing"

	"kiro-go/config"
)

// newRevenueTestPool 造一个不依赖磁盘的池，直接注入账号副本。
func newRevenueTestPool(accounts []config.Account) *AccountPool {
	return &AccountPool{
		accounts:     accounts,
		revenueDirty: make(map[string]float64),
	}
}

func TestAddRevenueAccumulates(t *testing.T) {
	p := newRevenueTestPool([]config.Account{{ID: "a", Revenue: 1.5}})

	p.AddRevenue("a", 2.0)
	p.AddRevenue("a", 0.25)

	if got := p.accounts[0].Revenue; got != 3.75 {
		t.Errorf("Revenue = %v, want 3.75 (1.5 + 2.0 + 0.25)", got)
	}
}

func TestAddRevenueSyncsWeightedDuplicates(t *testing.T) {
	// weight=3 的账号：Reload 会放三份副本进切片。
	p := newRevenueTestPool([]config.Account{
		{ID: "a", Revenue: 1.0},
		{ID: "a", Revenue: 1.0},
		{ID: "a", Revenue: 1.0},
		{ID: "b", Revenue: 9.0},
	})

	p.AddRevenue("a", 0.5)

	for i := 0; i < 3; i++ {
		if got := p.accounts[i].Revenue; got != 1.5 {
			t.Errorf("copy %d Revenue = %v, want 1.5: every weighted duplicate must "+
				"carry the same total, otherwise the panel value flickers as "+
				"round-robin lands on different copies", i, got)
		}
	}
	if got := p.accounts[3].Revenue; got != 9.0 {
		t.Errorf("unrelated account was modified: Revenue = %v, want 9.0", got)
	}
}

func TestAddRevenueIgnoresNonPositive(t *testing.T) {
	p := newRevenueTestPool([]config.Account{{ID: "a", Revenue: 2.0}})

	p.AddRevenue("a", 0)
	p.AddRevenue("a", -5)

	if got := p.accounts[0].Revenue; got != 2.0 {
		t.Errorf("Revenue = %v, want 2.0 unchanged: zero adds nothing and a "+
			"negative value can only come from a bug upstream", got)
	}
	if len(p.revenueDirty) != 0 {
		t.Errorf("no-op adds must not mark the account dirty; got %d pending", len(p.revenueDirty))
	}
}

func TestAddRevenueUnknownAccountIsNoOp(t *testing.T) {
	p := newRevenueTestPool([]config.Account{{ID: "a"}})

	p.AddRevenue("does-not-exist", 1.0)

	if len(p.revenueDirty) != 0 {
		t.Errorf("an unknown id must not queue a write; got %d pending", len(p.revenueDirty))
	}
}

// 落盘是批量的：N 次累加只产生一条待写记录，而不是 N 条。
func TestAddRevenueBatchesInsteadOfWritingPerRequest(t *testing.T) {
	p := newRevenueTestPool([]config.Account{{ID: "a"}})

	for i := 0; i < 100; i++ {
		p.AddRevenue("a", 0.01)
	}

	if len(p.revenueDirty) != 1 {
		t.Fatalf("pending writes = %d, want 1: 100 次累加必须合并成一次落盘，"+
			"否则每个成功请求都会重写整个 config.json", len(p.revenueDirty))
	}
	if got := p.revenueDirty["a"]; got < 0.999 || got > 1.001 {
		t.Errorf("pending total = %v, want ~1.0 (the latest cumulative value)", got)
	}
}

func TestFlushRevenuePersistsAndClears(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := config.Init(cfgPath); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "a", Enabled: true, RefreshToken: "rt"}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	p := newRevenueTestPool([]config.Account{{ID: "a"}})
	p.AddRevenue("a", 4.25)

	if n := p.FlushRevenue(); n != 1 {
		t.Fatalf("FlushRevenue wrote %d accounts, want 1", n)
	}

	// 落盘后必须清空，否则下一次 flush 会把同一个值反复写入。
	if len(p.revenueDirty) != 0 {
		t.Errorf("dirty set not cleared after flush; got %d pending", len(p.revenueDirty))
	}
	if n := p.FlushRevenue(); n != 0 {
		t.Errorf("second flush wrote %d accounts, want 0 (nothing changed since)", n)
	}

	// 值真的到了磁盘上。
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, a := range config.GetAccounts() {
		if a.ID == "a" {
			if a.Revenue != 4.25 {
				t.Errorf("persisted Revenue = %v, want 4.25; file=%s", a.Revenue, raw)
			}
			return
		}
	}
	t.Fatal("account a missing after flush")
}

func TestFlushRevenueEmptyIsCheap(t *testing.T) {
	p := newRevenueTestPool(nil)
	if n := p.FlushRevenue(); n != 0 {
		t.Errorf("FlushRevenue on an empty pool wrote %d, want 0", n)
	}
}
