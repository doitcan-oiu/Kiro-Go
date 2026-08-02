// Package price 提供按 token 计价的模型单价表，用于把请求日志换算成收入。
//
// 数据来源是 LiteLLM 维护的 model_prices_and_context_window.json（本目录内），
// 用 go:embed 打进二进制：运行时不依赖外部文件，也不需要在容器里额外挂载。
//
// 三个关键设计点：
//
//  1. 模型名要做形态转换。日志里记的是 Kiro 的点号形式（claude-sonnet-4.5），
//     价格表用的是短横线形式（claude-sonnet-4-5）。若不转换，查表全部落空，
//     收入会静默恒为 0 —— 这是本包最容易出错的地方，因此单独抽出 resolveKey
//     并做了完整的单测。
//
//  2. 缺价必须可区分于零价。Lookup 返回 (Pricing, bool)，调用方据此决定显示
//     「—」还是 0.00。把未知模型按 0 计入会让收入被低估且毫无提示。
//
//  3. 单价单位是「美元 / 单 token」。价格表原样如此（3e-06 表示 $3/M tokens），
//     这里不做任何缩放，避免引入一层容易记错的换算。
package price

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed model_prices_and_context_window.json
var priceFS embed.FS

const priceFile = "model_prices_and_context_window.json"

