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

import "errors"

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

	// 运行态（随每次补号更新并持久化）
	LastRunAt  int64  `json:"lastRunAt,omitempty"`  // 上次补号执行时间（Unix 秒）
	LastError  string `json:"lastError,omitempty"`  // 上次补号错误（成功时清空）
	LastResult string `json:"lastResult,omitempty"` // 上次补号结果摘要
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
	// 保留运行态字段，仅覆盖用户可配置项。
	cfg.Replenish.BaseURL = rc.BaseURL
	cfg.Replenish.ApiKey = rc.ApiKey
	cfg.Replenish.Region = rc.Region
	cfg.Replenish.Enabled = rc.Enabled
	cfg.Replenish.MinPoolSize = rc.MinPoolSize
	cfg.Replenish.BatchCount = rc.BatchCount
	cfg.Replenish.IntervalSeconds = rc.IntervalSeconds
	return Save()
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
