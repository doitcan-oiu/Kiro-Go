package proxy

// 在线补号（在线购买 Kiro API Key 并导入账号池）。
//
// 本文件实现两层：
//  1. replenishSupplier —— 供应商抽象。两个实现：
//     - supplierClient（本文件，config.ReplenishProviderVendor，对接文档.md）：
//     X-API-Key 头，/api/my/profile、/api/my/stock、/api/my/purchase，
//     支持 client_order_id 幂等与 webhook 推送。
//     - kiroappClient（replenish_kiroapp.go，config.ReplenishProviderKiroapp）：
//     Authorization: Bearer，/openapi/balance、/openapi/stock、/openapi/claim，
//     无幂等订单号、无 webhook，只能靠轮询。
//     由 newReplenishSupplier 按 config 里的 Provider 选择。
//  2. Handler 上的补号编排 —— runReplenishOnce 购买一批 Key 并复用既有的
//     ImportApiKeys 导入到账号池；backgroundReplenish 周期性检查低水位与
//     「全部凭证禁用」两种触发条件。
//
// 幂等：vendor 的 purchase 必填 client_order_id（32 位十六进制）。同一订单号+相同
// count 重试返回首次结果且不重复扣费，因此调用方失败重试务必复用同一订单号。
// kiroapp 无此机制，重试会重复扣费，故其失败不做自动重试。

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

// supplierAccount 是各供应商账户信息的统一视图（面板「测试连接」用）。
// 不同供应商字段覆盖度不同：kiroapp 只有余额，没有名称/配额/已用量。
type supplierAccount struct {
	Name      string  // 账户名，kiroapp 无此字段
	Quota     float64 // 总配额，kiroapp 无此字段
	Remaining float64 // 剩余余额/积分
	UsedQuota float64 // 已用配额，kiroapp 无此字段
	KeyPrice  float64 // 单价，仅 kiroapp 提供
	HasQuota  bool    // Quota/UsedQuota 是否有意义
	HasPrice  bool    // KeyPrice 是否有意义
}

// supplierClaim 是一次提取的统一结果。OrderID 仅 vendor 有（幂等订单号）。
//
// Purchased 是供应商自报的出 Key 数量（计费依据），可能与 len(Keys) 不同——例如
// 幂等重试命中已有订单时。<=0 表示供应商未提供该字段，调用方应回退到 len(Keys)。
type supplierClaim struct {
	Keys      []string
	Purchased int
	Remaining float64
	OrderID   string
}

// PurchasedCount 返回本次实际出 Key 数量，优先用供应商自报值。
func (c *supplierClaim) PurchasedCount() int {
	if c.Purchased > 0 {
		return c.Purchased
	}
	return len(c.Keys)
}

// replenishSupplier 抽象「提取 Key」这一上游能力，让补号编排与具体供应商解耦。
//
// orderID 语义：支持幂等的供应商（vendor）用它作为 client_order_id，失败重试复用
// 同一值可避免重复扣费；不支持的实现（kiroapp）忽略该参数。
type replenishSupplier interface {
	// Account 返回账户信息，用于面板「测试连接」。
	Account() (*supplierAccount, error)
	// Stock 返回本轮可提取上限。负数表示「未知/不限制」，调用方不应据此夹取。
	Stock() (int, error)
	// Claim 提取 count 个 Key。
	Claim(count int, orderID string) (*supplierClaim, error)
	// ProviderName 返回供应商标识，用于日志与摘要。
	ProviderName() string
}

// newReplenishSupplier 按配置里的 Provider 构造对应的供应商客户端。
func newReplenishSupplier(rc config.ReplenishConfig) (replenishSupplier, error) {
	switch rc.EffectiveProvider() {
	case config.ReplenishProviderKiroapp:
		return newKiroappClient(rc)
	default:
		return newSupplierClient(rc)
	}
}

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

func (c *supplierClient) ProviderName() string { return config.ReplenishProviderVendor }

// Account 实现 replenishSupplier，包装 Profile。
func (c *supplierClient) Account() (*supplierAccount, error) {
	p, err := c.Profile()
	if err != nil {
		return nil, err
	}
	return &supplierAccount{
		Name:      p.Name,
		Quota:     p.Quota,
		Remaining: p.Remaining,
		UsedQuota: p.UsedQuota,
		HasQuota:  true,
	}, nil
}