// Pricing 是单个模型的四项 token 单价（美元 / 单 token）。
//
// CacheRead / CacheWrite 在价格表里并非所有模型都有：只有支持 prompt cache 的
// 模型才带这两项。缺失时为 0，含义是「该模型不区分缓存计价」，此时缓存 token
// 按普通输入价计费更贴近真实账单（见 Revenue）。
type Pricing struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// rawEntry 只取我们需要的字段。价格表里每条有几十个键，全量反序列化既慢又占内存
// （文件 1.6 MB / 2986 条），因此显式列出四项单价。
type rawEntry struct {
	Mode       string  `json:"mode"`
	Input      float64 `json:"input_cost_per_token"`
	Output     float64 `json:"output_cost_per_token"`
	CacheRead  float64 `json:"cache_read_input_token_cost"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
}

var (
	once  sync.Once
	table map[string]Pricing
)

// kiroDotPattern 匹配 Kiro 的点号版本号形式：claude-opus-4.8 / claude-sonnet-4.5。
// 与 proxy 包里的 claudeDotPattern 同源，但这里刻意复制一份而不是跨包引用：
// price 包被 config/proxy 同时使用，反向依赖 proxy 会形成环。
var kiroDotPattern = regexp.MustCompile(`^(claude-(?:opus|sonnet|haiku))-(\d+)\.(\d{1,2})$`)

// load 解析并缓存价格表。只在首次查询时执行一次。
func load() {
	table = make(map[string]Pricing, 3000)

	data, err := priceFS.ReadFile(priceFile)
	if err != nil {
		// embed 失败意味着构建产物不完整，此时留空表：所有查询返回 not-found，
		// 面板显示「—」而不是错误的收入数字。
		return
	}

	var raw map[string]rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	for name, e := range raw {
		// sample_spec 是文件自带的字段说明，不是真实模型。
		if name == "sample_spec" {
			continue
		}
		// 只保留会产生 token 计费的条目。image_generation / embedding 之类
		// 按张/按次计费，混进来会让「按 token 算收入」得到荒谬的结果。
		if e.Input == 0 && e.Output == 0 {
			continue
		}
		table[strings.ToLower(name)] = Pricing{
			Input:      e.Input,
			Output:     e.Output,
			CacheRead:  e.CacheRead,
			CacheWrite: e.CacheWrite,
		}
	}

	buildFamilyIndex()
}

// resolveKey 把日志里的模型名归一化成价格表的键。
//
// 依次尝试：
//  1. 原名小写直查（覆盖客户端直接用价格表里存在的名字的情况）
//  2. 去掉 -thinking 后缀（本服务用它切换思考模式，不是独立模型）
//  3. 点号 → 短横线（claude-sonnet-4.5 → claude-sonnet-4-5）：主路径
//  4. 加 anthropic. / us.anthropic. 前缀（价格表里 bedrock 系列带前缀）
//  5. 家族回退（claude-sonnet-4 → claude-sonnet-4-5）：价格表没有无小版本号的
//     条目，而 Kiro 确实会返回 claude-sonnet-4。回退到该家族最接近的一档，
//     并通过第二返回值标记为「非精确匹配」，让调用方可以选择提示。
func resolveKey(model string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return "", false
	}
	m = strings.TrimSuffix(m, "-thinking")

	if _, ok := table[m]; ok {
		return m, true
	}

	// 点号 → 短横线
	dashed := m
	if sub := kiroDotPattern.FindStringSubmatch(m); sub != nil {
		dashed = sub[1] + "-" + sub[2] + "-" + sub[3]
		if _, ok := table[dashed]; ok {
			return dashed, true
		}
	}

	// bedrock 前缀变体
	for _, prefix := range []string{"anthropic.", "us.anthropic.", "global.anthropic."} {
		if _, ok := table[prefix+dashed]; ok {
			return prefix + dashed, true
		}
	}

	return "", false
}

// ─────────────────────────────────────────────────────────────────────────────
// 家族回退：新模型上架时不改代码也能计价
//
// 上游（AWS Kiro）会持续上架新模型，而 LiteLLM 的价格表总会滞后一段时间。若查不到
// 就当作「无价」，新模型的收入会静默恒为 0；若写死一张模型清单，每次上新都要改代码
// —— 两者都不可接受。
//
// 因此这里在加载价格表时按「家族 + 版本号」建立索引，查不到精确条目时回退到同家族
// 中版本号最接近且不高于它的那一档；没有更低档时回退到该家族最低档。索引完全从价格
// 表内容推导，不含任何硬编码的模型名。
//
// 例：价格表里有 opus-4-5 / 4-6 / 4-7，则
//   claude-opus-4.8  → opus-4-7（同家族已知的最高档，尚未收录时的最佳近似）
//   claude-opus-5.1  → opus-5 若存在，否则 opus-4-7
//   claude-sonnet-7  → sonnet 家族最高档
//
// 回退结果由 Lookup 的第三返回值标记为非精确，调用方可据此提示「费率为近似值」。

// familyVersion 是一条价格表条目在家族内的版本坐标。
type familyVersion struct {
	major int
	minor int
	key   string // 价格表里的键
}

// familyIndex: 家族名（claude-opus / claude-sonnet / claude-haiku …）→ 版本升序列表。
var familyIndex map[string][]familyVersion

// familyEntryPattern 从价格表的键里提取「家族 + 主版本 + 次版本」。
//
// 覆盖价格表里的两种主要写法：
//
//	claude-opus-4-5              → claude-opus, 4, 5
//	us.anthropic.claude-opus-4-5 → claude-opus, 4, 5（前缀被忽略）
//
// 刻意不匹配带日期的条目（claude-4-sonnet-20250514）：20250514 会被当成版本号，
// 排序后永远排在最前，把所有回退都吸到一个过时条目上。
var familyEntryPattern = regexp.MustCompile(
	`(?:^|\.)(claude-(?:opus|sonnet|haiku))-(\d+)(?:[-.](\d{1,2}))?$`)

// buildFamilyIndex 从已加载的价格表推导家族索引。
func buildFamilyIndex() {
	familyIndex = make(map[string][]familyVersion, 8)

	for key := range table {
		sub := familyEntryPattern.FindStringSubmatch(key)
		if sub == nil {
			continue
		}
		major, err := strconv.Atoi(sub[2])
		if err != nil {
			continue
		}
		minor := 0
		if sub[3] != "" {
			if n, err := strconv.Atoi(sub[3]); err == nil {
				minor = n
			}
		}
		family := sub[1]
		familyIndex[family] = append(familyIndex[family], familyVersion{
			major: major, minor: minor, key: key,
		})
	}

	// 版本升序；同版本时偏好更短的键（无前缀的裸名，通常是 global/默认费率）。
	for family := range familyIndex {
		list := familyIndex[family]
		sort.Slice(list, func(i, j int) bool {
			if list[i].major != list[j].major {
				return list[i].major < list[j].major
			}
			if list[i].minor != list[j].minor {
				return list[i].minor < list[j].minor
			}
			return len(list[i].key) < len(list[j].key)
		})
		familyIndex[family] = list
	}
}

// resolveByFamily 在同家族里找版本号 ≤ 目标的最高一档；都比目标高时取最低档。
//
// 取「不高于」而非「最接近」是刻意的保守选择：新模型通常不便宜，用一个更低档的
// 已知费率计价会低估收入，而低估比高估安全——高估会让人以为在赚钱。
func resolveByFamily(model string) (string, bool) {
	sub := familyLookupPattern.FindStringSubmatch(model)
	if sub == nil {
		return "", false
	}
	family := sub[1]
	list := familyIndex[family]
	if len(list) == 0 {
		return "", false
	}

	major, err := strconv.Atoi(sub[2])
	if err != nil {
		return "", false
	}
	minor := 0
	if sub[3] != "" {
		if n, err := strconv.Atoi(sub[3]); err == nil {
			minor = n
		}
	}

	best := ""
	for _, fv := range list {
		if fv.major < major || (fv.major == major && fv.minor <= minor) {
			best = fv.key // list 已升序，持续覆盖即得「≤ 目标的最高档」
		}
	}
	if best != "" {
		return best, true
	}
	// 目标比该家族所有已知档位都低（例如 claude-opus-3 而表里最低是 4-5）：
	// 用最低档，仍然优于「无价」。
	return list[0].key, true
}

// familyLookupPattern 从查询的模型名里提取家族与版本，接受点号与短横线两种写法。
var familyLookupPattern = regexp.MustCompile(
	`^(claude-(?:opus|sonnet|haiku))-(\d+)(?:[-.](\d{1,2}))?`)

// Lookup 返回模型的 token 单价。
//
// 返回值：
//
//	Pricing — 单价；未解析出任何费率时为零值
//	ok     — 是否得到可用费率（精确或家族回退）
//
// ok 为 false 表示既没有精确条目、也没有同家族可回退的条目，调用方必须显示「—」
// 而不能当作 0：把未知按 0 计入会让收入静默偏低且无从察觉。
func Lookup(model string) (Pricing, bool) {
	p, ok, _ := LookupExact(model)
	return p, ok
}

// LookupExact 与 Lookup 相同，但额外返回费率是否为精确匹配。
//
// exact 为 false 表示价格表里还没有该模型，用的是同家族相邻版本的费率——上游刚上
// 新模型时必然如此。面板可据此加一个「近似」标记，让人知道这个数字会在价格表更新
// 后变化，而不是默默给出一个看起来精确的收入。
func LookupExact(model string) (p Pricing, ok bool, exact bool) {
	once.Do(load)

	if key, found := resolveKey(model); found {
		return table[key], true, true
	}

	// 家族回退：从价格表内容动态推导，新模型无需改代码。
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimSuffix(m, "-thinking")
	if key, found := resolveByFamily(m); found {
		return table[key], true, false
	}

	return Pricing{}, false, false
}

// Usage 是一条请求日志里参与计价的四项 token 数。
type Usage struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
}

// Revenue 按模型单价把 token 用量换算成金额（美元）。
//
// 缓存计价的回退规则：模型没有 CacheRead/CacheWrite 单价时，缓存 token 按普通
// 输入价计。这比按 0 计更贴近真实账单——缓存读命中在上游依然是要收费的输入，
// 只是折扣不同；按 0 计会让高缓存命中率的账号显示出虚高的利润。
//
// 第二返回值为 false 表示模型未知，此时金额为 0，调用方应显示「—」。
func Revenue(model string, u Usage) (float64, bool) {
	p, ok := Lookup(model)
	if !ok {
		return 0, false
	}

	readRate := p.CacheRead
	if readRate == 0 {
		readRate = p.Input
	}
	writeRate := p.CacheWrite
	if writeRate == 0 {
		writeRate = p.Input
	}

	// 负数只可能来自上游异常或日志损坏，夹到 0 避免把收入算成负值。
	nz := func(n int) float64 {
		if n < 0 {
			return 0
		}
		return float64(n)
	}

	return nz(u.Input)*p.Input +
		nz(u.Output)*p.Output +
		nz(u.CacheRead)*readRate +
		nz(u.CacheCreation)*writeRate, true
}

// Size 返回价格表条目数，用于启动日志与自检。
func Size() int {
	once.Do(load)
	return len(table)
}
