package proxy

// 在线补号（在线购买 Kiro API Key 并导入账号池）的框架层。
//
// 目前没有任何供应商实现：原先对接的三家（default / kiroapp.cc / kiroapp.io）已整体
// 移除，newReplenishSupplier 因此一律返回 errNoReplenishSupplier。手动补号、后台
// 轮询与 webhook 推送都会以「无可用供应商」失败，并把原因记录到运行态供面板展示。
//
// 保留下来的是与具体供应商无关的框架：
//  1. replenishSupplier 抽象与统一的入参/结果 DTO（supplierClaimRequest /
//     supplierClaim / supplierAccount）。新增供应商只需实现该接口，并在
//     newReplenishSupplier 里挂上，其余编排无需改动。
//  2. Handler 上的补号编排 —— runReplenishOnce 提取一批 Key 并复用既有的
//     ImportApiKeys 导入到账号池；backgroundReplenish 周期性检查低水位与
//     「全部凭证禁用」两种触发条件；handleReplenishWebhookEvent 处理入站推送。
//
// 幂等约定：框架统一为每次提取生成 32 位十六进制的 ClientOrderID（见
// newClientOrderID），供应商实现无需自己造。支持幂等的上游应原样作为
// client_order_id 上报，失败重试复用同一值即可避免重复扣费；不支持的上游忽略它，
// 但其失败不应自动重试。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
// ClientOrderID 是幂等键，由框架生成保证非空。支持幂等的供应商应把它作为
// client_order_id 上报；不支持的实现忽略即可。
// BatchOrderID 是可选的上游批次 id：webhook 推送里带批次概念的供应商可用它只提取
// 该批次产出的 Key，无此概念的实现忽略。
type supplierClaimRequest struct {
	Count         int
	ClientOrderID string
	BatchOrderID  string
}

// supplierClaim 是一次提取的统一结果。OrderID 仅支持幂等的供应商有（订单号）。
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

// errNoReplenishSupplier 表示当前没有任何可用的补号供应商。
//
// 三家原有供应商已整体移除，框架保留但无实现。所有补号入口（手动、轮询、webhook）
// 都会拿到这个错误，因此面板上的失败原因是明确的「没有供应商」，而不是配置校验
// 报错那类容易被误读为「填错了」的提示。
var errNoReplenishSupplier = errors.New("no replenish supplier is available: all suppliers have been removed")

// newReplenishSupplier 构造当前配置对应的供应商客户端。
//
// 目前恒定返回 errNoReplenishSupplier。新增供应商时在此按
// rc.EffectiveProvider() 分支返回对应实现即可，调用方无需改动。
func newReplenishSupplier(rc config.ReplenishConfig) (replenishSupplier, error) {
	return nil, errNoReplenishSupplier
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
	Spent     float64 `json:"spent,omitempty"`    // 本次实际扣费（仅阶梯定价的供应商提供）
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
	return h.replenishLocked(supplierClaimRequest{Count: count})
}

// replenishLocked 是 runReplenishOnce 的内部实现，调用方需自行持有 replenishMu。
// req.ClientOrderID 为空时，支持幂等的供应商会自行生成一个新订单号。
func (h *Handler) replenishLocked(req supplierClaimRequest) (*replenishResult, error) {
	rc := config.GetReplenishConfig()
	client, err := newReplenishSupplier(rc)
	if err != nil {
		return nil, err
	}

	if req.Count <= 0 {
		req.Count = rc.BatchCount
	}
	if req.Count <= 0 {
		return nil, errors.New("purchase count must be positive")
	}

	// 用本轮可提取上限夹取请求量，避免必然失败的超量请求。stock 查询失败不致命，
	// 继续按请求量尝试，让供应商侧决定；负数表示上限未知，不夹取。
	if maxStock, serr := client.Stock(); serr == nil && maxStock >= 0 && req.Count > maxStock {
		if maxStock == 0 {
			return nil, errors.New("supplier stock is 0; nothing to replenish")
		}
		logger.Infof("[Replenish] requested %d but supplier stock is %d; clamping", req.Count, maxStock)
		req.Count = maxStock
	}

	return h.claimAndImport(client, rc, req)
}

