package proxy

// 在线补号的管理面板 HTTP 接口：
//   GET  /admin/api/replenish                读取两家供应商配置 + 策略 + 运行态
//   POST /admin/api/replenish                保存（每家独立的开关/连接/推送数量 + 共用策略）
//   POST /admin/api/replenish/test           测试各启用供应商的连通性
//   POST /admin/api/replenish/run            立即手动补号一次（两家并行）
//   POST /admin/api/replenish/register-webhook   把回调地址注册到支持注册的供应商
//   POST /admin/api/replenish/reset-secret       重置某家的回调路径密钥
//
// 公开入站端点（无管理密码，路径密钥鉴权）：
//   POST /replenish/webhook/<provider>/<secret>  接收指定供应商的推送并只买它
//   POST /replenish/webhook/<secret>             兼容旧地址，按密钥反查供应商

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

// replenishWebhookPath 拼接某家供应商的完整回调地址：
// <公网基地址>/replenish/webhook/<provider>/<secret>。
// 路径里带 provider 是为了让两家各有专属地址：收到推送即可确定是谁在推，
// 从而只动用该家的余额，不会误买另一家。
func replenishWebhookPath(publicBase, provider, secret string) string {
	return strings.TrimRight(publicBase, "/") + "/replenish/webhook/" + provider + "/" + secret
}

// replenishSupplierView 投影单家供应商的面板视图。密钥只回掩码，不回明文。
func replenishSupplierView(rc config.ReplenishConfig, provider string) map[string]interface{} {
	sc := rc.Supplier(provider)
	view := map[string]interface{}{
		"provider":     provider,
		"enabled":      sc.Enabled,
		"baseUrl":      sc.BaseURL,
		"baseUrlHint":  config.DefaultSupplierBaseURL(provider),
		"apiKeyMasked": config.MaskApiKey(sc.ApiKey),
		"hasApiKey":    sc.ApiKey != "",
		"webhookCount": sc.WebhookCount,
		// 能力位：前端据此决定是否显示「注册回调」按钮。
		"supportsWebhookAutoRegister": config.SupportsWebhookAutoRegister(provider),
		"hasSecret":                   sc.WebhookSecret != "",
		// 该家最近一次收到推送。
		"lastWebhookAt":  sc.LastWebhookAt,
		"lastWebhookMsg": sc.LastWebhookMsg,
	}
	// 回调地址要等公网基地址与密钥都就绪才有意义。字段名与注册/重置接口的返回
	// 保持一致（callbackUrl），前端拿到哪个都是同一个语义。
	if rc.PublicBaseURL != "" && sc.WebhookSecret != "" {
		view["callbackUrl"] = replenishWebhookPath(rc.PublicBaseURL, provider, sc.WebhookSecret)
	}
	return view
}

// apiGetReplenish 返回两家供应商配置与共用策略、运行态。
func (h *Handler) apiGetReplenish(w http.ResponseWriter, r *http.Request) {
	rc := config.GetReplenishConfig()

	suppliers := make([]map[string]interface{}, 0, 2)
	for _, p := range config.ReplenishProviders() {
		suppliers = append(suppliers, replenishSupplierView(rc, p))
	}

	// 凭证健康度用于面板说明「全部凭证禁用」触发条件当前是否成立。
	health := config.GetCredentialHealth()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"suppliers": suppliers,
		// 共用策略
		"region":           rc.Region,
		"enabled":          rc.Enabled,
		"minPoolSize":      rc.MinPoolSize,
		"batchCount":       rc.BatchCount,
		"intervalSeconds":  rc.IntervalSeconds,
		"allDeadReplenish": rc.AllDeadReplenish,
		"allDeadCount":     rc.AllDeadCount,
		"publicBaseUrl":    rc.PublicBaseURL,
		// 运行态
		"lastRunAt":  rc.LastRunAt,
		"lastError":  rc.LastError,
		"lastResult": rc.LastResult,
		// 凭证健康度
		"credentialsTotal":       health.Total,
		"credentialsEnabled":     health.Enabled,
		"credentialsAllDisabled": health.AllDisabled(),
	})
}

