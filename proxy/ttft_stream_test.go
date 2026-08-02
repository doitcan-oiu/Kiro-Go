package proxy

// 端到端验证 TTFT 采集：真实驱动流式 handler，让上游在「响应开始」与「首个内容
// chunk」之间插入一段可测量的延迟，然后断言落到 RequestLog.TTFT 的值确实反映了
// 那段延迟，而不是 0，也不是总耗时。
//
// 单纯测 firstTokenTimer 本身（见 ttft_test.go）不足以说明问题：真正容易出错的
// 是「mark 调用点插在哪」。插到协议头（message_start / response.created）上，
// TTFT 会恒等于 ~0；插到收尾处，TTFT 会等于总耗时。这两种错误都能编译通过、
// 也都能让 ttft_test.go 全绿，只有跑通真实数据流才能区分。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kiro-go/config"
	accountpool "kiro-go/pool"
)

// newDelayedFirstTokenServer 返回一个上游：先返回 200 并 flush（此时下游已经可以
// 发出协议头），静默 delay 之后才写出第一帧 assistant 文本，随后立刻写第二帧。
//
// 这样构造的意义在于把「首字延迟」与「总耗时」明确拉开：首字延迟 ≈ delay，
// 而总耗时 ≈ delay + 第二帧的处理时间。若 mark 插错位置，两者会重合或归零。
func newDelayedFirstTokenServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("upstream ResponseWriter is not a Flusher")
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// 上游「在思考」：连接已建立、响应已开始，但一个 token 都还没吐。
		time.Sleep(delay)

		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "hello ",
		}))
		flusher.Flush()
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "world",
		}))
		flusher.Flush()
	}))
}

// ttftTestConfigDir 返回一个配置目录，且不使用 t.TempDir()。
//
// 这一点是必要的而非偏好：pool.UpdateStats 内部以 `go config.UpdateAccountStats(...)`
// 异步落盘（pool/account.go），该 goroutine 可能在测试函数返回之后才写 config.json。
// t.TempDir() 的自动清理会因此撞上「目录非空」而让已经通过的测试报错——一个与被测
// 行为毫无关系的伪失败。这里改用 os.MkdirTemp 并在 Cleanup 里做尽力删除（忽略错误），
// 把这条竞争彻底移出断言路径。
func ttftTestConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ttft-cfg-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		// 给异步写盘留一点时间，然后尽力删除；删不掉也不影响测试结论。
		time.Sleep(50 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})
	return dir
}

// ttftTestHandler 组装一个可用于流式调用的 Handler，并把上游指向 srv。
func ttftTestHandler(t *testing.T, accountID string, srv *httptest.Server) *Handler {
	t.Helper()

	cfgFile := filepath.Join(ttftTestConfigDir(t), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID:          accountID,
		Enabled:     true,
		AccessToken: "token-" + accountID,
		ProfileArn:  "arn:aws:codewhisperer:profile/" + accountID,
	}); err != nil {
		t.Fatalf("add account: %v", err)
	}
	// 固定走 kiro 端点且关闭回退，确保请求一定打到下面的测试 server 上。
	if err := config.UpdatePreferredEndpoint("kiro"); err != nil {
		t.Fatalf("set preferred endpoint: %v", err)
	}
	if err := config.UpdateEndpointFallback(false); err != nil {
		t.Fatalf("disable endpoint fallback: %v", err)
	}

	oldEndpoints := kiroEndpoints
	kiroEndpoints = []kiroEndpoint{{URL: srv.URL, Origin: "AI_EDITOR", Name: "test"}}
	t.Cleanup(func() { kiroEndpoints = oldEndpoints })
	t.Cleanup(withNoTotalTimeoutStreamClient(t))

	// 日志持久化同样是异步写盘，单测里关掉。
	t.Setenv("KIRO_LOG_PERSIST_DISABLE", "1")

	p := accountpool.GetPool()
	p.Reload()
	return &Handler{
		pool:        p,
		promptCache: newPromptCacheTracker(defaultPromptCacheTTL),
	}
}

// lastSuccessLog 取出最近一条成功日志。
func lastSuccessLog(t *testing.T, h *Handler) RequestLog {
	t.Helper()
	logs := h.getRequestLogs() // newest first
	for _, l := range logs {
		if l.Status == "success" {
			return l
		}
	}
	t.Fatalf("no success log recorded; got %d entries: %+v", len(logs), logs)
	return RequestLog{}
}

