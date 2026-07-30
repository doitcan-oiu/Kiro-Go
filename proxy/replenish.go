package proxy

// 在线补号（在线购买 Kiro API Key 并导入账号池）。
//
// 两家供应商并行工作，不是二选一：
//   - kirossClient（replenish_kiross.go，config.ReplenishProviderKiross）：
//     X-API-Key 头，/api/my/*，支持 client_order_id 幂等与 PUT /api/my/webhook 注册回调。
//   - kiroappioClient（replenish_kiroappio.go，config.ReplenishProviderKiroappio）：
//     Authorization: Bearer km_，/api/me/*，支持 client_order_id 幂等与 webhook 推送，
//     但回调地址只能在其站点后台手填。
//
// 为什么并行：单家的 Key 存活时间不可控，只靠一家可能出现「全死了才发现」的窗口。
// 两家同时补，任一家的 Key 被封时另一家仍在供货。因此：
//   - 轮询触发时遍历所有启用的供应商，各买一批；单家失败不影响另一家（见 replenishAll）。
//   - 推送触发时只买推送方（见 handleProviderWebhookEvent），数量取该家自己的
//     WebhookCount 配置，与另一家无关。
//
// 幂等：两家的 purchase 都必填 client_order_id（32 位十六进制）。推送场景优先复用
// 事件里的订单号（kiroappio 的 client_order_id 由「批次 + 收件人」确定性派生，
// kiross 的 purchase_order_id 在 Hook 重试时恒定），因此推送重试不会二次扣费。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

// supplierAccount 是各供应商账户信息的统一视图（面板「测试连接」用）。
// 各家字段覆盖度不同，用 Has* 标志位表明哪些字段有意义。
type supplierAccount struct {
	Name      string  // 账户名
	Quota     float64 // 总配额
	Remaining float64 // 剩余余额/积分
	UsedQuota float64 // 已用配额
	KeyPrice  float64 // 单价；阶梯定价时为最低价
	PriceMax  float64 // 阶梯定价的最高价；0 表示单一价（无区间）
	HasQuota  bool    // Quota/UsedQuota 是否有意义
	HasPrice  bool    // KeyPrice/PriceMax 是否有意义
}

// supplierClaimRequest 是一次提取请求的统一入参。
//
// ClientOrderID 是幂等键，由框架保证非空。两家都作为 client_order_id 上报。
// BatchOrderID 是可选的上游批次 id：kiroappio 用它只提取该批次产出的 Key；
// kiross 无此概念，忽略。
type supplierClaimRequest struct {
	Count         int
	ClientOrderID string
	BatchOrderID  string
}

