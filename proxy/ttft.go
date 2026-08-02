package proxy

// 首字延迟（TTFT, time-to-first-token）采集。
//
// 为什么单独抽一个类型，而不是在三个流式处理函数里各放一个局部变量：
//
//   - 三条流式路径（claude / openai / responses）都有「首个内容 chunk」这一时刻，
//     但各自的发送函数不同（emit / emitRaw / SSE writer）。抽成同一个类型可以让
//     三处的语义严格一致，也能单独测。
//   - 记录必须幂等：一次响应里内容 chunk 有成百上千个，只有第一个才算首字。
//   - 保活 goroutine 与主 goroutine 都会写响应体（见 handler.go 里的 hbMu），
//     因此 mark 可能在不同 goroutine 上被调用，需要并发安全。
//
// 语义约定：
//
//   - 只有「真实内容」才算首字。message_start 这类协议头不算——它在上游还没吐
//     token 时就已经发出，把它计入会让 TTFT 恒等于 0，指标彻底失去意义。
//   - thinking 内容算首字。对用户而言屏幕上出现的第一个字符就是首字，不区分它
//     属于思考段还是正文段。
//   - 非流式路径不记录（保持 0）。那里整个响应一次性返回，首字延迟等于总耗时，
//     混进同一张分布图只会得到一个双峰的、看不出问题的图形。前端据此过滤。
import (
	"sync/atomic"
	"time"
)

// firstTokenTimer 记录一次流式响应中首个内容 chunk 的相对时刻。
//
// 零值不可用，必须经 newFirstTokenTimer 构造（需要起算点）。
type firstTokenTimer struct {
	start time.Time
	// elapsedMs 存首字延迟毫秒数；-1 表示尚未观察到首字。
	// 用 -1 而不是 0 作哨兵：本机直连时首字延迟真的可能是 0ms，
	// 若用 0 当哨兵会把这种情况误判为「没有数据」。
	elapsedMs atomic.Int64
}

// newFirstTokenTimer 以 start 为起算点创建计时器。
// start 应当是请求进入处理函数的时刻，与总耗时同一起点，两者才可比。
func newFirstTokenTimer(start time.Time) *firstTokenTimer {
	t := &firstTokenTimer{start: start}
	t.elapsedMs.Store(-1)
	return t
}

// mark 记录首字时刻。幂等：仅首次调用生效，后续调用是空操作。
//
// 用 CompareAndSwap 而非 sync.Once：热路径上每个 chunk 都会调用它，
// CAS 在已记录后只是一次原子读 + 比较失败，比 Once.Do 的开销更低。
func (t *firstTokenTimer) mark() {
	if t == nil {
		return
	}
	if t.elapsedMs.Load() != -1 {
		return // 快速路径：绝大多数调用走到这里就返回
	}
	ms := time.Since(t.start).Milliseconds()
	if ms < 0 {
		ms = 0 // 时钟回拨保护：宁可记 0 也不要写入负值污染统计
	}
	t.elapsedMs.CompareAndSwap(-1, ms)
}

// ms 返回首字延迟毫秒数；从未观察到首字时返回 0。
//
// 返回 0（而非 -1）是为了直接喂给 RequestLog.TTFT —— 该字段是 omitempty，
// 0 会被省略，前端因此天然地把「无首字数据」与「首字 0ms」区分开：
// 前者字段缺失，后者……也是 0。这一点是刻意的取舍：真实的 0ms 首字延迟只可能
// 出现在本机 mock 上，把它当作无数据不会影响真实部署的统计。
func (t *firstTokenTimer) ms() int64 {
	if t == nil {
		return 0
	}
	if v := t.elapsedMs.Load(); v > 0 {
		return v
	}
	return 0
}
