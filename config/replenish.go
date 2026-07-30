// Package config — 在线补号（自动补充账号池）相关配置。
//
// 补号功能通过对接上游“供应商 API”在线购买 Kiro API Key，并把它们作为 api_key
// 账号导入本地账号池。目前对接两家，**同时并行**而非二选一：
//   - ReplenishProviderKiross ("kiross")：X-API-Key 认证，POST /api/my/purchase，
//     PUT /api/my/webhook 可自动注册回调。
//   - ReplenishProviderKiroappio ("kiroappio")：Bearer km_ 令牌，/api/me/* 前台
//     接口，回调地址只能在其站点后台手填。
//
// 为什么并行：单家供应商的 Key 存活时长不可控，只靠一家会出现「那家断供就整体没号」
// 的窗口。两家各自独立开关、独立密钥、独立的每次推送购买数量，任一家推送
// new_keys_available 就只向推送方下单（见 SupplierConfig.WebhookCount），彼此
// 互不影响：一家挂了另一家照常补号。
//
// 每家有独立的入站回调路径 /replenish/webhook/<provider>/<secret>，因此能分辨
// 推送来自谁，也能单独吊销某一家的密钥而不影响另一家。
//
// 补号策略分两种轮询触发方式（两家一起补，各买各的 BatchCount）：
//   - 低水位（MinPoolSize）：可用账号数低于阈值时补号。
//   - 全部凭证禁用（AllDeadReplenish / AllDeadCount）：系统内所有凭证都被禁用/封禁
//     时拉一批 Key 导入，用于「号全死了自动救活」场景。
//
// 运行态字段（LastRunAt / LastError / LastResult）随每次补号更新并持久化，
// 供管理面板展示补号历史。
package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

// 供应商标识。持久化到配置文件，请勿随意更名。
const (
	// ReplenishProviderKiross 是 X-API-Key 认证的那家（取号文档 kiro.ss.txt）：
	// POST /api/my/purchase 带 client_order_id 幂等，PUT /api/my/webhook 注册回调。
	ReplenishProviderKiross = "kiross"
	// ReplenishProviderKiroappio 是 kiroapp.io：Bearer km_ 令牌，/api/me/* 前台接口，
	// purchase 必填 client_order_id（幂等），支持 webhook 但无注册接口。
	ReplenishProviderKiroappio = "kiroappio"
)

// DefaultKiroappioBaseURL 是 kiroapp.io 的默认 API 基地址；用户只需填令牌。
const DefaultKiroappioBaseURL = "http://kiroapp.io"

// ReplenishProviders 是全部已对接的供应商标识，顺序即面板展示与补号遍历顺序。
func ReplenishProviders() []string {
	return []string{ReplenishProviderKiross, ReplenishProviderKiroappio}
}

// NormalizeReplenishProvider 把任意输入归一化为已知的供应商标识。
// 未知值返回空串，调用方据此拒绝，避免把脏输入静默当成某一家。
func NormalizeReplenishProvider(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case ReplenishProviderKiross, "kiro.ss", "default", "vendor":
		// "default"/"vendor" 是这家在旧配置里的历史标识，保留以兼容升级。
		return ReplenishProviderKiross
	case ReplenishProviderKiroappio, "kiroapp.io":
		return ReplenishProviderKiroappio
	default:
		return ""
	}
}

// SupplierConfig 是单家供应商的独立配置。两家各持一份，互不干扰。
type SupplierConfig struct {
	// Enabled 控制这家是否参与补号（轮询与推送都受它约束）。
	// 关掉一家不会影响另一家，也不会丢失它的密钥。
	Enabled bool `json:"enabled,omitempty"`

	// BaseURL 留空时回落到该供应商的官方默认地址（若有）。
	BaseURL string `json:"baseUrl,omitempty"`
	// ApiKey 是该家的凭证：kiross 用于 X-API-Key，kiroappio 用于 Bearer。
	ApiKey string `json:"apiKey,omitempty"`

	// WebhookCount 是收到该家 new_keys_available 推送时下单的数量。
	// 这是「按供应商分别设置买几个」的落点：两家可以配不同数量。
	// <=0 表示回退到全局 BatchCount；实际下单量还会被推送里的 new_keys 夹取，
	// 因为 new_keys 是该批次可提取上限，超量请求必然失败。
	WebhookCount int `json:"webhookCount,omitempty"`

	// WebhookSecret 是该家专属回调路径 /replenish/webhook/<provider>/<secret>
	// 内嵌的随机密钥。每家独立，便于单独轮换而不影响另一家。
	WebhookSecret string `json:"webhookSecret,omitempty"`

	// 运行态：该家最近一次收到推送的时间与摘要。
	LastWebhookAt  int64  `json:"lastWebhookAt,omitempty"`
	LastWebhookMsg string `json:"lastWebhookMsg,omitempty"`
}

