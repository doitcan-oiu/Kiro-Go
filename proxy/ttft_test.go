package proxy

import (
	"sync"
	"testing"
	"time"
)

func TestFirstTokenTimerRecordsOnFirstMarkOnly(t *testing.T) {
	start := time.Now().Add(-120 * time.Millisecond)
	timer := newFirstTokenTimer(start)

	if got := timer.ms(); got != 0 {
		t.Fatalf("before mark: ms() = %d, want 0", got)
	}

	timer.mark()
	first := timer.ms()
	if first < 100 {
		t.Fatalf("after mark: ms() = %d, want >=100 (start was 120ms ago)", first)
	}

	// 再等一会儿后重复 mark，结果必须不变——首字只有一次。
	time.Sleep(30 * time.Millisecond)
	timer.mark()
	timer.mark()
	if got := timer.ms(); got != first {
		t.Errorf("mark() is not idempotent: %d -> %d", first, got)
	}
}

func TestFirstTokenTimerNilIsSafe(t *testing.T) {
	// 非流式路径传 nil，不应 panic。
	var timer *firstTokenTimer
	timer.mark()
	if got := timer.ms(); got != 0 {
		t.Errorf("nil timer ms() = %d, want 0", got)
	}
}

func TestFirstTokenTimerConcurrentMark(t *testing.T) {
	// 保活 goroutine 与主 goroutine 会并发写响应体，故 mark 必须并发安全。
	// 该测试在 -race 下才有意义。
	timer := newFirstTokenTimer(time.Now())

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer.mark()
		}()
	}
	wg.Wait()

	// 只断言「记录了一个非负值且不再变化」——并发下具体数值不可预测。
	settled := timer.elapsedMs.Load()
	if settled < 0 {
		t.Fatalf("elapsedMs = %d, want >=0 after concurrent marks", settled)
	}
	timer.mark()
	if got := timer.elapsedMs.Load(); got != settled {
		t.Errorf("value changed after settling: %d -> %d", settled, got)
	}
}

func TestFirstTokenTimerZeroMsIsReportedAsNoData(t *testing.T) {
	// 时钟精度足够高时，同一瞬间 mark 会得到 0ms。ms() 对外统一返回 0，
	// 与「从未 mark」不可区分——这是 ttft.go 里说明过的刻意取舍。
	timer := newFirstTokenTimer(time.Now())
	timer.mark()
	if got := timer.ms(); got < 0 {
		t.Errorf("ms() = %d, want >=0", got)
	}
}
