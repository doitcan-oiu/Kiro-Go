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
	// 凭证健康度用于面板说明「全部凭证禁用」触发条件当前是否成立。
	health := config.GetCredentialHealth()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"provider": rc.EffectiveProvider(),
		// vendor 供应商连接信息
		"baseUrl":      rc.BaseURL,
		"apiKeyMasked": config.MaskApiKey(rc.ApiKey),
		"hasApiKey":    rc.ApiKey != "",
		// kiroapp.cc 供应商连接信息
		"kiroappBaseUrl":      rc.KiroappBaseURL,
		"kiroappBaseUrlHint":  config.DefaultKiroappBaseURL,
		"kiroappApiKeyMasked": config.MaskApiKey(rc.KiroappApiKey),
		"hasKiroappApiKey":    rc.KiroappApiKey != "",
		// kiroapp.io 供应商连接信息
		"kiroappioBaseUrl":      rc.KiroappioBaseURL,
		"kiroappioBaseUrlHint":  config.DefaultKiroappioBaseURL,
		"kiroappioApiKeyMasked": config.MaskApiKey(rc.KiroappioApiKey),
		"hasKiroappioApiKey":    rc.KiroappioApiKey != "",
		// 策略
		"region":           rc.Region,
		"enabled":          rc.Enabled,
		"minPoolSize":      rc.MinPoolSize,
		"batchCount":       rc.BatchCount,
		"intervalSeconds":  rc.IntervalSeconds,
		"allDeadReplenish": rc.AllDeadReplenish,
		"allDeadCount":     rc.AllDeadCount,
		// 推送式补号（vendor 与 kiroapp.io 支持；只有 vendor 能自动注册回调）
		"supportsWebhook":             rc.SupportsWebhook(),
		"supportsWebhookAutoRegister": rc.SupportsWebhookAutoRegister(),
		"webhookMaxCount":             rc.WebhookMaxCount,
		"publicBaseUrl":               rc.PublicBaseURL,
		"webhookUrl":                  webhookURL,
		"hasSecret":                   rc.WebhookSecret != "",
		// 运行态
		"lastRunAt":      rc.LastRunAt,
		"lastError":      rc.LastError,
		"lastResult":     rc.LastResult,
		"lastWebhookAt":  rc.LastWebhookAt,
		"lastWebhookMsg": rc.LastWebhookMsg,
		// 凭证健康度
		"credentialsTotal":       health.Total,
		"credentialsEnabled":     health.Enabled,
		"credentialsAllDisabled": health.AllDisabled(),
	})
}