// ReplenishConfig 保存在线补号的全部配置与最近一次运行的状态。
type ReplenishConfig struct {
	// Suppliers 按供应商标识存放各家的独立配置（键为 ReplenishProvider* 常量）。
	Suppliers map[string]SupplierConfig `json:"suppliers,omitempty"`

	// 导入 Kiro Key 时使用的区域 hint（留空自动探测），各供应商共用。
	Region string `json:"region,omitempty"`

	// 自动补号策略（轮询触发时两家共用；每家实际买 BatchCount 个）
	Enabled         bool `json:"enabled,omitempty"`         // 是否启用后台自动补号
	MinPoolSize     int  `json:"minPoolSize,omitempty"`     // 低水位阈值：可用账号数 < 该值时触发补号
	BatchCount      int  `json:"batchCount,omitempty"`      // 每家每次补号购买的 Key 数量
	IntervalSeconds int  `json:"intervalSeconds,omitempty"` // 自动检查间隔（秒），最小 60

	// 全部凭证禁用时补号（轮询触发）。开启后每轮检查若系统内所有凭证都已被
	// 禁用/封禁（且没有等待自动恢复的临时隔离账号），就提取 AllDeadCount 个 Key
	// 导入。AllDeadCount <= 0 时回退到 BatchCount。
	AllDeadReplenish bool `json:"allDeadReplenish,omitempty"`
	AllDeadCount     int  `json:"allDeadCount,omitempty"`

	// PublicBaseURL 是本服务的公网基地址（如 https://my-proxy.example.com），
	// 用于拼接注册给各供应商的回调地址。两家共用同一个公网地址，只是路径不同。
	PublicBaseURL string `json:"publicBaseUrl,omitempty"`

	// 运行态（随每次补号更新并持久化）
	LastRunAt  int64  `json:"lastRunAt,omitempty"`  // 上次补号执行时间（Unix 秒）
	LastError  string `json:"lastError,omitempty"`  // 上次补号错误（全部成功时清空）
	LastResult string `json:"lastResult,omitempty"` // 上次补号结果摘要

	// ---- 以下为旧版单选结构的遗留字段，仅用于一次性迁移，勿在新代码中读写 ----
	// 旧配置是 Provider 二选一 + 每家一组平铺字段。migrateLegacy 会把它们搬进
	// Suppliers 后清空，使升级用户不必重填密钥。
	LegacyProvider         string `json:"provider,omitempty"`
	LegacyBaseURL          string `json:"baseUrl,omitempty"`
	LegacyApiKey           string `json:"apiKey,omitempty"`
	LegacyKiroappioBaseURL string `json:"kiroappioBaseUrl,omitempty"`
	LegacyKiroappioApiKey  string `json:"kiroappioApiKey,omitempty"`
	LegacyWebhookMaxCount  int    `json:"webhookMaxCount,omitempty"`
	LegacyWebhookSecret    string `json:"webhookSecret,omitempty"`
}

// Supplier 返回指定供应商的配置副本；未配置过时返回零值。
func (rc ReplenishConfig) Supplier(provider string) SupplierConfig {
	if rc.Suppliers == nil {
		return SupplierConfig{}
	}
	return rc.Suppliers[NormalizeReplenishProvider(provider)]
}

