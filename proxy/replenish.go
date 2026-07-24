package proxy

// 在线补号（在线购买 Kiro API Key 并导入账号池）。
//
// 本文件实现两层：
//   1. supplierClient —— 对接上游“供应商 API”（对接文档.md）。所有请求带
//      X-API-Key 头，覆盖余额查询（/api/my/profile）、可提取上限（/api/my/stock）
//      与提取 Key（/api/my/purchase）。
//   2. Handler 上的补号编排 —— runReplenishOnce 购买一批 Key 并复用既有的
//      ImportApiKeys 导入到账号池；backgroundReplenish 按低水位策略周期性触发。
//
// 幂等：purchase 必填 client_order_id（32 位十六进制）。同一订单号+相同 count 重
// 试返回首次结果且不重复扣费，因此调用方失败重试务必复用同一订单号。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

// supplierClient 是对接上游供应商 API 的最小客户端。
type supplierClient struct {
	baseURL string
	apiKey  string
}

// newSupplierClient 从补号配置构造客户端；baseURL/apiKey 缺失时返回错误。
func newSupplierClient(rc config.ReplenishConfig) (*supplierClient, error) {
	base := strings.TrimRight(strings.TrimSpace(rc.BaseURL), "/")
	key := strings.TrimSpace(rc.ApiKey)
	if base == "" {
		return nil, errors.New("supplier baseUrl is not configured")
	}
	if key == "" {
		return nil, errors.New("supplier apiKey is not configured")
	}
	return &supplierClient{baseURL: base, apiKey: key}, nil
}

// supplierProfile 对应 GET /api/my/profile 响应。
type supplierProfile struct {
	Name       string  `json:"name"`
	Quota      float64 `json:"quota"`
	Remaining  float64 `json:"remaining"`
	UsedQuota  float64 `json:"used_quota"`
	WebhookURL string  `json:"webhook_url"`
}

// supplierStock 对应 GET /api/my/stock 响应。
type supplierStock struct {
	Max int `json:"max"`
}

// supplierPurchaseResp 对应 POST /api/my/purchase 响应。
type supplierPurchaseResp struct {
	ClientOrderID string  `json:"client_order_id"`
	Purchased     int     `json:"purchased"`
	Remaining     float64 `json:"remaining"`
	Keys          []struct {
		Key string `json:"key"`
	} `json:"keys"`
}

