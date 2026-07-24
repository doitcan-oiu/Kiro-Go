package proxy

// 在线补号的管理面板 HTTP 接口：
//   GET  /admin/api/replenish            读取补号配置 + 运行态
//   POST /admin/api/replenish            保存补号配置（连接信息 + 自动策略）
//   POST /admin/api/replenish/test       测试供应商连通性（返回余额/库存）
//   POST /admin/api/replenish/run        立即手动补号一次

import (
	"encoding/json"
	"net/http"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

// apiGetReplenish 返回补号配置与运行态。apiKey 做掩码，避免在面板明文回显。
func (h *Handler) apiGetReplenish(w http.ResponseWriter, r *http.Request) {
	rc := config.GetReplenishConfig()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"baseUrl":         rc.BaseURL,
		"apiKeyMasked":    config.MaskApiKey(rc.ApiKey),
		"hasApiKey":       rc.ApiKey != "",
		"region":          rc.Region,
		"enabled":         rc.Enabled,
		"minPoolSize":     rc.MinPoolSize,
		"batchCount":      rc.BatchCount,
		"intervalSeconds": rc.IntervalSeconds,
		"lastRunAt":       rc.LastRunAt,
		"lastError":       rc.LastError,
		"lastResult":      rc.LastResult,
	})
}

// apiUpdateReplenish 保存补号配置。apiKey 为空字符串表示保持原值不变（面板不回显明文），
// 传入非空值才覆盖。
func (h *Handler) apiUpdateReplenish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL         *string `json:"baseUrl,omitempty"`
		ApiKey          *string `json:"apiKey,omitempty"`
		Region          *string `json:"region,omitempty"`
		Enabled         *bool   `json:"enabled,omitempty"`
		MinPoolSize     *int    `json:"minPoolSize,omitempty"`
		BatchCount      *int    `json:"batchCount,omitempty"`
		IntervalSeconds *int    `json:"intervalSeconds,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	// 以当前配置为基线，仅覆盖请求中出现的字段。
	rc := config.GetReplenishConfig()
	if req.BaseURL != nil {
		rc.BaseURL = *req.BaseURL
	}
	if req.ApiKey != nil && *req.ApiKey != "" {
		rc.ApiKey = *req.ApiKey
	}
	if req.Region != nil {
		rc.Region = *req.Region
	}
	if req.Enabled != nil {
		rc.Enabled = *req.Enabled
	}
	if req.MinPoolSize != nil {
		rc.MinPoolSize = *req.MinPoolSize
	}
	if req.BatchCount != nil {
		rc.BatchCount = *req.BatchCount
	}
	if req.IntervalSeconds != nil {
		rc.IntervalSeconds = *req.IntervalSeconds
	}

	if err := config.UpdateReplenishSettings(rc); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 配置变更后重启后台循环，使新的 enabled/interval 立即生效。
	h.restartReplenishLoop()
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// apiTestReplenish 测试供应商连通性：查询余额与本轮可提取上限。
func (h *Handler) apiTestReplenish(w http.ResponseWriter, r *http.Request) {
	client, err := newSupplierClient(config.GetReplenishConfig())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	profile, err := client.Profile()
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 库存查询失败不致命，作为附加信息返回。
	stock, stockErr := client.Stock()
	resp := map[string]interface{}{
		"success":   true,
		"name":      profile.Name,
		"quota":     profile.Quota,
		"remaining": profile.Remaining,
		"usedQuota": profile.UsedQuota,
	}
	if stockErr == nil {
		resp["stock"] = stock
	}
	json.NewEncoder(w).Encode(resp)
}

// apiRunReplenish 立即手动补号一次。可选 body {"count":N} 覆盖批量大小。
func (h *Handler) apiRunReplenish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Count int `json:"count,omitempty"`
	}
	// body 可选：解析失败按默认批量处理。
	_ = json.NewDecoder(r.Body).Decode(&req)

	res, err := h.runReplenishOnce(req.Count)
	now := time.Now().Unix()
	if err != nil {
		_ = config.RecordReplenishRun(now, "", err.Error())
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = config.RecordReplenishRun(now, res.Summary, "")
	logger.Infof("[Replenish] manual run: %s", res.Summary)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"purchased": res.Purchased,
		"imported":  res.Imported,
		"skipped":   res.Skipped,
		"remaining": res.Remaining,
		"orderId":   res.OrderID,
		"summary":   res.Summary,
	})
}
