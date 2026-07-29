// Package config — 在线补号（自动补充账号池）相关配置。
//
// 补号功能通过对接上游“供应商 API”在线购买 Kiro API Key (ksk_...)，并把它们作为
// api_key 账号导入本地账号池。目前支持两个供应商，由 Provider 选择：
//   - ReplenishProviderVendor ("default")：X-API-Key 认证，支持 webhook 推送与
//     client_order_id 幂等（BaseURL / ApiKey）。
//   - ReplenishProviderKiroapp ("kiroapp")：kiroapp.cc，Bearer 认证，无 webhook
//     （KiroappBaseURL / KiroappApiKey）。
//
// 两个供应商的连接信息各自独立保存，切换 Provider 不会丢失另一个的密钥。
//
// 补号策略分两种触发方式：
//   - 低水位（MinPoolSize）：可用账号数低于阈值时补号。
//   - 全部凭证禁用（AllDeadReplenish / AllDeadCount）：系统内所有凭证都被禁用/封禁
//     时拉一批 Key 导入，用于「号全死了自动救活」场景。
//
// 运行态字段（LastRunAt / LastError / LastResult）随每次补号更新并持久化，
// 供管理面板展示补号历史。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

// 供应商标识。持久化到配置文件，请勿随意更名。
const (
	// ReplenishProviderVendor 是最初对接的供应商（对接文档.md）：X-API-Key 认证，
	// 支持 /api/my/purchase 幂等与 webhook 推送。空值按该供应商处理（向后兼容）。
	ReplenishProviderVendor = "default"
	// ReplenishProviderKiroapp 是 kiroapp.cc：Bearer 认证，/openapi/claim 提取，
	// 无 webhook、无幂等订单号，依赖轮询触发。
	ReplenishProviderKiroapp = "kiroapp"
)

// DefaultKiroappBaseURL 是 kiroapp.cc 的默认 API 基地址；用户只需填密钥。
const DefaultKiroappBaseURL = "https://kiroapp.cc"

// ReplenishConfig 保存在线补号的全部配置与最近一次运行的状态。
type ReplenishConfig struct {
	// Provider 选择使用哪个供应商，取值见 ReplenishProvider* 常量。
	// 空值等价于 ReplenishProviderVendor（兼容旧配置）。
	Provider string `json:"provider,omitempty"`

	// 供应商连接信息（Provider = "default"）
	BaseURL string `json:"baseUrl,omitempty"` // 供应商 API 基地址，如 https://vendor.example.com
	ApiKey  string `json:"apiKey,omitempty"`  // 供应商密钥 usr-xxx，用于 X-API-Key 请求头

	// 供应商连接信息（Provider = "kiroapp"）
	KiroappBaseURL string `json:"kiroappBaseUrl,omitempty"` // 留空则用 DefaultKiroappBaseURL
	KiroappApiKey  string `json:"kiroappApiKey,omitempty"`  // 用于 Authorization: Bearer 请求头

	// 导入 Kiro Key 时使用的区域 hint（留空自动探测），两个供应商共用。
	Region string `json:"region,omitempty"`

	// 自动补号策略
	Enabled         bool `json:"enabled,omitempty"`         // 是否启用后台自动补号
	MinPoolSize     int  `json:"minPoolSize,omitempty"`     // 低水位阈值：可用账号数 < 该值时触发补号
	BatchCount      int  `json:"batchCount,omitempty"`      // 每次补号购买的 Key 数量
	IntervalSeconds int  `json:"intervalSeconds,omitempty"` // 自动检查间隔（秒），最小 60

	// 全部凭证禁用时补号（轮询触发）。开启后每轮检查若系统内所有凭证都已被
	// 禁用/封禁（且没有等待自动恢复的临时隔离账号），就提取 AllDeadCount 个 Key
	// 导入。AllDeadCount <= 0 时回退到 BatchCount。
	AllDeadReplenish bool `json:"allDeadReplenish,omitempty"`
	AllDeadCount     int  `json:"allDeadCount,omitempty"`

	// 推送式补号的单次提取上限。供应商 webhook 里的 new_keys 是「可提取上限」而非
	// 必须全取，收到推送时按 min(new_keys, WebhookMaxCount) 提取。<=0 表示不限制，
	// 取供应商推送的全部。
	WebhookMaxCount int `json:"webhookMaxCount,omitempty"`

	// 推送式补号（供应商 webhook）
	// PublicBaseURL 是本服务的公网基地址（如 https://my-proxy.example.com），
	// 用于拼接注册给供应商的回调地址。WebhookSecret 是回调路径内嵌的随机密钥，
	// 供应商推送时不携带管理密码或客户端 Key，路径密钥即该入站端点的唯一鉴权。
	PublicBaseURL string `json:"publicBaseUrl,omitempty"` // 本服务公网基地址
	WebhookSecret string `json:"webhookSecret,omitempty"` // 入站回调路径密钥（自动生成）

	// 运行态（随每次补号更新并持久化）
	LastRunAt      int64  `json:"lastRunAt,omitempty"`      // 上次补号执行时间（Unix 秒）
	LastError      string `json:"lastError,omitempty"`      // 上次补号错误（成功时清空）
	LastResult     string `json:"lastResult,omitempty"`     // 上次补号结果摘要
	LastWebhookAt  int64  `json:"lastWebhookAt,omitempty"`  // 上次收到供应商 webhook 的时间（Unix 秒）
	LastWebhookMsg string `json:"lastWebhookMsg,omitempty"` // 上次 webhook 事件摘要
}

