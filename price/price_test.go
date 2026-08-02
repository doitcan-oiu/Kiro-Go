package price

// 价格查表与收入换算的测试。
//
// 重点覆盖模型名归一化：日志记的是 Kiro 点号形式（claude-sonnet-4.5），价格表
// 用短横线形式（claude-sonnet-4-5）。这个转换一旦失效，Lookup 全部落空、收入
// 恒为 0，而界面上只会看到「利润 = -成本」——看起来像是没有流量，而不是像 bug。
// 因此这里逐个钉住 Kiro 实际会返回的每个模型名。

import (
	"math"
	"testing"
)

// kiroModels 是 proxy/translator.go 里 effortCapableModels 与模型映射表中
// 出现的、Kiro 实际会返回的模型名。每一个都必须能查到价格。
var kiroModels = []string{
	"claude-sonnet-4",
	"claude-sonnet-4.5",
	"claude-sonnet-4.6",
	"claude-sonnet-5",
	"claude-opus-4.5",
	"claude-opus-4.6",
	"claude-opus-4.7",
	"claude-opus-4.8",
	"claude-haiku-4.5",
}

func TestPriceTableLoads(t *testing.T) {
	if n := Size(); n < 1000 {
		t.Fatalf("price table has %d entries, want >=1000: the embedded json "+
			"is probably missing or failed to parse", n)
	}
}

func TestEveryKiroModelHasPricing(t *testing.T) {
	for _, m := range kiroModels {
		t.Run(m, func(t *testing.T) {
			p, ok := Lookup(m)
			if !ok {
				t.Fatalf("no pricing for %q: the dot->dash conversion or a family "+
					"fallback is missing, and revenue for this model would be 0", m)
			}
			if p.Input <= 0 || p.Output <= 0 {
				t.Errorf("pricing for %q has non-positive rates: in=%g out=%g",
					m, p.Input, p.Output)
			}
			// 输出价必须高于输入价：所有 Claude 模型都是这个关系，若反了说明
			// 把两个字段读错了位置。
			if p.Output <= p.Input {
				t.Errorf("pricing for %q: output %g should exceed input %g",
					m, p.Output, p.Input)
			}
		})
	}
}

func TestThinkingSuffixResolvesToBaseModel(t *testing.T) {
	// -thinking 是本服务用来切换思考模式的后缀，不是独立模型；两者必须同价。
	base, ok1 := Lookup("claude-sonnet-4.5")
	think, ok2 := Lookup("claude-sonnet-4.5-thinking")
	if !ok1 || !ok2 {
		t.Fatal("both the base and -thinking form must resolve")
	}
	if base != think {
		t.Errorf("-thinking pricing %+v differs from base %+v", think, base)
	}
}

func TestUnknownModelIsDistinguishableFromFree(t *testing.T) {
	// 关键语义：未知模型返回 ok=false，而不是「单价 0」。把未知按 0 计会让收入
	// 静默偏低，界面上完全看不出漏了模型。
	if _, ok := Lookup("totally-made-up-model-xyz"); ok {
		t.Error("an unknown model must report ok=false")
	}
	rev, ok := Revenue("totally-made-up-model-xyz", Usage{Input: 1000, Output: 1000})
	if ok {
		t.Error("Revenue must report ok=false for an unknown model")
	}
	if rev != 0 {
		t.Errorf("revenue for an unknown model = %g, want 0", rev)
	}
}

func TestEmptyModelIsUnknown(t *testing.T) {
	if _, ok := Lookup(""); ok {
		t.Error("empty model name must not resolve")
	}
}

func TestCaseAndWhitespaceInsensitive(t *testing.T) {
	want, _ := Lookup("claude-sonnet-4.5")
	for _, variant := range []string{"Claude-Sonnet-4.5", "  claude-sonnet-4.5  ", "CLAUDE-SONNET-4.5"} {
		got, ok := Lookup(variant)
		if !ok {
			t.Errorf("%q did not resolve", variant)
			continue
		}
		if got != want {
			t.Errorf("%q gave %+v, want %+v", variant, got, want)
		}
	}
}