// apiUpdateReplenish 保存补号配置。
//
// 密钥语义与面板一致：字段缺省或为空字符串表示「保持原密钥不变」（面板不回显明文），
// 只有传入非空值才覆盖。
func (h *Handler) apiUpdateReplenish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// 每家一份，键为供应商标识。
		Suppliers map[string]struct {
			Enabled      *bool   `json:"enabled,omitempty"`
			BaseURL      *string `json:"baseUrl,omitempty"`
			ApiKey       *string `json:"apiKey,omitempty"`
			WebhookCount *int    `json:"webhookCount,omitempty"`
		} `json:"suppliers,omitempty"`

		Region           *string `json:"region,omitempty"`
		Enabled          *bool   `json:"enabled,omitempty"`
		MinPoolSize      *int    `json:"minPoolSize,omitempty"`
		BatchCount       *int    `json:"batchCount,omitempty"`
		IntervalSeconds  *int    `json:"intervalSeconds,omitempty"`
		AllDeadReplenish *bool   `json:"allDeadReplenish,omitempty"`
		AllDeadCount     *int    `json:"allDeadCount,omitempty"`
		PublicBaseURL    *string `json:"publicBaseUrl,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	// 以当前配置为基线，仅覆盖请求中出现的字段。
	rc := config.GetReplenishConfig()

	// 各家的变更整理成 SupplierUpdate 交给 config 层合并（含「空密钥=保持不变」
	// 与数值夹取），避免在两处重复实现同一套语义。
	updates := make(map[string]config.SupplierUpdate, len(req.Suppliers))
	for rawProvider, in := range req.Suppliers {
		provider := config.NormalizeReplenishProvider(rawProvider)
		if provider == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "unknown supplier: " + rawProvider})
			return
		}
		updates[provider] = config.SupplierUpdate{
			Enabled:      in.Enabled,
			BaseURL:      in.BaseURL,
			ApiKey:       in.ApiKey,
			WebhookCount: in.WebhookCount,
		}
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
	if req.PublicBaseURL != nil {
		rc.PublicBaseURL = *req.PublicBaseURL
	}

	if err := config.UpdateReplenishSettings(rc, updates); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 重新读取：各家的 Enabled 由 UpdateReplenishSettings 合并而成，
	// 请求里可能并未携带某家的字段，只有落盘后的配置才是权威状态。
	rc = config.GetReplenishConfig()

	// 公网地址就绪后，为每家启用的供应商备好回调密钥，GET 才有完整地址可复制。
	// kiroapp.io 只能手工把地址填进它的后台，没有「注册回调」按钮顺带生成密钥，
	// 因此这里主动生成。
	if strings.TrimSpace(rc.PublicBaseURL) != "" {
		for _, p := range config.ReplenishProviders() {
			if !rc.Supplier(p).Enabled {
				continue
			}
			if _, err := config.GetOrCreateSupplierWebhookSecret(p); err != nil {
				logger.Warnf("[Replenish] generate webhook secret for %s failed: %v", p, err)
			}
		}
	}

	// 配置变更后重启后台循环，使新的 enabled/interval 立即生效。
	h.restartReplenishLoop()
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// apiTestReplenish 测试每家已启用供应商的连通性，逐家返回结果。
//
// 一家失败不影响另一家的结果展示：整体仍返回 200，由前端按 ok 标志分别提示。
// 这样用户能一眼看出是「两家都挂了」还是「只有一家配错」。
func (h *Handler) apiTestReplenish(w http.ResponseWriter, r *http.Request) {
	rc := config.GetReplenishConfig()
	clients, buildErrs, err := newEnabledReplenishSuppliers(rc)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	results := make([]map[string]interface{}, 0, len(clients)+len(buildErrs))
	anyOK := false
	// 构造阶段就失败的（例如密钥没填）也要出现在结果里，否则那一家会从面板上凭空消失。
	for provider, msg := range buildErrs {
		results = append(results, map[string]interface{}{
			"provider": provider,
			"ok":       false,
			"error":    msg,
		})
	}
	for _, client := range clients {
		item := map[string]interface{}{"provider": client.ProviderName()}
		acc, err := client.Account()
		if err != nil {
			item["ok"] = false
			item["error"] = err.Error()
			results = append(results, item)
			continue
		}
		anyOK = true
		item["ok"] = true
		item["remaining"] = acc.Remaining
		if acc.Name != "" {
			item["name"] = acc.Name
		}
		if acc.HasQuota {
			item["quota"] = acc.Quota
			item["usedQuota"] = acc.UsedQuota
		}
		if acc.HasPrice {
			item["priceMin"] = acc.KeyPrice
			if acc.PriceMax > 0 {
				item["priceMax"] = acc.PriceMax
			}
		}
		// 库存查询失败不致命，作为附加信息返回。
		if stock, serr := client.Stock(); serr == nil {
			item["stock"] = stock
		}
		results = append(results, item)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   anyOK,
		"suppliers": results,
	})
}

// apiRunReplenish 立即手动补号一次（所有启用的供应商各买一批）。
// 可选 body {"count":N} 覆盖每家的购买数量。
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

	_ = config.RecordReplenishRun(now, res.Summary, res.ErrorText())
	logger.Infof("[Replenish] manual run: %s", res.Summary)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"purchased": res.Purchased,
		"imported":  res.Imported,
		"skipped":   res.Skipped,
		"summary":   res.Summary,
		"suppliers": res.SupplierPayload(),
	})
}

// apiRegisterReplenishWebhook 把本服务的回调地址注册到支持注册接口的供应商。
//
// body {"provider":"..."} 指定注册哪一家；省略则对所有支持注册且已启用的供应商都注册。
// 不支持注册的（kiroapp.io）会在结果里说明需要手工填写，不算失败。
func (h *Handler) apiRegisterReplenishWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	rc := config.GetReplenishConfig()
	if strings.TrimSpace(rc.PublicBaseURL) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "publicBaseUrl is not configured"})
		return
	}

	// 确定目标供应商集合。
	targets := config.ReplenishProviders()
	if p := config.NormalizeReplenishProvider(req.Provider); p != "" {
		targets = []string{p}
	} else if strings.TrimSpace(req.Provider) != "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown supplier: " + req.Provider})
		return
	}

	results := make([]map[string]interface{}, 0, len(targets))
	anyOK := false
	for _, provider := range targets {
		item := map[string]interface{}{"provider": provider}

		if !config.SupportsWebhookAutoRegister(provider) {
			// 这家没有注册接口，只能在它自己的后台填地址。给出地址供复制。
			item["ok"] = false
			item["manual"] = true
			secret, err := config.GetOrCreateSupplierWebhookSecret(provider)
			if err == nil {
				item["callbackUrl"] = replenishWebhookPath(rc.PublicBaseURL, provider, secret)
			}
			item["error"] = "this supplier has no registration API; paste the callback URL into its dashboard"
			results = append(results, item)
			continue
		}

		client, err := newReplenishSupplier(provider, rc.Supplier(provider))
		if err != nil {
			item["ok"] = false
			item["error"] = err.Error()
			results = append(results, item)
			continue
		}
		registrar, ok := client.(webhookRegistrar)
		if !ok {
			item["ok"] = false
			item["error"] = "supplier client does not implement webhook registration"
			results = append(results, item)
			continue
		}

		secret, err := config.GetOrCreateSupplierWebhookSecret(provider)
		if err != nil {
			item["ok"] = false
			item["error"] = err.Error()
			results = append(results, item)
			continue
		}

		callbackURL := replenishWebhookPath(rc.PublicBaseURL, provider, secret)
		if err := registrar.SetWebhook(callbackURL); err != nil {
			item["ok"] = false
			item["error"] = err.Error()
			item["callbackUrl"] = callbackURL
			results = append(results, item)
			continue
		}

		anyOK = true
		item["ok"] = true
		item["callbackUrl"] = callbackURL
		logger.Infof("[Replenish] registered webhook callback with %s", provider)
		results = append(results, item)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   anyOK,
		"suppliers": results,
	})
}

// apiResetReplenishSecret 重置某家的回调路径密钥，其旧回调地址立即失效。
// body {"provider":"..."} 必填：两家密钥独立，必须明确重置哪一家，避免误伤另一家。
func (h *Handler) apiResetReplenishSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	provider := config.NormalizeReplenishProvider(req.Provider)
	if provider == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "provider is required"})
		return
	}

	secret, err := config.ResetSupplierWebhookSecret(provider)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	rc := config.GetReplenishConfig()
	resp := map[string]interface{}{"success": true, "provider": provider}
	if strings.TrimSpace(rc.PublicBaseURL) != "" {
		resp["callbackUrl"] = replenishWebhookPath(rc.PublicBaseURL, provider, secret)
	}
	json.NewEncoder(w).Encode(resp)
}

// handleReplenishWebhook 是公开入站端点的处理器，路径形如
// /replenish/webhook/<provider>/<secret> 或旧式 /replenish/webhook/<secret>。
//
// 它不走管理密码鉴权，改由路径内嵌的 secret 做常数时间比对（见
// config.FindProviderByWebhookSecret）。密钥对不上或未配置时统一 404，
// 不泄露端点是否存在、也不区分「没配」和「配错」。
//
// pathTail 是 /replenish/webhook/ 之后的全部内容。
func (h *Handler) handleReplenishWebhook(w http.ResponseWriter, r *http.Request, pathTail string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// 解析 <provider>/<secret>，兼容只有 <secret> 的旧地址。
	tail := strings.Trim(strings.TrimSpace(pathTail), "/")
	var declaredProvider, secret string
	if i := strings.Index(tail, "/"); i >= 0 {
		declaredProvider = config.NormalizeReplenishProvider(tail[:i])
		secret = tail[i+1:]
	} else {
		secret = tail
	}

	// 密钥反查供应商：这是唯一的鉴权，也顺带确定了「是谁在推」。
	provider := config.FindProviderByWebhookSecret(secret)
	// 路径声明了供应商时必须与密钥归属一致，避免用 A 的密钥触发买 B。
	if provider == "" || (declaredProvider != "" && declaredProvider != provider) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}

	// 限制请求体大小，供应商事件很小。先读原始字节：各家载荷字段不尽相同，
	// 遇到无法识别的事件时把原文记进日志，才能据实补映射而不是靠猜。
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "read body failed"})
		return
	}
	var evt supplierWebhookEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		logger.Warnf("[Replenish] webhook from %s: invalid JSON: %s", provider, truncateForLog(raw, 512))
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	// 同步处理并返回结果：处理逻辑自身幂等（复用供应商订单号），且供应商通常有重试。
	msg, err := h.handleProviderWebhookEvent(provider, evt)
	now := time.Now().Unix()
	if err != nil {
		_ = config.RecordSupplierWebhook(provider, now, "error: "+err.Error())
		// 带上原始载荷：事件名不认识时，这是唯一能看出该家真实字段的线索。
		logger.Warnf("[Replenish] webhook event %q from %s failed: %v | payload=%s",
			evt.Event, provider, err, truncateForLog(raw, 512))
		// 仍返回 200，避免供应商因我方内部错误无限重试；错误已记录到运行态。
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	_ = config.RecordSupplierWebhook(provider, now, msg)
	logger.Infof("[Replenish] webhook event %q from %s: %s", evt.Event, provider, msg)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "message": msg})
}

// truncateForLog 返回可安全写入日志的载荷摘要。
// webhook 载荷不含密钥明文（各家文档均如此），但仍限长避免刷爆日志。
func truncateForLog(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