// GetReplenishConfig 返回补号配置的副本。未初始化时返回零值。
func GetReplenishConfig() ReplenishConfig {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return ReplenishConfig{}
	}
	return cfg.Replenish
}

// NormalizeReplenishProvider 把任意输入归一化为已知的供应商标识。
// 空值/未知值一律回落到 ReplenishProviderVendor，保证旧配置与脏输入都能工作。
func NormalizeReplenishProvider(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case ReplenishProviderKiroapp, "kiroapp.cc":
		return ReplenishProviderKiroapp
	default:
		return ReplenishProviderVendor
	}
}

// EffectiveProvider 返回归一化后的供应商标识。
func (rc ReplenishConfig) EffectiveProvider() string {
	return NormalizeReplenishProvider(rc.Provider)
}

// SupportsWebhook 报告当前供应商是否支持推送式补号。
// kiroapp.cc 没有 webhook 接口，只能靠轮询。
func (rc ReplenishConfig) SupportsWebhook() bool {
	return rc.EffectiveProvider() == ReplenishProviderVendor
}

// ActiveBaseURL 返回当前供应商的 API 基地址（已去除末尾斜杠）。
// kiroapp 留空时回落到 DefaultKiroappBaseURL。
func (rc ReplenishConfig) ActiveBaseURL() string {
	if rc.EffectiveProvider() == ReplenishProviderKiroapp {
		if b := strings.TrimRight(strings.TrimSpace(rc.KiroappBaseURL), "/"); b != "" {
			return b
		}
		return DefaultKiroappBaseURL
	}
	return strings.TrimRight(strings.TrimSpace(rc.BaseURL), "/")
}

// ActiveApiKey 返回当前供应商的密钥。
func (rc ReplenishConfig) ActiveApiKey() string {
	if rc.EffectiveProvider() == ReplenishProviderKiroapp {
		return strings.TrimSpace(rc.KiroappApiKey)
	}
	return strings.TrimSpace(rc.ApiKey)
}

// EffectiveAllDeadCount 返回「全部凭证禁用」触发时应提取的 Key 数量。
// AllDeadCount <= 0 时回退到 BatchCount。
func (rc ReplenishConfig) EffectiveAllDeadCount() int {
	if rc.AllDeadCount > 0 {
		return rc.AllDeadCount
	}
	return rc.BatchCount
}

// UpdateReplenishSettings 更新补号的连接信息与策略字段（不含运行态），并持久化。
// intervalSeconds < 60 时会被抬到 60，避免过于频繁地打上游。
func UpdateReplenishSettings(rc ReplenishConfig) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
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
	if rc.WebhookMaxCount < 0 {
		rc.WebhookMaxCount = 0
	}
	if rc.AllDeadCount < 0 {
		rc.AllDeadCount = 0
	}
	// 保留运行态字段，仅覆盖用户可配置项。
	cfg.Replenish.Provider = NormalizeReplenishProvider(rc.Provider)
	cfg.Replenish.BaseURL = rc.BaseURL
	cfg.Replenish.ApiKey = rc.ApiKey
	cfg.Replenish.KiroappBaseURL = strings.TrimRight(strings.TrimSpace(rc.KiroappBaseURL), "/")
	cfg.Replenish.KiroappApiKey = rc.KiroappApiKey
	cfg.Replenish.Region = rc.Region
	cfg.Replenish.Enabled = rc.Enabled
	cfg.Replenish.MinPoolSize = rc.MinPoolSize
	cfg.Replenish.BatchCount = rc.BatchCount
	cfg.Replenish.IntervalSeconds = rc.IntervalSeconds
	cfg.Replenish.WebhookMaxCount = rc.WebhookMaxCount
	cfg.Replenish.AllDeadReplenish = rc.AllDeadReplenish
	cfg.Replenish.AllDeadCount = rc.AllDeadCount
	cfg.Replenish.PublicBaseURL = strings.TrimRight(strings.TrimSpace(rc.PublicBaseURL), "/")
	return Save()
}

// GetOrCreateReplenishWebhookSecret 返回入站回调路径密钥；若尚未生成则创建并持久化。
// 密钥为 32 字节随机的十六进制串，用于 /replenish/webhook/<secret> 的路径鉴权。
func GetOrCreateReplenishWebhookSecret() (string, error) {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return "", errors.New("config not initialized")
	}
	if cfg.Replenish.WebhookSecret != "" {
		return cfg.Replenish.WebhookSecret, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	cfg.Replenish.WebhookSecret = hex.EncodeToString(buf)
	if err := Save(); err != nil {
		return "", err
	}
	return cfg.Replenish.WebhookSecret, nil
}

// ResetReplenishWebhookSecret 重新生成回调路径密钥并持久化，使旧回调地址立即失效。
func ResetReplenishWebhookSecret() (string, error) {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return "", errors.New("config not initialized")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	cfg.Replenish.WebhookSecret = hex.EncodeToString(buf)
	if err := Save(); err != nil {
		return "", err
	}
	return cfg.Replenish.WebhookSecret, nil
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

// RecordReplenishWebhook 持久化一次收到供应商 webhook 的时间与摘要。
func RecordReplenishWebhook(at int64, msg string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	cfg.Replenish.LastWebhookAt = at
	cfg.Replenish.LastWebhookMsg = msg
	return Save()
}
