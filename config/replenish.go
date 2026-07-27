// Package config — 在线补号（自动补充账号池）相关配置。
//
// 补号功能通过对接上游“供应商 API”（X-API-Key: usr-xxx）在线购买 Kiro API Key
// (ksk_...)，并把它们作为 api_key 账号导入本地账号池。配置分两部分：
//   - 供应商连接信息（BaseURL / ApiKey / Region）
//   - 自动补号策略（Enabled / MinPoolSize / BatchCount / IntervalSeconds）
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

// ReplenishConfig 保存在线补号的全部配置与最近一次运行的状态。
type ReplenishConfig struct {
	// 供应商连接信息
	BaseURL string `json:"baseUrl,omitempty"` // 供应商 API 基地址，如 https://vendor.example.com
	ApiKey  string `json:"apiKey,omitempty"`  // 供应商密钥 usr-xxx，用于 X-API-Key 请求头
	Region  string `json:"region,omitempty"`  // 导入 Kiro Key 时使用的区域 hint（留空自动探测）

	// 自动补号策略
	Enabled         bool `json:"enabled,omitempty"`         // 是否启用后台自动补号
	MinPoolSize     int  `json:"minPoolSize,omitempty"`     // 低水位阈值：可用账号数 < 该值时触发补号
	BatchCount      int  `json:"batchCount,omitempty"`      // 每次补号购买的 Key 数量
	IntervalSeconds int  `json:"intervalSeconds,omitempty"` // 自动检查间隔（秒），最小 60

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
	// 保留运行态字段，仅覆盖用户可配置项。
	cfg.Replenish.BaseURL = rc.BaseURL
	cfg.Replenish.ApiKey = rc.ApiKey
	cfg.Replenish.Region = rc.Region
	cfg.Replenish.Enabled = rc.Enabled
	cfg.Replenish.MinPoolSize = rc.MinPoolSize
	cfg.Replenish.BatchCount = rc.BatchCount
	cfg.Replenish.IntervalSeconds = rc.IntervalSeconds
	cfg.Replenish.WebhookMaxCount = rc.WebhookMaxCount
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