// apiUpdateReplenish 保存补号配置。apiKey 为空字符串表示保持原值不变（面板不回显明文），
// 传入非空值才覆盖。
func (h *Handler) apiUpdateReplenish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider         *string `json:"provider,omitempty"`
		BaseURL          *string `json:"baseUrl,omitempty"`
		ApiKey           *string `json:"apiKey,omitempty"`
		KiroappBaseURL   *string `json:"kiroappBaseUrl,omitempty"`
		KiroappApiKey    *string `json:"kiroappApiKey,omitempty"`
		KiroappioBaseURL *string `json:"kiroappioBaseUrl,omitempty"`
		KiroappioApiKey  *string `json:"kiroappioApiKey,omitempty"`
		Region           *string `json:"region,omitempty"`
		Enabled          *bool   `json:"enabled,omitempty"`
		MinPoolSize      *int    `json:"minPoolSize,omitempty"`
		BatchCount       *int    `json:"batchCount,omitempty"`
		IntervalSeconds  *int    `json:"intervalSeconds,omitempty"`
		AllDeadReplenish *bool   `json:"allDeadReplenish,omitempty"`
		AllDeadCount     *int    `json:"allDeadCount,omitempty"`
		WebhookMaxCount  *int    `json:"webhookMaxCount,omitempty"`
		PublicBaseURL    *string `json:"publicBaseUrl,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	// 以当前配置为基线，仅覆盖请求中出现的字段。
	rc := config.GetReplenishConfig()
	if req.Provider != nil {
		rc.Provider = *req.Provider
	}
	if req.BaseURL != nil {
		rc.BaseURL = *req.BaseURL
	}
	if req.ApiKey != nil && *req.ApiKey != "" {
		rc.ApiKey = *req.ApiKey
	}
	if req.KiroappBaseURL != nil {
		rc.KiroappBaseURL = *req.KiroappBaseURL
	}
	// 与 apiKey 同理：空字符串表示保持原密钥不变（面板不回显明文）。
	if req.KiroappApiKey != nil && *req.KiroappApiKey != "" {
		rc.KiroappApiKey = *req.KiroappApiKey
	}
	if req.KiroappioBaseURL != nil {
		rc.KiroappioBaseURL = *req.KiroappioBaseURL
	}
	if req.KiroappioApiKey != nil && *req.KiroappioApiKey != "" {
		rc.KiroappioApiKey = *req.KiroappioApiKey
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
	if req.AllDeadReplenish != nil {
		rc.AllDeadReplenish = *req.AllDeadReplenish
	}
	if req.AllDeadCount != nil {
		rc.AllDeadCount = *req.AllDeadCount
	}
	if req.WebhookMaxCount != nil {
		rc.WebhookMaxCount = *req.WebhookMaxCount
	}
	if req.PublicBaseURL != nil {
		rc.PublicBaseURL = *req.PublicBaseURL
	}

	if err := config.UpdateReplenishSettings(rc); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 需要手工注册回调的供应商（kiroapp.io 只能在其站点后台填地址）没有「注册回调」
	// 按钮去顺带生成密钥，因此一配好公网地址就把密钥备好，GET 才有完整回调地址可复制。
	if rc.SupportsWebhook() && !rc.SupportsWebhookAutoRegister() && strings.TrimSpace(rc.PublicBaseURL) != "" {
		if _, err := config.GetOrCreateReplenishWebhookSecret(); err != nil {
			logger.Warnf("[Replenish] generate webhook secret failed: %v", err)
		}
	}

	// 配置变更后重启后台循环，使新的 enabled/interval 立即生效。
	h.restartReplenishLoop()
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// apiTestReplenish 测试当前所选供应商的连通性：查询账户信息与可提取上限。
// 各供应商字段覆盖度不同（kiroapp 只有余额与单价），缺失的字段直接不返回，
// 由前端按存在性展示。
func (h *Handler) apiTestReplenish(w http.ResponseWriter, r *http.Request) {
	rc := config.GetReplenishConfig()
	client, err := newReplenishSupplier(rc)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	acc, err := client.Account()
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	resp := map[string]interface{}{
		"success":   true,
		"provider":  client.ProviderName(),
		"remaining": acc.Remaining,
	}
	if acc.Name != "" {
		resp["name"] = acc.Name
	}
	if acc.HasQuota {
		resp["quota"] = acc.Quota
		resp["usedQuota"] = acc.UsedQuota
	}
	if acc.HasPrice {
		resp["keyPrice"] = acc.KeyPrice
		resp["priceMin"] = acc.KeyPrice
		// 阶梯定价的供应商还给出上限，前端据此展示价格区间。
		if acc.PriceMax > 0 {
			resp["priceMax"] = acc.PriceMax
		}
	}
	// 库存查询失败不致命，作为附加信息返回。
	if stock, stockErr := client.Stock(); stockErr == nil {
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
		"provider":  res.Provider,
		"summary":   res.Summary,
	})
}

// apiRegisterReplenishWebhook 曾把回调地址注册到供应商（PUT /api/my/webhook）。
// 供应商客户端已全部移除，已无可调用的注册接口，故一律返回 400。端点保留是为了让
// 旧前端拿到明确原因而不是 404。
//
// 接回供应商时，在这里恢复「构造客户端 → 生成 secret → 调其注册接口」的流程。
func (h *Handler) apiRegisterReplenishWebhook(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": errNoReplenishSupplier.Error()})
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