// EnabledProviders 返回已启用且填了密钥的供应商标识，顺序稳定。
// 补号编排据此遍历：只有真正可用的才会被下单。
func (rc ReplenishConfig) EnabledProviders() []string {
	var out []string
	for _, p := range ReplenishProviders() {
		sc := rc.Supplier(p)
		if sc.Enabled && strings.TrimSpace(sc.ApiKey) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SupplierBaseURL 返回该供应商生效的 API 基地址（已去除末尾斜杠）。
// 留空时回落到官方默认地址；kiross 无默认地址，必须由用户填写。
func (rc ReplenishConfig) SupplierBaseURL(provider string) string {
	p := NormalizeReplenishProvider(provider)
	if b := strings.TrimRight(strings.TrimSpace(rc.Supplier(p).BaseURL), "/"); b != "" {
		return b
	}
	if p == ReplenishProviderKiroappio {
		return DefaultKiroappioBaseURL
	}
	return ""
}

// DefaultSupplierBaseURL 返回该供应商的官方默认地址，供面板作输入框提示。
// 空串表示该家没有默认地址，必须由用户填写。
func DefaultSupplierBaseURL(provider string) string {
	if NormalizeReplenishProvider(provider) == ReplenishProviderKiroappio {
		return DefaultKiroappioBaseURL
	}
	return ""
}

// SupportsWebhookAutoRegister 报告该供应商能否通过 API 自动注册回调地址。
// kiroapp.io 只能在其站点「设置 → Webhook 配置」里手填，故返回 false。
func SupportsWebhookAutoRegister(provider string) bool {
	return NormalizeReplenishProvider(provider) == ReplenishProviderKiross
}

// EffectiveWebhookCount 返回收到该家推送时应下单的数量。
// 该家未单独设置（<=0）时回退到全局 BatchCount。
func (rc ReplenishConfig) EffectiveWebhookCount(provider string) int {
	if n := rc.Supplier(provider).WebhookCount; n > 0 {
		return n
	}
	return rc.BatchCount
}

// EffectiveAllDeadCount 返回「全部凭证禁用」触发时每家应提取的 Key 数量。
// AllDeadCount <= 0 时回退到 BatchCount。
func (rc ReplenishConfig) EffectiveAllDeadCount() int {
	if rc.AllDeadCount > 0 {
		return rc.AllDeadCount
	}
	return rc.BatchCount
}

// GetReplenishConfig 返回补号配置的副本。未初始化时返回零值。
//
// Suppliers 是 map，直接返回会让调用方持有内部引用；这里深拷一层，
// 避免外部改动绕过 cfgLock 影响全局配置。
func GetReplenishConfig() ReplenishConfig {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return ReplenishConfig{}
	}
	return cfg.Replenish.clone()
}

// clone 深拷 Suppliers，其余字段均为值类型。
func (rc ReplenishConfig) clone() ReplenishConfig {
	out := rc
	if rc.Suppliers != nil {
		out.Suppliers = make(map[string]SupplierConfig, len(rc.Suppliers))
		for k, v := range rc.Suppliers {
			out.Suppliers[k] = v
		}
	}
	return out
}

// MigrateReplenishLegacy 把旧版单选结构的字段搬进 Suppliers，并清空遗留字段。
// 幂等：已迁移过（遗留字段为空）时什么都不做。由 config 加载流程调用。
//
// 迁移策略：旧配置里被选中的那家继承 Enabled，另一家即便有密钥也保持关闭——
// 用户没同意过同时向两家下单，静默开启会带来预期外的扣费。
func MigrateReplenishLegacy() error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	if !cfg.Replenish.migrateLegacy() {
		return nil
	}
	return Save()
}

