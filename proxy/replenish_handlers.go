package proxy

// 在线补号的管理面板 HTTP 接口：
//   GET  /admin/api/replenish                读取补号配置 + 运行态
//   POST /admin/api/replenish                保存补号配置（连接信息 + 自动策略 + 公网地址）
//   POST /admin/api/replenish/test           测试供应商连通性（返回余额/库存）
//   POST /admin/api/replenish/run            立即手动补号一次
//   POST /admin/api/replenish/register-webhook   把回调地址注册到供应商
//   POST /admin/api/replenish/reset-secret       重置回调路径密钥
//
// 公开入站端点（无管理密码，路径密钥鉴权）：
//   POST /replenish/webhook/<secret>         接收供应商推送并按事件补号

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

// replenishWebhookPath 拼接注册给供应商的完整回调地址：<公网基地址>/replenish/webhook/<secret>。
// publicBase 已在保存时去除末尾斜杠。
func replenishWebhookPath(publicBase, secret string) string {
	return strings.TrimRight(publicBase, "/") + "/replenish/webhook/" + secret
}

// apiGetReplenish 返回补号配置与运行态。apiKey 做掩码，避免在面板明文回显；
// 回调地址仅在已配置公网基地址且密钥已生成时返回完整 URL。
func (h *Handler) apiGetReplenish(w http.ResponseWriter, r *http.Request) {
	rc := config.GetReplenishConfig()
	webhookURL := ""
	if rc.PublicBaseURL != "" && rc.WebhookSecret != "" {
		webhookURL = replenishWebhookPath(rc.PublicBaseURL, rc.WebhookSecret)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"baseUrl":         rc.BaseURL,
		"apiKeyMasked":    config.MaskApiKey(rc.ApiKey),
		"hasApiKey":       rc.ApiKey != "",
		"region":          rc.Region,
		"enabled":         rc.Enabled,
		"minPoolSize":     rc.MinPoolSize,
		"batchCount":      rc.BatchCount,
		"intervalSeconds": rc.IntervalSeconds,
		"publicBaseUrl":   rc.PublicBaseURL,
		"webhookUrl":      webhookURL,
		"hasSecret":       rc.WebhookSecret != "",
		"lastRunAt":       rc.LastRunAt,
		"lastError":       rc.LastError,
		"lastResult":      rc.LastResult,
		"lastWebhookAt":   rc.LastWebhookAt,
		"lastWebhookMsg":  rc.LastWebhookMsg,
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
		PublicBaseURL   *string `json:"publicBaseUrl,omitempty"`
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
	if req.PublicBaseURL != nil {
		rc.PublicBaseURL = *req.PublicBaseURL
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

// apiRegisterReplenishWebhook 把本服务的回调地址注册到供应商（PUT /api/my/webhook）。
// 回调地址为 <公网基地址>/replenish/webhook/<secret>；secret 若尚未生成则自动创建。
// 需先保存公网基地址（PublicBaseURL）。
func (h *Handler) apiRegisterReplenishWebhook(w http.ResponseWriter, r *http.Request) {
	rc := config.GetReplenishConfig()
	if strings.TrimSpace(rc.PublicBaseURL) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "publicBaseUrl is not configured"})
		return
	}

	client, err := newSupplierClient(rc)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	secret, err := config.GetOrCreateReplenishWebhookSecret()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	callbackURL := replenishWebhookPath(rc.PublicBaseURL, secret)
	if err := client.SetWebhook(callbackURL); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	logger.Infof("[Replenish] registered webhook callback with supplier")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"callbackUrl": callbackURL,
	})
}

// apiResetReplenishSecret 重置回调路径密钥，旧回调地址立即失效。
// 重置后需要重新点击「注册回调」把新地址推给供应商。
func (h *Handler) apiResetReplenishSecret(w http.ResponseWriter, r *http.Request) {
	secret, err := config.ResetReplenishWebhookSecret()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	rc := config.GetReplenishConfig()
	resp := map[string]interface{}{"success": true}
	if strings.TrimSpace(rc.PublicBaseURL) != "" {
		resp["callbackUrl"] = replenishWebhookPath(rc.PublicBaseURL, secret)
	}
	json.NewEncoder(w).Encode(resp)
}

// handleReplenishWebhook 是公开入站端点 POST /replenish/webhook/<secret> 的处理器。
// 它不走管理密码鉴权，改由路径内嵌的 secret 做常数时间比对；secret 未配置时直接 404，
// 不暴露端点是否存在。供应商推送 new_keys_available 时会据此按事件订单号补号。
func (h *Handler) handleReplenishWebhook(w http.ResponseWriter, r *http.Request, pathSecret string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// secret 未配置 → 端点视为未启用，返回 404。
	want := config.GetReplenishConfig().WebhookSecret
	if want == "" {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	// 常数时间比对，避免时序侧信道；长度不同 subtle 会直接返回 0。
	if subtle.ConstantTimeCompare([]byte(pathSecret), []byte(want)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}

	// 限制请求体大小，供应商事件很小。
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var evt supplierWebhookEvent
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	// 同步处理并返回结果：处理逻辑自身幂等（复用供应商订单号），且供应商通常有重试。
	msg, err := h.handleReplenishWebhookEvent(evt)
	now := time.Now().Unix()
	if err != nil {
		_ = config.RecordReplenishWebhook(now, "error: "+err.Error())
		logger.Warnf("[Replenish] webhook event %q failed: %v", evt.Event, err)
		// 仍返回 200，避免供应商因我方内部错误无限重试；错误已记录到运行态。
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	_ = config.RecordReplenishWebhook(now, msg)
	logger.Infof("[Replenish] webhook event %q: %s", evt.Event, msg)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "message": msg})
}