// assertTTFTReflectsDelay 是三条路径共用的断言。
//
// 上界用 delay 的一个宽松倍数：CI 机器负载不可控，卡得太死会变成 flaky 测试。
// 真正要防的错误是量级性的（0ms 或等于总耗时），宽松上界足以捕获。
func assertTTFTReflectsDelay(t *testing.T, entry RequestLog, delay time.Duration) {
	t.Helper()
	floor := delay.Milliseconds() / 2 // 允许计时抖动
	if entry.TTFT < floor {
		t.Errorf("TTFT=%dms is below the injected first-token delay (%v): "+
			"the mark() call is probably on a protocol header (message_start / "+
			"response.created) rather than on the first real content chunk",
			entry.TTFT, delay)
	}
	if entry.TTFT > entry.Duration {
		t.Errorf("TTFT=%dms exceeds total duration=%dms, which is impossible",
			entry.TTFT, entry.Duration)
	}
	if entry.Duration <= 0 {
		t.Errorf("duration=%dms should be positive", entry.Duration)
	}
}

func TestClaudeStreamRecordsTTFT(t *testing.T) {
	const delay = 120 * time.Millisecond

	srv := newDelayedFirstTokenServer(t, delay)
	defer srv.Close()

	h := ttftTestHandler(t, "ttft-claude", srv)

	model := "claude-opus-4-8"
	rec := newLockedFlushRecorder()
	h.handleClaudeStream(rec, keepaliveTestPayload(model), model, false,
		claudeThinkingResponseOptions{}, 1000, nil, "")

	entry := lastSuccessLog(t, h)
	assertTTFTReflectsDelay(t, entry, delay)
}

func TestOpenAIStreamRecordsTTFT(t *testing.T) {
	const delay = 120 * time.Millisecond

	srv := newDelayedFirstTokenServer(t, delay)
	defer srv.Close()

	h := ttftTestHandler(t, "ttft-openai", srv)

	model := "claude-opus-4-8"
	rec := newLockedFlushRecorder()
	h.handleOpenAIStream(rec, keepaliveTestPayload(model), model, false, 1000, "")

	entry := lastSuccessLog(t, h)
	assertTTFTReflectsDelay(t, entry, delay)
}

// 非流式路径必须记 0（「未测量」），而不是把总耗时抄进 TTFT。
//
// 这一条是刻意的语义约定：非流式响应整体一次性返回，其「首字延迟」在概念上就等于
// 总耗时。若把它写进 TTFT，前端的首字延迟分布图会混入一批与总耗时完全相同的样本，
// 直接把分布的高分位数带偏。所以后端记 0，前端据此过滤。
func TestNonStreamPathsDoNotRecordTTFT(t *testing.T) {
	const delay = 60 * time.Millisecond

	srv := newDelayedFirstTokenServer(t, delay)
	defer srv.Close()

	h := ttftTestHandler(t, "ttft-nonstream", srv)

	model := "claude-opus-4-8"
	rec := httptest.NewRecorder()
	h.handleClaudeNonStream(rec, keepaliveTestPayload(model), model, false,
		claudeThinkingResponseOptions{}, 1000, nil, "")

	entry := lastSuccessLog(t, h)
	if entry.TTFT != 0 {
		t.Errorf("non-stream path recorded TTFT=%dms, want 0: "+
			"non-stream has no meaningful time-to-first-token and must not "+
			"contribute samples to the latency distribution", entry.TTFT)
	}
	if entry.Duration <= 0 {
		t.Errorf("duration=%dms should still be recorded for non-stream", entry.Duration)
	}
}

// TTFT 必须出现在序列化结果里（前端要读它），且在无数据时被 omitempty 省略。
//
// 这一点直接决定前端能否区分「首字 0ms」与「没有首字数据」：非流式路径写 0，
// omitempty 使其在 JSON 中整个字段消失，前端据此过滤掉这些样本。
func TestTTFTJSONSerialization(t *testing.T) {
	withTTFT, err := json.Marshal(RequestLog{Status: "success", Duration: 900, TTFT: 250})
	if err != nil {
		t.Fatalf("marshal with ttft: %v", err)
	}
	if !strings.Contains(string(withTTFT), `"ttft":250`) {
		t.Errorf("expected ttft in JSON, got %s", withTTFT)
	}

	withoutTTFT, err := json.Marshal(RequestLog{Status: "success", Duration: 900})
	if err != nil {
		t.Fatalf("marshal without ttft: %v", err)
	}
	if strings.Contains(string(withoutTTFT), `"ttft"`) {
		t.Errorf("expected ttft to be omitted when zero, got %s", withoutTTFT)
	}
}