// Claim 实现 replenishSupplier，包装带幂等订单号的 Purchase。
func (c *supplierClient) Claim(count int, orderID string) (*supplierClaim, error) {
	if strings.TrimSpace(orderID) == "" {
		orderID = newClientOrderID()
	}
	r, err := c.Purchase(count, orderID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(r.Keys))
	for _, k := range r.Keys {
		if s := strings.TrimSpace(k.Key); s != "" {
			keys = append(keys, s)
		}
	}
	return &supplierClaim{
		Keys:      keys,
		Purchased: r.Purchased,
		Remaining: r.Remaining,
		OrderID:   r.ClientOrderID,
	}, nil
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
	Purchased int     `json:"purchased"`          // 供应商实际出 Key 数
	Imported  int     `json:"imported"`           // 成功导入账号池的数量
	Skipped   int     `json:"skipped"`            // 因重复被跳过的数量
	Remaining float64 `json:"remaining"`          // 供应商侧剩余余额
	OrderID   string  `json:"orderId,omitempty"`  // 本次订单号（仅支持幂等的供应商有）
	Provider  string  `json:"provider,omitempty"` // 本次使用的供应商
	Summary   string  `json:"summary,omitempty"`  // 人类可读摘要
}

// replenishMu 串行化补号运行，避免手动触发与后台循环并发购买。
var replenishMu sync.Mutex

// runReplenishOnce 执行一次补号：从当前配置的供应商提取 count 个 Key 并导入账号池。
// count <= 0 时使用配置的 BatchCount。提取前会用供应商的库存接口夹取可提取上限。
func (h *Handler) runReplenishOnce(count int) (*replenishResult, error) {
	replenishMu.Lock()
	defer replenishMu.Unlock()
	return h.replenishLocked(count, "")
}

// replenishLocked 是 runReplenishOnce 的内部实现，调用方需自行持有 replenishMu。
// orderID 为空时对支持幂等的供应商生成一个新订单号。
func (h *Handler) replenishLocked(count int, orderID string) (*replenishResult, error) {
	rc := config.GetReplenishConfig()
	client, err := newReplenishSupplier(rc)
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
	// 继续按请求量尝试，让供应商侧决定；负数表示上限未知，不夹取。
	if maxStock, serr := client.Stock(); serr == nil && maxStock >= 0 && count > maxStock {
		if maxStock == 0 {
			return nil, errors.New("supplier stock is 0; nothing to replenish")
		}
		logger.Infof("[Replenish] requested %d but supplier stock is %d; clamping", count, maxStock)
		count = maxStock
	}

	return h.claimAndImport(client, rc, count, orderID)
}