// do 发起一次带认证的请求，解析 JSON 到 out。非 2xx 时尝试解析 {"error":...}。
// method 为 GET 时 body 应为 nil。
func (c *supplierClient) do(method, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// 复用 Kiro REST client（遵循全局出站代理配置），10s 超时够用。
	resp, err := GetRestClientForProxy(config.GetProxyURL()).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 失败响应格式为 {"error":"..."}
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("supplier %s %s: HTTP %d: %s", method, path, resp.StatusCode, errResp.Error)
		}
		return fmt.Errorf("supplier %s %s: HTTP %d", method, path, resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("supplier %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

func (c *supplierClient) Profile() (*supplierProfile, error) {
	var p supplierProfile
	if err := c.do(http.MethodGet, "/api/my/profile", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *supplierClient) Stock() (int, error) {
	var s supplierStock
	if err := c.do(http.MethodGet, "/api/my/stock", nil, &s); err != nil {
		return 0, err
	}
	return s.Max, nil
}

// Purchase 提取 count 个 Key。clientOrderID 必须是 32 位十六进制字符串；
// 失败重试时复用同一订单号可避免重复扣费。
func (c *supplierClient) Purchase(count int, clientOrderID string) (*supplierPurchaseResp, error) {
	body := map[string]interface{}{
		"count":           count,
		"client_order_id": clientOrderID,
	}
	var r supplierPurchaseResp
	if err := c.do(http.MethodPost, "/api/my/purchase", body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// SetWebhook 通过 PUT /api/my/webhook 把回调地址注册到供应商。
func (c *supplierClient) SetWebhook(webhookURL string) error {
	body := map[string]interface{}{"webhook_url": webhookURL}
	return c.do(http.MethodPut, "/api/my/webhook", body, nil)
}

// newClientOrderID 生成 32 位十六进制订单号（16 字节随机）。
func newClientOrderID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 退化到时间戳派生，极少发生；仍满足 32 位十六进制格式。
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// replenishResult 是一次补号运行的结构化结果，返回给前端并写入运行态。
type replenishResult struct {
	Purchased int     `json:"purchased"`         // 供应商实际出 Key 数
	Imported  int     `json:"imported"`          // 成功导入账号池的数量
	Skipped   int     `json:"skipped"`           // 因重复被跳过的数量
	Remaining float64 `json:"remaining"`         // 供应商侧剩余余额
	OrderID   string  `json:"orderId,omitempty"` // 本次订单号
	Summary   string  `json:"summary,omitempty"` // 人类可读摘要
}

// replenishMu 串行化补号运行，避免手动触发与后台循环并发购买。
var replenishMu sync.Mutex

// runReplenishOnce 执行一次补号：购买 count 个 Key 并导入账号池。
// count <= 0 时使用配置的 BatchCount。购买前会用 /api/my/stock 夹取实际可提取上限。
func (h *Handler) runReplenishOnce(count int) (*replenishResult, error) {
	replenishMu.Lock()
	defer replenishMu.Unlock()

	rc := config.GetReplenishConfig()
	client, err := newSupplierClient(rc)
	if err != nil {
		return nil, err
	}

	if count <= 0 {
		count = rc.BatchCount
	}
	if count <= 0 {
		return nil, errors.New("purchase count must be positive")
	}

	// 用本轮可提取上限夹取请求量，避免必然失败的超量请求。stock 查询失败不致命，
	// 继续按请求量尝试，让供应商侧决定。
	if maxStock, serr := client.Stock(); serr == nil && maxStock >= 0 && count > maxStock {
		if maxStock == 0 {
			return nil, errors.New("supplier stock is 0; nothing to replenish")
		}
		logger.Infof("[Replenish] requested %d but supplier stock is %d; clamping", count, maxStock)
		count = maxStock
	}

	return h.purchaseAndImport(client, rc, count, newClientOrderID())
}

// purchaseAndImport 用指定订单号购买 count 个 Key 并导入账号池，返回结构化结果。
// 推送式补号（webhook）会传入供应商事件里的 purchase_order_id 作为订单号，
// 借助供应商侧幂等，webhook 重试不会重复扣费。调用方需自行持有 replenishMu。
func (h *Handler) purchaseAndImport(client *supplierClient, rc config.ReplenishConfig, count int, orderID string) (*replenishResult, error) {
	purchase, err := client.Purchase(count, orderID)
	if err != nil {
		return nil, err
	}

	res := &replenishResult{
		Purchased: purchase.Purchased,
		Remaining: purchase.Remaining,
		OrderID:   purchase.ClientOrderID,
	}

	// 收集出的 Key，交给既有的批量导入逻辑（去重 + 区域探测 + 信息刷新 + 池重载）。
	var keyLines []string
	for _, k := range purchase.Keys {
		if s := strings.TrimSpace(k.Key); s != "" {
			keyLines = append(keyLines, s)
		}
	}

	if len(keyLines) > 0 {
		summary := h.ImportApiKeys(strings.Join(keyLines, "\n"), rc.Region, "", "")
		res.Imported = summary.Imported
		res.Skipped = summary.Skipped
	}

	res.Summary = fmt.Sprintf("purchased=%d imported=%d skipped=%d remaining=%.2f",
		res.Purchased, res.Imported, res.Skipped, res.Remaining)
	return res, nil
}

// replenishState holds the background auto-replenish loop's lifecycle handle.
// restartReplenishLoop swaps it out under replenishLoopMu so config changes
// (enable/disable, interval) take effect without a process restart.
var (
	replenishLoopMu   sync.Mutex
	replenishLoopStop chan struct{}
)

// defaultReplenishInterval is used when IntervalSeconds is unset (<=0).
const defaultReplenishInterval = 5 * time.Minute

// startReplenishLoop launches the background auto-replenish loop if enabled.
// Called once from NewHandler; subsequent config changes go through
// restartReplenishLoop.
func (h *Handler) startReplenishLoop() {
	h.restartReplenishLoop()
}

// restartReplenishLoop stops any running loop and starts a fresh one when
// auto-replenish is enabled. Safe to call from request handlers.
func (h *Handler) restartReplenishLoop() {
	replenishLoopMu.Lock()
	defer replenishLoopMu.Unlock()

	// Stop the previous loop, if any.
	if replenishLoopStop != nil {
		close(replenishLoopStop)
		replenishLoopStop = nil
	}

	rc := config.GetReplenishConfig()
	if !rc.Enabled {
		return
	}

	stop := make(chan struct{})
	replenishLoopStop = stop
	go h.backgroundReplenish(stop)
}

// backgroundReplenish periodically checks the pool's available-account count and
// buys a fresh batch when it drops below MinPoolSize. The loop exits when stop is
// closed (config change / disable). Each tick re-reads config so策略调整即时生效.
func (h *Handler) backgroundReplenish(stop chan struct{}) {
	interval := defaultReplenishInterval
	if s := config.GetReplenishConfig().IntervalSeconds; s > 0 {
		interval = time.Duration(s) * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Infof("[Replenish] auto-replenish loop started (interval=%s)", interval)

	for {
		select {
		case <-stop:
			logger.Infof("[Replenish] auto-replenish loop stopped")
			return
		case <-ticker.C:
			h.maybeReplenish()
		}
	}
}

// maybeReplenish runs one low-water check: if the pool's available-account count
// is below MinPoolSize, buy a BatchCount batch and import it. All outcomes are
// persisted to the run state for the panel.
func (h *Handler) maybeReplenish() {
	rc := config.GetReplenishConfig()
	if !rc.Enabled {
		return
	}
	available := h.pool.AvailableCount()
	if rc.MinPoolSize <= 0 || available >= rc.MinPoolSize {
		return
	}

	logger.Infof("[Replenish] available=%d below minPoolSize=%d; replenishing %d",
		available, rc.MinPoolSize, rc.BatchCount)

	res, err := h.runReplenishOnce(rc.BatchCount)
	now := time.Now().Unix()
	if err != nil {
		_ = config.RecordReplenishRun(now, "", err.Error())
		logger.Warnf("[Replenish] auto run failed: %v", err)
		return
	}
	_ = config.RecordReplenishRun(now, res.Summary, "")
	logger.Infof("[Replenish] auto run: %s", res.Summary)
}

// supplierWebhookEvent 是供应商推送的 webhook 载荷（对接文档.md）。
// new_keys_available 携带 purchase_order_id，自动提取时必须原样作为
// client_order_id 提取该批 Key；all_keys_dead 仅作通知。
type supplierWebhookEvent struct {
	Event           string `json:"event"`
	EventID         string `json:"event_id"`
	PurchaseOrderID string `json:"purchase_order_id"`
	Message         string `json:"message"`
	NewKeys         int    `json:"new_keys"`
	Dead            int    `json:"dead"`
}

// handleReplenishWebhookEvent 处理一条供应商 webhook 事件：
//   - new_keys_available：用事件里的 purchase_order_id 作为订单号提取并导入这批 Key；
//     供应商侧幂等保证 webhook 重试不会重复扣费。
//   - all_keys_dead：仅记录，池会在下一轮低水位或后续事件时补充。
//
// 返回人类可读摘要写入运行态供面板展示。购买/导入错误会返回 error 但仍记录摘要。
func (h *Handler) handleReplenishWebhookEvent(ev supplierWebhookEvent) (string, error) {
	switch ev.Event {
	case "new_keys_available":
		count := ev.NewKeys
		if count <= 0 {
			return "", fmt.Errorf("new_keys_available with non-positive new_keys=%d", count)
		}
		orderID := strings.TrimSpace(ev.PurchaseOrderID)
		if orderID == "" {
			return "", errors.New("new_keys_available missing purchase_order_id")
		}

		rc := config.GetReplenishConfig()
		client, err := newSupplierClient(rc)
		if err != nil {
			return "", err
		}

		// 与手动/后台补号串行，避免并发购买。
		replenishMu.Lock()
		res, err := h.purchaseAndImport(client, rc, count, orderID)
		replenishMu.Unlock()
		if err != nil {
			return "", err
		}
		summary := fmt.Sprintf("webhook new_keys_available: %s", res.Summary)
		logger.Infof("[Replenish] %s", summary)
		return summary, nil

	case "all_keys_dead":
		summary := fmt.Sprintf("webhook all_keys_dead: %d 个 Key 已失效", ev.Dead)
		logger.Warnf("[Replenish] %s", summary)
		return summary, nil

	case "test", "webhook_test", "ping":
		// 供应商「测试推送」按钮发来的连通性探测，不触发购买，仅确认收到。
		summary := "webhook 连通性测试成功"
		if strings.TrimSpace(ev.Message) != "" {
			summary = "webhook 测试：" + strings.TrimSpace(ev.Message)
		}
		logger.Infof("[Replenish] %s", summary)
		return summary, nil

	default:
		return "", fmt.Errorf("unsupported webhook event %q", ev.Event)
	}
}