// claimAndImport 按 req 从供应商提取 Key 并导入账号池，返回结构化结果。
// req.ClientOrderID 只对支持幂等的供应商有意义：推送式补号（webhook）传入事件里的
// 订单号，借助供应商侧幂等使 webhook 重试不会重复扣费。
// 调用方需自行持有 replenishMu。
func (h *Handler) claimAndImport(client replenishSupplier, rc config.ReplenishConfig, req supplierClaimRequest) (*replenishResult, error) {
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
	// 阶梯定价的供应商（kiroappio）自报本单扣费，附到摘要里让用户看到真实花费。
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

// supplierWebhookEvent 是供应商推送的 webhook 载荷，兼容两家支持推送的供应商：
//
//   - vendor（对接文档.md）：幂等键在 purchase_order_id。
//   - kiroappio：幂等键在 client_order_id（由「批次 + 收件人」派生，重试恒定），
//     另有 order_id 表示开号批次，带上它只提取该批次产出的 Key。
//
// 两家的幂等键字段名不同但语义一致，由 idempotencyKey 归一。
type supplierWebhookEvent struct {
	Event   string `json:"event"`
	EventID string `json:"event_id"`
	// vendor 的幂等订单号。
	PurchaseOrderID string `json:"purchase_order_id"`
	// kiroappio 的幂等键与开号批次 id。
	ClientOrderID string `json:"client_order_id"`
	OrderID       string `json:"order_id"`
	Message       string `json:"message"`
	NewKeys       int    `json:"new_keys"`
	Dead          int    `json:"dead"`
}

// idempotencyKey 返回本次推送的幂等键，兼容两家供应商的字段名。
func (ev supplierWebhookEvent) idempotencyKey() string {
	if s := strings.TrimSpace(ev.ClientOrderID); s != "" {
		return s
	}
	return strings.TrimSpace(ev.PurchaseOrderID)
}

// handleReplenishWebhookEvent 处理一条供应商 webhook 事件：
//   - new_keys_available：用事件里的幂等键作为订单号提取并导入这批 Key；
//     供应商侧幂等保证 webhook 重试不会重复扣费。kiroappio 还会带上 order_id，
//     只提取该批次产出的 Key。
//   - all_keys_dead：仅记录。真正的「号全死了」补号由后台轮询按本地凭证状态触发
//     （见 maybeReplenish），比信任上游事件更可靠，也避免与轮询重复购买。
//   - key_revoked_abuse（kiroappio）：仅记录告警，Key 已被上游吊销。
//
// 只有支持推送的供应商会触发购买。若当前选择的供应商不支持推送，事件只记录不购买——
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
		orderID := ev.idempotencyKey()
		if orderID == "" {
			return "", errors.New("new_keys_available missing client_order_id/purchase_order_id")
		}

		rc := config.GetReplenishConfig()
		// 当前供应商不支持推送：忽略事件，不动用任何供应商的余额。
		if !rc.SupportsWebhook() {
			summary := fmt.Sprintf("webhook new_keys_available 已忽略：当前供应商为 %s，未启用推送式补号",
				rc.EffectiveProvider())
			logger.Infof("[Replenish] %s", summary)
			return summary, nil
		}
		client, err := newReplenishSupplier(rc)
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
		res, err := h.claimAndImport(client, rc, supplierClaimRequest{
			Count:         count,
			ClientOrderID: orderID,
			BatchOrderID:  strings.TrimSpace(ev.OrderID),
		})
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

	case "key_revoked_abuse":
		// 上游因滥用吊销了 Key，只记录告警；本地失效由请求侧的封禁检测处理。
		summary := "webhook key_revoked_abuse: 上游已吊销 Key（疑似滥用）"
		if m := strings.TrimSpace(ev.Message); m != "" {
			summary = "webhook key_revoked_abuse: " + m
		}
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