// supplierClaim 是一次提取的统一结果。
//
// Purchased 是供应商自报的出 Key 数量（计费依据），可能与 len(Keys) 不同——例如
// 幂等重试命中已有订单时。<=0 表示供应商未提供该字段，调用方应回退到 len(Keys)。
//
// Spent 是本次实际扣费总额，仅阶梯定价的供应商提供；<=0 表示未提供。
type supplierClaim struct {
	Keys      []string
	Purchased int
	Remaining float64
	Spent     float64
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
type replenishSupplier interface {
	// Account 返回账户信息，用于面板「测试连接」。
	Account() (*supplierAccount, error)
	// Stock 返回本轮可提取上限。负数表示「未知/不限制」，调用方不应据此夹取。
	Stock() (int, error)
	// Claim 按 req 提取 Key。
	Claim(req supplierClaimRequest) (*supplierClaim, error)
	// ProviderName 返回供应商标识，用于日志与摘要。
	ProviderName() string
}

// newReplenishSupplier 按供应商标识构造对应的客户端。
// sc 是该家自己的配置（凭证缺失时构造失败），provider 未知时返回错误。
func newReplenishSupplier(provider string, sc config.SupplierConfig) (replenishSupplier, error) {
	switch config.NormalizeReplenishProvider(provider) {
	case config.ReplenishProviderKiross:
		return newKirossClient(sc)
	case config.ReplenishProviderKiroappio:
		return newKiroappioClient(sc)
	case config.ReplenishProviderKiroappcc:
		return newKiroappccClient(sc)
	default:
		return nil, fmt.Errorf("unknown replenish provider %q", provider)
	}
}

// webhookRegistrar 是「能通过 API 注册回调地址」这一可选能力。
//
// 只有部分供应商具备（kiross 有 PUT /api/my/webhook；kiroapp.io 只能在其站点后台
// 手填）。做成可选接口而非塞进 replenishSupplier，是为了让没有该能力的实现不必
// 提供一个只会返回错误的空方法。
type webhookRegistrar interface {
	SetWebhook(webhookURL string) error
}

// newEnabledReplenishSuppliers 为每家「已启用」的供应商构造客户端。
//
// 单家构造失败（例如密钥没填）不影响其余各家：失败原因记入 errs 由调用方展示，
// 能用的照常返回。一家都没有时返回错误，调用方据此提示用户先配置。
func newEnabledReplenishSuppliers(rc config.ReplenishConfig) ([]replenishSupplier, map[string]string, error) {
	providers := rc.EnabledProviders()
	if len(providers) == 0 {
		return nil, nil, errNoEnabledSupplier
	}

	clients := make([]replenishSupplier, 0, len(providers))
	errs := make(map[string]string)
	for _, p := range providers {
		client, err := newReplenishSupplier(p, rc.Supplier(p))
		if err != nil {
			errs[p] = err.Error()
			continue
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, errs, fmt.Errorf("no usable replenish supplier: %s", joinProviderErrors(errs))
	}
	return clients, errs, nil
}

// errNoEnabledSupplier 表示两家都没启用。区别于「启用了但配错了」，
// 让面板能给出「先启用一家」而不是「凭证无效」这类误导性提示。
var errNoEnabledSupplier = errors.New("no replenish supplier is enabled")

// joinProviderErrors 把各家的失败原因拼成稳定顺序的一行，便于日志与面板展示。
// 按 ReplenishProviders 的顺序输出，避免 map 遍历顺序随机导致摘要抖动。
func joinProviderErrors(errs map[string]string) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, p := range config.ReplenishProviders() {
		if msg, ok := errs[p]; ok {
			parts = append(parts, p+": "+msg)
		}
	}
	return strings.Join(parts, "; ")
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

// replenishResult 是单家供应商一次补号的结构化结果。
type replenishResult struct {
	Purchased int     `json:"purchased"`          // 供应商实际出 Key 数
	Imported  int     `json:"imported"`           // 成功导入账号池的数量
	Skipped   int     `json:"skipped"`            // 因重复被跳过的数量
	Remaining float64 `json:"remaining"`          // 供应商侧剩余余额
	Spent     float64 `json:"spent,omitempty"`    // 本次实际扣费（仅阶梯定价的供应商提供）
	OrderID   string  `json:"orderId,omitempty"`  // 本次订单号
	Provider  string  `json:"provider,omitempty"` // 本次使用的供应商
	Summary   string  `json:"summary,omitempty"`  // 人类可读摘要
}

// replenishBatchResult 是一次「所有启用的供应商各买一批」的汇总结果。
//
// 分开记录成功与失败，是因为两家相互独立：一家余额不足或接口故障时，另一家的
// 成交必须照常生效并如实回报，不能被整体判为失败。
type replenishBatchResult struct {
	Results   []*replenishResult `json:"results"`             // 各家成功的结果
	Errors    map[string]string  `json:"errors,omitempty"`    // 供应商标识 -> 失败原因
	Purchased int                `json:"purchased"`           // 合计出 Key 数
	Imported  int                `json:"imported"`            // 合计导入数
	Skipped   int                `json:"skipped"`             // 合计跳过数
	Summary   string             `json:"summary,omitempty"`   // 人类可读汇总
	Attempted int                `json:"attempted,omitempty"` // 本轮尝试的供应商家数
}

// ok 报告本轮是否至少有一家成功出货。
func (r *replenishBatchResult) ok() bool { return len(r.Results) > 0 }

// ErrorText 把各家的失败原因拼成一行，供写入运行态。全部成功时返回空串。
//
// 部分成功也会返回非空：一家挂了必须留下痕迹，否则面板只显示另一家的成功摘要，
// 用户不会察觉有一路已经停止供货。
func (r *replenishBatchResult) ErrorText() string {
	if len(r.Errors) == 0 {
		return ""
	}
	// 按供应商标识排序，保证摘要稳定（map 遍历顺序随机）。
	providers := make([]string, 0, len(r.Errors))
	for p := range r.Errors {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	parts := make([]string, 0, len(providers))
	for _, p := range providers {
		parts = append(parts, fmt.Sprintf("%s: %s", p, r.Errors[p]))
	}
	return strings.Join(parts, "; ")
}

// SupplierPayload 投影逐家结果供前端展示：成功的带数量，失败的带原因。
func (r *replenishBatchResult) SupplierPayload() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(r.Results)+len(r.Errors))
	for _, res := range r.Results {
		item := map[string]interface{}{
			"provider":  res.Provider,
			"ok":        true,
			"purchased": res.Purchased,
			"imported":  res.Imported,
			"skipped":   res.Skipped,
			"remaining": res.Remaining,
		}
		if res.Spent > 0 {
			item["spent"] = res.Spent
		}
		if res.OrderID != "" {
			item["orderId"] = res.OrderID
		}
		out = append(out, item)
	}
	// 失败项同样按标识排序，避免每次刷新顺序跳动。
	providers := make([]string, 0, len(r.Errors))
	for p := range r.Errors {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		out = append(out, map[string]interface{}{
			"provider": p,
			"ok":       false,
			"error":    r.Errors[p],
		})
	}
	return out
}

// replenishMu 串行化补号运行，避免手动触发、后台轮询与推送并发购买。
var replenishMu sync.Mutex

// runReplenishOnce 手动补号一次：所有启用的供应商各提取 count 个 Key。
// count <= 0 时使用配置的 BatchCount。
func (h *Handler) runReplenishOnce(count int) (*replenishBatchResult, error) {
	replenishMu.Lock()
	defer replenishMu.Unlock()
	return h.replenishAll(count)
}

// replenishAll 让每个启用的供应商各买一批，返回汇总结果。调用方需持有 replenishMu。
//
// 逐家独立处理：任一家构造失败/库存为 0/下单失败都只记进 Errors，继续处理下一家。
// 只有「没有任何启用的供应商」才返回 error——那是配置问题，不是上游故障。
func (h *Handler) replenishAll(count int) (*replenishBatchResult, error) {
	rc := config.GetReplenishConfig()
	enabled := rc.EnabledProviders()
	if len(enabled) == 0 {
		return nil, errors.New("no replenish supplier is enabled")
	}

	if count <= 0 {
		count = rc.BatchCount
	}
	if count <= 0 {
		return nil, errors.New("purchase count must be positive")
	}

	batch := &replenishBatchResult{Errors: map[string]string{}, Attempted: len(enabled)}
	for _, provider := range enabled {
		res, err := h.replenishOneProvider(provider, rc, supplierClaimRequest{Count: count})
		if err != nil {
			batch.Errors[provider] = err.Error()
			logger.Warnf("[Replenish] provider=%s failed: %v", provider, err)
			continue
		}
		batch.Results = append(batch.Results, res)
		batch.Purchased += res.Purchased
		batch.Imported += res.Imported
		batch.Skipped += res.Skipped
	}

	batch.Summary = summarizeBatch(batch)
	return batch, nil
}

// summarizeBatch 拼出一行人类可读摘要，成功与失败都列出，供面板与日志展示。
func summarizeBatch(batch *replenishBatchResult) string {
	parts := make([]string, 0, len(batch.Results)+len(batch.Errors))
	for _, r := range batch.Results {
		parts = append(parts, fmt.Sprintf("%s: purchased=%d imported=%d skipped=%d",
			r.Provider, r.Purchased, r.Imported, r.Skipped))
	}
	// 失败按供应商固定顺序输出，避免 map 随机序让摘要每次都变样。
	for _, p := range config.ReplenishProviders() {
		if msg, bad := batch.Errors[p]; bad {
			parts = append(parts, fmt.Sprintf("%s: FAILED (%s)", p, msg))
		}
	}
	return fmt.Sprintf("total purchased=%d imported=%d skipped=%d | %s",
		batch.Purchased, batch.Imported, batch.Skipped, strings.Join(parts, "; "))
}

// replenishOneProvider 从单家供应商提取并导入，返回该家的结果。
// 调用方需持有 replenishMu。
func (h *Handler) replenishOneProvider(provider string, rc config.ReplenishConfig, req supplierClaimRequest) (*replenishResult, error) {
	client, err := newReplenishSupplier(provider, rc.Supplier(provider))
	if err != nil {
		return nil, err
	}

	// 用本轮可提取上限夹取请求量，避免必然失败的超量请求。stock 查询失败不致命，
	// 继续按请求量尝试，让供应商侧决定；负数表示上限未知，不夹取。
	if maxStock, serr := client.Stock(); serr == nil && maxStock >= 0 && req.Count > maxStock {
		if maxStock == 0 {
			return nil, errors.New("supplier stock is 0; nothing to replenish")
		}
		logger.Infof("[Replenish] provider=%s requested %d but stock is %d; clamping",
			provider, req.Count, maxStock)
		req.Count = maxStock
	}

	return h.claimAndImport(client, rc, req)
}

// claimAndImport 按 req 从供应商提取 Key 并导入账号池，返回结构化结果。
// ClientOrderID 为空时补一个新订单号，保证幂等键始终存在。
// 调用方需自行持有 replenishMu。
func (h *Handler) claimAndImport(client replenishSupplier, rc config.ReplenishConfig, req supplierClaimRequest) (*replenishResult, error) {
	if strings.TrimSpace(req.ClientOrderID) == "" {
		req.ClientOrderID = newClientOrderID()
	}
	claim, err := client.Claim(req)
	if err != nil {
		return nil, err
	}

	res := &replenishResult{
		// Purchased 取供应商自报的出 Key 数，缺失时回退到实际 Key 条数。
		Purchased: claim.PurchasedCount(),
		Remaining: claim.Remaining,
		Spent:     claim.Spent,
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
	// 阶梯定价的供应商自报本单扣费，附到摘要里让用户看到真实花费。
	if claim.Spent > 0 {
		res.Summary += fmt.Sprintf(" spent=%.2f", claim.Spent)
	}
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
// 触发后所有启用的供应商各买一批（见 replenishAll）：只补一家的话，那家的 Key 被
// 封时池子会再次见底，两家同时补才能保证始终有存活的 Key。
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

	providers := rc.EnabledProviders()
	logger.Infof("[Replenish] %s; replenishing %d from each of %d provider(s): %s",
		reason, count, len(providers), strings.Join(providers, ","))

	batch, err := h.runReplenishOnce(count)
	now := time.Now().Unix()
	if err != nil {
		_ = config.RecordReplenishRun(now, "", err.Error())
		logger.Warnf("[Replenish] auto run failed (%s): %v", reason, err)
		return
	}
	// 部分失败也要留痕：摘要里已含各家成败，errMsg 只在全军覆没时填，
	// 避免面板把「一家成交一家故障」显示成整体失败。
	errMsg := ""
	if !batch.ok() {
		errMsg = "all providers failed"
	}
	_ = config.RecordReplenishRun(now, batch.Summary, errMsg)
	logger.Infof("[Replenish] auto run (%s): %s", reason, batch.Summary)
}

// replenishTrigger decides whether a polling tick should replenish, returning the
// number of keys to claim and a human-readable reason. Returns 0 when no trigger
// fires. Split out from maybeReplenish so the trigger policy is unit-testable
// without an HTTP round-trip or a live pool.
//
// 返回值是「每家」买多少，不是总量：两家并行，各买这么多。
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

// supplierWebhookEvent 是供应商推送的 webhook 载荷，兼容两家的字段命名：
//
//   - kiross（kiro.ss.txt）：幂等键在 purchase_order_id，无批次概念。
//   - kiroappio：幂等键在 client_order_id（由「批次 + 收件人」派生，重试恒定），
//     另有 order_id 表示开号批次，带上它只提取该批次产出的 Key。
//
// 两家的幂等键字段名不同但语义一致，由 idempotencyKey 归一。
type supplierWebhookEvent struct {
	Event   string `json:"event"`
	EventID string `json:"event_id"`
	// kiross 的幂等订单号。
	PurchaseOrderID string `json:"purchase_order_id"`
	// kiroappio 的幂等键与开号批次 id。
	ClientOrderID string `json:"client_order_id"`
	OrderID       string `json:"order_id"`
	Message       string `json:"message"`
	// NewKeys 是本批可提取数量。用指针以区分「字段缺失」与「显式 0」：
	// kiroapp.cc 的推送不含该字段，缺失时按配置量下单；显式 0 表示本批无 Key。
	NewKeys *int `json:"new_keys"`
	Dead    int  `json:"dead"`
}

// idempotencyKey 返回本次推送的幂等键，兼容两家供应商的字段名。
func (ev supplierWebhookEvent) idempotencyKey() string {
	if s := strings.TrimSpace(ev.ClientOrderID); s != "" {
		return s
	}
	return strings.TrimSpace(ev.PurchaseOrderID)
}

// webhookEventKind 是各家事件名归一化后的类别。
//
// 各供应商的事件命名并不统一（kiross/kiroapp.io 用 new_keys_available，
// kiroapp.cc 用 stock），语义却一致，因此先归一再分派，避免把分派逻辑写成
// 一长串各家字面量的 case。
type webhookEventKind int

const (
	webhookEventUnknown webhookEventKind = iota
	webhookEventNewKeys                  // 有新 Key/库存可提取
	webhookEventAllDead                  // 本轮 Key 全部失效
	webhookEventRevoked                  // 上游吊销 Key
	webhookEventProbe                    // 连通性测试
)

// classifyWebhookEvent 把供应商推送的事件名归一化为类别。
//
// 对没有幂等键的供应商（kiroapp.cc）采取「非探测即到货」：它的推送语义只有
// 「有新库存」一种，文档也未列出事件名清单，若按白名单匹配就会像 "stock" 那样
// 被拒而错过补号。这类家的下单量取本地配置，不依赖载荷字段，因此把未知事件当
// 到货是安全的——最坏情况是按配置量买一次，而不是漏补。
//
// 反之，支持幂等的两家事件名有明确文档，保持严格匹配：未知事件宁可报错留痕，
// 也不要拿一个语义不明的推送去下单。
func classifyWebhookEvent(provider, event string) webhookEventKind {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "new_keys_available", "stock", "new_stock", "stock_available", "new_keys":
		return webhookEventNewKeys
	case "all_keys_dead":
		return webhookEventAllDead
	case "key_revoked_abuse":
		return webhookEventRevoked
	case "test", "webhook_test", "ping":
		return webhookEventProbe
	}
	if !providerHasIdempotency(provider) {
		return webhookEventNewKeys
	}
	return webhookEventUnknown
}

// providerHasIdempotency 报告该供应商的提取接口是否支持幂等订单号。
// 决定了未知事件能否安全地当作「到货」处理，以及失败后能否重试。
func providerHasIdempotency(provider string) bool {
	switch config.NormalizeReplenishProvider(provider) {
	case config.ReplenishProviderKiross, config.ReplenishProviderKiroappio:
		return true
	default:
		return false
	}
}

// handleProviderWebhookEvent 处理来自 provider 的一条 webhook 事件。
//
// provider 由回调路径确定（每家一个专属 secret），因此推送方是可信的、无需从载荷里猜。
// 关键语义：只向推送方下单，数量取该家自己的 WebhookCount 配置。另一家不受影响——
// 它有自己的推送和自己的数量设置。
//
// 事件分派：
//   - new_keys_available：按该家配置的数量提取并导入。复用事件里的幂等键，
//     使推送重试/重复推送不会二次扣费。
//   - all_keys_dead：仅记录。真正的「号全死了」补号由后台轮询按本地凭证状态触发
//     （见 maybeReplenish），比信任上游事件更可靠，也避免与轮询重复购买。
//   - key_revoked_abuse：仅记录告警，Key 已被上游吊销。
//   - test/ping：连通性探测，不下单。
//
// 返回人类可读摘要写入该家的运行态供面板展示。
func (h *Handler) handleProviderWebhookEvent(provider string, ev supplierWebhookEvent) (string, error) {
	switch classifyWebhookEvent(provider, ev.Event) {
	case webhookEventNewKeys:
		rc := config.GetReplenishConfig()
		sc := rc.Supplier(provider)
		// 该家被停用：不下单。回调地址可能还留在供应商后台，停用就该真的不再花钱。
		if !sc.Enabled {
			summary := fmt.Sprintf("webhook new_keys_available 已忽略：供应商 %s 当前已停用", provider)
			logger.Infof("[Replenish] %s", summary)
			return summary, nil
		}

		// 下单量取该家自己配置的数量（各家可配不同值）。
		count := rc.EffectiveWebhookCount(provider)
		if count <= 0 {
			return "", fmt.Errorf("provider %s has no webhook purchase count configured", provider)
		}

		// new_keys 是可选字段：带了就当作本批可提取上限并据此夹取（超量请求必然失败）；
		// 没带（kiroapp.cc 只通知「有货了」，不报数量）就按配置量直接下单。
		// 显式的 0 视为「本批没有 Key」，按各家文档都不应再调取号接口。
		if ev.NewKeys != nil {
			available := *ev.NewKeys
			if available <= 0 {
				return "", fmt.Errorf("new_keys_available with non-positive new_keys=%d", available)
			}
			if count > available {
				logger.Infof("[Replenish] provider=%s webhookCount=%d clamped to new_keys=%d",
					provider, count, available)
				count = available
			}
		}

		req := supplierClaimRequest{
			Count: count,
			// 复用上游备好的幂等键；缺失时 claimAndImport 会补一个新的。
			ClientOrderID: ev.idempotencyKey(),
			BatchOrderID:  strings.TrimSpace(ev.OrderID),
		}

		// 与手动/后台补号串行，避免并发购买。
		replenishMu.Lock()
		res, err := h.replenishOneProvider(provider, rc, req)
		replenishMu.Unlock()
		if err != nil {
			return "", err
		}
		summary := fmt.Sprintf("webhook new_keys_available: %s", res.Summary)
		logger.Infof("[Replenish] %s", summary)
		return summary, nil

	case webhookEventAllDead:
		summary := fmt.Sprintf("webhook all_keys_dead (%s): %d 个 Key 已失效", provider, ev.Dead)
		logger.Warnf("[Replenish] %s", summary)
		return summary, nil

	case webhookEventRevoked:
		// 上游因滥用吊销了 Key，只记录告警；本地失效由请求侧的封禁检测处理。
		summary := fmt.Sprintf("webhook key_revoked_abuse (%s): 上游已吊销 Key（疑似滥用）", provider)
		if m := strings.TrimSpace(ev.Message); m != "" {
			summary = fmt.Sprintf("webhook key_revoked_abuse (%s): %s", provider, m)
		}
		logger.Warnf("[Replenish] %s", summary)
		return summary, nil

	case webhookEventProbe:
		// 供应商「测试推送」按钮发来的连通性探测，不触发购买，仅确认收到。
		summary := fmt.Sprintf("webhook 连通性测试成功（%s）", provider)
		if strings.TrimSpace(ev.Message) != "" {
			summary = fmt.Sprintf("webhook 测试（%s）：%s", provider, strings.TrimSpace(ev.Message))
		}
		logger.Infof("[Replenish] %s", summary)
		return summary, nil

	default:
		// 记下原始事件名，便于对接新供应商时按实际载荷补映射，而不是靠猜。
		return "", fmt.Errorf("unsupported webhook event %q from %s", ev.Event, provider)
	}
}