func TestRevenueUsesEachTokenClass(t *testing.T) {
	p, ok := Lookup("claude-sonnet-4.5")
	if !ok {
		t.Fatal("fixture model must resolve")
	}

	u := Usage{Input: 1000, Output: 2000, CacheRead: 3000, CacheCreation: 4000}
	got, ok := Revenue("claude-sonnet-4.5", u)
	if !ok {
		t.Fatal("Revenue should succeed for a known model")
	}

	want := 1000*p.Input + 2000*p.Output + 3000*p.CacheRead + 4000*p.CacheWrite
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("revenue = %g, want %g", got, want)
	}
	// 四项都必须真的参与：若某项被漏掉，结果会等于「少算那一项」的值。
	if math.Abs(got-want) < 1e-12 && want == 0 {
		t.Error("fixture produced zero revenue; the assertion is vacuous")
	}
}

func TestRevenueFallsBackToInputRateWhenCacheRatesAbsent(t *testing.T) {
	// 造一个只有 input/output 的临时条目，验证缓存 token 按输入价计。
	once.Do(load)
	const key = "test-model-without-cache-rates"
	table[key] = Pricing{Input: 2e-06, Output: 8e-06}
	t.Cleanup(func() { delete(table, key) })

	got, ok := Revenue(key, Usage{CacheRead: 1000, CacheCreation: 1000})
	if !ok {
		t.Fatal("Revenue should succeed")
	}
	// 按输入价：2000 * 2e-06。按 0 计会得到 0，那会让高缓存命中的账号利润虚高。
	want := 2000 * 2e-06
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("cache tokens billed at %g, want %g (input rate fallback)", got, want)
	}
}

func TestRevenueClampsNegativeTokenCounts(t *testing.T) {
	// 负 token 只可能来自上游异常或日志损坏；必须夹到 0 而不是产生负收入。
	got, ok := Revenue("claude-sonnet-4.5", Usage{Input: -5000, Output: 1000})
	if !ok {
		t.Fatal("Revenue should succeed")
	}
	p, _ := Lookup("claude-sonnet-4.5")
	want := 1000 * p.Output
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("revenue = %g, want %g (negative input clamped to 0)", got, want)
	}
}

func TestRevenueZeroUsageIsZeroNotUnknown(t *testing.T) {
	// 已知模型 + 零用量 = 0 收入且 ok=true。这与「未知模型」必须可区分。
	rev, ok := Revenue("claude-sonnet-4.5", Usage{})
	if !ok {
		t.Error("a known model with zero usage must still report ok=true")
	}
	if rev != 0 {
		t.Errorf("revenue = %g, want 0", rev)
	}
}

func TestNonTokenModelsAreExcluded(t *testing.T) {
	once.Do(load)
	// 图像/嵌入类模型按张或按次计费，不该出现在按 token 计价的表里：混进来会让
	// 收入换算得到无意义的数字。
	for _, m := range []string{
		"1024-x-1024/dall-e-2",
		"amazon.titan-image-generator-v1",
	} {
		if _, ok := table[m]; ok {
			t.Errorf("%q should be excluded: it is not token-billed", m)
		}
	}
}

func TestSonnet4FallbackMatchesSonnet45Rates(t *testing.T) {
	// claude-sonnet-4 在价格表里没有直接条目，走 familyFallbacks。回退目标的
	// 单价应与 sonnet-4-5 一致；若某天价格表变了，这个断言会提醒重新确认。
	got, ok := Lookup("claude-sonnet-4")
	if !ok {
		t.Fatal("claude-sonnet-4 must resolve via the family fallback")
	}
	ref, _ := Lookup("claude-sonnet-4.5")
	if got.Input != ref.Input || got.Output != ref.Output {
		t.Logf("claude-sonnet-4 rates (in=%g out=%g) differ from sonnet-4.5 "+
			"(in=%g out=%g); verify the fallback target is still right",
			got.Input, got.Output, ref.Input, ref.Output)
	}
}