// migrateLegacy 执行迁移，返回是否有改动。调用方需持有 cfgLock。
func (rc *ReplenishConfig) migrateLegacy() bool {
	hasLegacy := rc.LegacyProvider != "" || rc.LegacyApiKey != "" ||
		rc.LegacyKiroappioApiKey != "" || rc.LegacyWebhookSecret != "" ||
		rc.LegacyBaseURL != "" || rc.LegacyKiroappioBaseURL != ""
	if !hasLegacy {
		return false
	}
	if rc.Suppliers == nil {
		rc.Suppliers = map[string]SupplierConfig{}
	}
	// 旧的 "default"/"vendor" 都归一到 kiross。
	selected := NormalizeReplenishProvider(rc.LegacyProvider)

	if rc.LegacyApiKey != "" || rc.LegacyBaseURL != "" {
		sc := rc.Suppliers[ReplenishProviderKiross]
		if sc.ApiKey == "" {
			sc.ApiKey = rc.LegacyApiKey
		}
		if sc.BaseURL == "" {
			sc.BaseURL = strings.TrimRight(strings.TrimSpace(rc.LegacyBaseURL), "/")
		}
		// 旧的单一 webhookSecret 归给原先选中的那家，另一家重新生成，
		// 否则两家会共用同一密钥，失去按家吊销的意义。
		if sc.WebhookSecret == "" && selected == ReplenishProviderKiross {
			sc.WebhookSecret = rc.LegacyWebhookSecret
		}
		if sc.WebhookCount == 0 {
			sc.WebhookCount = rc.LegacyWebhookMaxCount
		}
		sc.Enabled = sc.Enabled || selected == ReplenishProviderKiross
		rc.Suppliers[ReplenishProviderKiross] = sc
	}

	if rc.LegacyKiroappioApiKey != "" || rc.LegacyKiroappioBaseURL != "" {
		sc := rc.Suppliers[ReplenishProviderKiroappio]
		if sc.ApiKey == "" {
			sc.ApiKey = rc.LegacyKiroappioApiKey
		}
		if sc.BaseURL == "" {
			sc.BaseURL = strings.TrimRight(strings.TrimSpace(rc.LegacyKiroappioBaseURL), "/")
		}
		if sc.WebhookSecret == "" && selected == ReplenishProviderKiroappio {
			sc.WebhookSecret = rc.LegacyWebhookSecret
		}
		if sc.WebhookCount == 0 {
			sc.WebhookCount = rc.LegacyWebhookMaxCount
		}
		sc.Enabled = sc.Enabled || selected == ReplenishProviderKiroappio
		rc.Suppliers[ReplenishProviderKiroappio] = sc
	}

	rc.LegacyProvider = ""
	rc.LegacyBaseURL = ""
	rc.LegacyApiKey = ""
	rc.LegacyKiroappioBaseURL = ""
	rc.LegacyKiroappioApiKey = ""
	rc.LegacyWebhookMaxCount = 0
	rc.LegacyWebhookSecret = ""
	return true
}

// SupplierUpdate 是面板提交的单家供应商变更。指针字段为 nil 表示该项不变。
// ApiKey 为空字符串同样表示保持原值——面板不回显明文，空输入不应清空密钥。
type SupplierUpdate struct {
	Enabled      *bool
	BaseURL      *string
	ApiKey       *string
	WebhookCount *int
}

// UpdateReplenishSettings 更新补号的策略字段与各家供应商配置（不含运行态），
// 并持久化。intervalSeconds < 60 时会被抬到 60，避免过于频繁地打上游。
//
// suppliers 的键必须是已知供应商标识，未知键返回错误而非静默忽略，
// 否则前端字段名写错会表现为「保存成功但配置没生效」。
func UpdateReplenishSettings(rc ReplenishConfig, suppliers map[string]SupplierUpdate) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	for p := range suppliers {
		if NormalizeReplenishProvider(p) == "" {
			return errors.New("unknown replenish provider: " + p)
		}
	}

	if rc.IntervalSeconds > 0 && rc.IntervalSeconds < 60 {
		rc.IntervalSeconds = 60
	}
	if rc.MinPoolSize < 0 {
		rc.MinPoolSize = 0
	}
	if rc.BatchCount < 0 {
		rc.BatchCount = 0
	}
	if rc.AllDeadCount < 0 {
		rc.AllDeadCount = 0
	}

	// 仅覆盖用户可配置项，保留运行态字段。
	cfg.Replenish.Region = rc.Region
	cfg.Replenish.Enabled = rc.Enabled
	cfg.Replenish.MinPoolSize = rc.MinPoolSize
	cfg.Replenish.BatchCount = rc.BatchCount
	cfg.Replenish.IntervalSeconds = rc.IntervalSeconds
	cfg.Replenish.AllDeadReplenish = rc.AllDeadReplenish
	cfg.Replenish.AllDeadCount = rc.AllDeadCount
	cfg.Replenish.PublicBaseURL = strings.TrimRight(strings.TrimSpace(rc.PublicBaseURL), "/")

	if len(suppliers) > 0 && cfg.Replenish.Suppliers == nil {
		cfg.Replenish.Suppliers = map[string]SupplierConfig{}
	}
	for p, up := range suppliers {
		key := NormalizeReplenishProvider(p)
		sc := cfg.Replenish.Suppliers[key]
		if up.Enabled != nil {
			sc.Enabled = *up.Enabled
		}
		if up.BaseURL != nil {
			sc.BaseURL = strings.TrimRight(strings.TrimSpace(*up.BaseURL), "/")
		}
		// 空字符串 = 保持原密钥不变（面板不回显明文）。
		if up.ApiKey != nil && *up.ApiKey != "" {
			sc.ApiKey = strings.TrimSpace(*up.ApiKey)
		}
		if up.WebhookCount != nil {
			n := *up.WebhookCount
			if n < 0 {
				n = 0
			}
			sc.WebhookCount = n
		}
		cfg.Replenish.Suppliers[key] = sc
	}
	return Save()
}