// claimAndImport 从供应商提取 count 个 Key 并导入账号池，返回结构化结果。
// orderID 只对支持幂等的供应商有意义：推送式补号（webhook）传入事件里的
// purchase_order_id，借助供应商侧幂等使 webhook 重试不会重复扣费。
// 调用方需自行持有 replenishMu。
func (h *Handler) claimAndImport(client replenishSupplier, rc config.ReplenishConfig, count int, orderID string) (*replenishResult, error) {
	claim, err := client.Claim(count, orderID)
	if err != nil {
		return nil, err
	}

	// Purchased 取供应商自报的出 Key 数（vendor 提供），缺失时回退到实际 Key 条数。
	purchased := claim.Purchased
	if purchased <= 0 {
		purchased = len(claim.Keys)
	}
	res := &replenishResult{
		Purchased: claim.PurchasedCount(),
		Remaining: claim.Remaining,
		OrderID:   claim.OrderID,
		Provider:  client.ProviderName(),
	}

	// 交给既有的批量导入逻辑（去重 + 区域探测 + 信息刷新 + 池重载）。
	if len(claim.Keys) > 0 {
		summary := h.ImportApiKeys(strings.Join(claim.Keys, "\n"), rc.Region, "", "")
		res.Imported = summary.Imported
		res.Skipped = summary.Skipped
	}

	res.Summary = fmt.Sprintf("provider=%s purchased=%d imported=%d skipped=%d remaining=%.2f",
		res.Provider, res.Purchased, res.Imported, res.Skipped, res.Remaining)
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

// maybeReplenish runs one polling check and replenishes if any trigger fires.
// Two independent triggers, evaluated in this order:
//
//  1. All credentials disabled (AllDeadReplenish): every stored credential is
//     disabled/banned with none waiting to auto-restore. Buys EffectiveAllDeadCount.
//     Checked first because it is the more urgent condition — the proxy is fully
//     down, not merely running thin.
//  2. Low water mark (MinPoolSize): available accounts below the threshold.
//     Buys BatchCount.
//
// All outcomes are persisted to the run state for the panel.
func (h *Handler) maybeReplenish() {
	rc := config.GetReplenishConfig()
	if !rc.Enabled {
		return
	}

	count, reason := replenishTrigger(rc, h.pool.AvailableCount(), config.GetCredentialHealth())
	if count <= 0 {
		return
	}

	logger.Infof("[Replenish] %s; replenishing %d from provider=%s",
		reason, count, rc.EffectiveProvider())

	res, err := h.runReplenishOnce(count)
	now := time.Now().Unix()
	if err != nil {
		_ = config.RecordReplenishRun(now, "", err.Error())
		logger.Warnf("[Replenish] auto run failed (%s): %v", reason, err)
		return
	}
	_ = config.RecordReplenishRun(now, res.Summary, "")
	logger.Infof("[Replenish] auto run (%s): %s", reason, res.Summary)
}

// replenishTrigger decides whether a polling tick should replenish, returning the
// number of keys to claim and a human-readable reason. Returns 0 when no trigger
// fires. Split out from maybeReplenish so the trigger policy is unit-testable
// without an HTTP round-trip or a live pool.
func replenishTrigger(rc config.ReplenishConfig, available int, health config.CredentialHealth) (int, string) {
	// Trigger 1: every credential in the system is disabled/banned.
	// Deliberately keyed on persisted enabled/ban state rather than the pool's
	// AvailableCount, which also reads 0 when every account is merely in a
	// short cooldown (e.g. a 429 storm) — buying on that would over-purchase.
	if rc.AllDeadReplenish && health.AllDisabled() {
		if n := rc.EffectiveAllDeadCount(); n > 0 {
			return n, fmt.Sprintf("all %d credentials disabled", health.Total)
		}
	}

	// Trigger 2: low water mark.
	if rc.MinPoolSize > 0 && available < rc.MinPoolSize {
		if rc.BatchCount > 0 {
			return rc.BatchCount, fmt.Sprintf("available=%d below minPoolSize=%d", available, rc.MinPoolSize)
		}
	}

	return 0, ""
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
//   - all_keys_dead：仅记录。真正的「号全死了」补号由后台轮询按本地凭证状态触发
//     （见 maybeReplenish），比信任上游事件更可靠，也避免与轮询重复购买。
//
// 只有 vendor 供应商有 webhook。若当前选择的是其它供应商，推送不会触发购买——
// 否则切换供应商后，旧供应商仍注册着回调就会在用户不知情的情况下继续扣费。
//
// 返回人类可读摘要写入运行态供面板展示。购买/导入错误会返回 error 但仍记录摘要。
func (h *Handler) handleReplenishWebhookEvent(ev supplierWebhookEvent) (string, error) {
	switch ev.Event {
	case "new_keys_available":
		available := ev.NewKeys
		if available <= 0 {
			return "", fmt.Errorf("new_keys_available with non-positive new_keys=%d", available)
		}
		orderID := strings.TrimSpace(ev.PurchaseOrderID)
		if orderID == "" {
			return "", errors.New("new_keys_available missing purchase_order_id")
		}

		rc := config.GetReplenishConfig()
		// 当前供应商不是 vendor：忽略推送，不动用 vendor 余额。
		if !rc.SupportsWebhook() {
			summary := fmt.Sprintf("webhook new_keys_available 已忽略：当前供应商为 %s，未启用推送式补号",
				rc.EffectiveProvider())
			logger.Infof("[Replenish] %s", summary)
			return summary, nil
		}
		client, err := newSupplierClient(rc)
		if err != nil {
			return "", err
		}

		// new_keys 是「可提取上限」而非必须全取。若配置了单次上限，则夹取到该值，
		// 让用户无需改动供应商侧即可控制每轮实际提取数量。<=0 表示不限制。
		count := available
		if rc.WebhookMaxCount > 0 && count > rc.WebhookMaxCount {
			logger.Infof("[Replenish] webhook new_keys=%d clamped to webhookMaxCount=%d",
				available, rc.WebhookMaxCount)
			count = rc.WebhookMaxCount
		}

		// 与手动/后台补号串行，避免并发购买。
		replenishMu.Lock()
		res, err := h.claimAndImport(client, rc, count, orderID)
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
