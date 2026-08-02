package price

import "testing"

// 上游随时会上新模型（claude-opus-5、claude-sonnet-7…），而价格表（litellm 数据）
// 未必同步收录。这里用「一定不存在于价格表」的假模型名验证：新模型必须能按家族
// 回退到同系最新费率，而不是静默算成 0 收入。
func TestFutureModelsFallBackByFamily(t *testing.T) {
	future := []string{
		"claude-opus-5", "claude-opus-5.1", "claude-opus-9.9",
		"claude-sonnet-7", "claude-sonnet-6.3",
		"claude-haiku-5", "claude-haiku-6.1",
		"claude-opus-5.2-thinking",
	}
	for _, m := range future {
		p, ok := Lookup(m)
		if !ok {
			t.Errorf("%s: no pricing resolved — a newly shipped model would be "+
				"counted as zero revenue", m)
			continue
		}
		if p.Input <= 0 || p.Output <= 0 {
			t.Errorf("%s: resolved but rates are zero (in=%g out=%g)", m, p.Input, p.Output)
		}
		t.Logf("%-26s -> in=%g out=%g", m, p.Input, p.Output)
	}
}

// 完全不认识的模型（非 claude 家族）必须明确返回 unknown，而不是硬套一个费率。
func TestTrulyUnknownStaysUnknown(t *testing.T) {
	for _, m := range []string{"llama-4-70b", "gpt-9", "some-random-model"} {
		if _, ok := Lookup(m); ok {
			t.Errorf("%s resolved, but it is not a Claude-family model; "+
				"guessing a rate here would silently fabricate revenue", m)
		}
	}
}