// GetOrCreateSupplierWebhookSecret 返回该供应商专属回调路径的密钥；
// 尚未生成则创建并持久化。密钥为 32 字节随机十六进制串，用于
// /replenish/webhook/<provider>/<secret> 的路径鉴权。
func GetOrCreateSupplierWebhookSecret(provider string) (string, error) {
	key := NormalizeReplenishProvider(provider)
	if key == "" {
		return "", errors.New("unknown replenish provider: " + provider)
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return "", errors.New("config not initialized")
	}
	if cfg.Replenish.Suppliers == nil {
		cfg.Replenish.Suppliers = map[string]SupplierConfig{}
	}
	sc := cfg.Replenish.Suppliers[key]
	if sc.WebhookSecret != "" {
		return sc.WebhookSecret, nil
	}
	secret, err := randomHex32()
	if err != nil {
		return "", err
	}
	sc.WebhookSecret = secret
	cfg.Replenish.Suppliers[key] = sc
	if err := Save(); err != nil {
		return "", err
	}
	return secret, nil
}

// ResetSupplierWebhookSecret 重新生成该供应商的回调密钥，旧地址立即失效。
// 只影响这一家，另一家的回调地址照常工作。
func ResetSupplierWebhookSecret(provider string) (string, error) {
	key := NormalizeReplenishProvider(provider)
	if key == "" {
		return "", errors.New("unknown replenish provider: " + provider)
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return "", errors.New("config not initialized")
	}
	if cfg.Replenish.Suppliers == nil {
		cfg.Replenish.Suppliers = map[string]SupplierConfig{}
	}
	secret, err := randomHex32()
	if err != nil {
		return "", err
	}
	sc := cfg.Replenish.Suppliers[key]
	sc.WebhookSecret = secret
	cfg.Replenish.Suppliers[key] = sc
	if err := Save(); err != nil {
		return "", err
	}
	return secret, nil
}

// FindProviderByWebhookSecret 用入站回调路径里的密钥反查供应商。
// 返回空串表示没有匹配，调用方应据此拒绝请求。
//
// 用常数时间比对，避免通过响应时间逐字节猜测密钥。
func FindProviderByWebhookSecret(secret string) string {
	if secret == "" {
		return ""
	}
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return ""
	}
	// 逐个常数时间比对：不因匹配到第一家就提前返回时序差异。
	match := ""
	for _, p := range ReplenishProviders() {
		want := cfg.Replenish.Suppliers[p].WebhookSecret
		if want == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(secret), []byte(want)) == 1 {
			match = p
		}
	}
	return match
}

// randomHex32 生成 32 字节随机的十六进制串。
func randomHex32() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// RecordReplenishRun 持久化一次补号运行的结果（运行态字段）。
// errMsg 为空表示成功。
func RecordReplenishRun(at int64, result, errMsg string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	cfg.Replenish.LastRunAt = at
	cfg.Replenish.LastResult = result
	cfg.Replenish.LastError = errMsg
	return Save()
}

// RecordSupplierWebhook 持久化某家供应商最近一次推送的时间与摘要。
// 按家记录，便于在面板上分别看到两家的推送活跃度。
func RecordSupplierWebhook(provider string, at int64, msg string) error {
	key := NormalizeReplenishProvider(provider)
	if key == "" {
		return errors.New("unknown replenish provider: " + provider)
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	if cfg.Replenish.Suppliers == nil {
		cfg.Replenish.Suppliers = map[string]SupplierConfig{}
	}
	sc := cfg.Replenish.Suppliers[key]
	sc.LastWebhookAt = at
	sc.LastWebhookMsg = msg
	cfg.Replenish.Suppliers[key] = sc
	return Save()
}
