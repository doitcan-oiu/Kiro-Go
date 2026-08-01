package proxy

// kiro.ceo 供应商客户端（对接文档/kiro.ceo.txt）。
//
// 特点：
//  1. 认证用 X-API-Key，与 kiross 相同，但接口更全（有 profile / stock）。
//  2. 按积分计费，每区单价独立。profile 的 quota/remaining/used_quota 沿用旧字段名，
//     但数字含义是积分而非个数。
//  3. POST /api/my/purchase 必填 client_order_id（32 位十六进制）幂等：同一 id 重试
//     原样返回首单，不重复扣费；换新 id 会变成第二笔订单。因此超时必须复用同一 id。
//  4. 区域严格隔离：zone 只接受 us / eu，其它值直接 400，且缺货不跨区顶替。
//     这也是唯一有区域概念的供应商——采购区决定了 Key 的导入区域。
//  5. 有 PUT /api/my/webhook 注册接口，回调地址可由本服务自动注册。
//
// 接口：
//   GET  /api/my/profile   {"quota":..,"remaining":..,"used_quota":..}
//   GET  /api/my/stock     {"max":N,"zones":[{"zone":"us","price":20,"available":N}]}
//   POST /api/my/purchase  {"count":N,"zone":"us","client_order_id":"<32hex>"}
//                          → {"purchased":5,"remaining":4500,"keys":[{"key":...}],
//                             "zone":"us","unit_price":100,"total_credits":500}
//   PUT  /api/my/webhook   {"webhook_url":"http://..."}

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"kiro-go/config"
)

// kiroceoClient 对接 kiro.ceo 的 /api/my/* 接口。
type kiroceoClient struct {
	baseURL string
	apiKey  string
	// zone 是本客户端固定采购的区域（us / eu）。由配置决定，构造时已归一化，
	// 因此 Claim 不需要再校验——上游对非法 zone 会直接 400。
	zone string
}

// newKiroceoClient 从该家的供应商配置构造客户端。
// baseURL 留空时用官方默认地址，因此用户通常只需填密钥与区域。
func newKiroceoClient(sc config.SupplierConfig) (*kiroceoClient, error) {
	base := strings.TrimRight(strings.TrimSpace(sc.BaseURL), "/")
	if base == "" {
		base = config.DefaultKiroceoBaseURL
	}
	key := strings.TrimSpace(sc.ApiKey)
	if key == "" {
		return nil, errors.New("kiro.ceo apiKey is not configured")
	}
	// 区域非法时拒绝构造，而不是静默按 us 采购：买错区的 Key 会带着错误区域进池，
	// 事后很难发现。留空是允许的（上游默认 us），显式写错则必须报错。
	raw := strings.TrimSpace(sc.Zone)
	zone := config.NormalizeSupplierZone(raw)
	if raw != "" && zone == "" {
		return nil, fmt.Errorf("kiro.ceo zone %q is invalid (want us or eu)", raw)
	}
	if zone == "" {
		zone = config.SupplierZoneUS
	}
	return &kiroceoClient{baseURL: base, apiKey: key, zone: zone}, nil
}

func (c *kiroceoClient) ProviderName() string { return config.ReplenishProviderKiroceo }

// do 发起一次带 X-API-Key 认证的请求。
func (c *kiroceoClient) do(method, path string, body, out interface{}) error {
	return supplierHTTPDo(supplierHTTPRequest{
		Provider: "kiro.ceo",
		Method:   method,
		URL:      c.baseURL + path,
		Header:   http.Header{"X-API-Key": []string{c.apiKey}},
		Body:     body,
		Out:      out,
	})
}

// kiroceoProfile 对应 GET /api/my/profile。字段名沿用旧版，数字含义是积分。
type kiroceoProfile struct {
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Quota     flexFloat `json:"quota"`
	Remaining flexFloat `json:"remaining"`
	UsedQuota flexFloat `json:"used_quota"`
}

// kiroceoStock 对应 GET /api/my/stock。
//
// max 是可提取数量；zones 给出各区单价与可购量，用于把「本区」的库存和单价
// 从聚合数字里挑出来——按区隔离的语义下，别区的库存对本客户端没有意义。
type kiroceoStock struct {
	Max   flexInt `json:"max"`
	Zones []struct {
		Zone      string    `json:"zone"`
		Price     flexFloat `json:"price"`
		Available flexInt   `json:"available"`
	} `json:"zones"`
}

// zoneInfo 返回指定区的单价与可购量；该区不在 zones 里时 ok 为 false。
func (s *kiroceoStock) zoneInfo(zone string) (price float64, available int, ok bool) {
	for _, z := range s.Zones {
		if config.NormalizeSupplierZone(z.Zone) == zone {
			return z.Price.Float64(), int(z.Available), true
		}
	}
	return 0, 0, false
}

// kiroceoPurchaseResp 对应 POST /api/my/purchase。
//
// 只取 keys[].key：账号/密码/issuer_url 是供应商侧的开号材料，账号池不存这些字段。
type kiroceoPurchaseResp struct {
	ClientOrderID string    `json:"client_order_id"`
	Purchased     flexInt   `json:"purchased"`
	Remaining     flexFloat `json:"remaining"`
	Zone          string    `json:"zone"`
	UnitPrice     flexFloat `json:"unit_price"`
	TotalCredits  flexFloat `json:"total_credits"`
	OrderID       string    `json:"order_id"`
	Keys          []struct {
		Key string `json:"key"`
	} `json:"keys"`
}

// Stock 返回本区可提取数量。
//
// 优先取 zones 里本区的 available：max 是跨区聚合值，而采购按区严格隔离，
// 用聚合值夹取会在「本区缺货、别区有货」时放过一个必然失败的请求。
// zones 缺失时回退到 max（兼容旧版响应）。
func (c *kiroceoClient) Stock() (int, error) {
	s, err := c.stock()
	if err != nil {
		return 0, err
	}
	if _, available, ok := s.zoneInfo(c.zone); ok {
		return available, nil
	}
	return int(s.Max), nil
}

func (c *kiroceoClient) stock() (*kiroceoStock, error) {
	var s kiroceoStock
	if err := c.do(http.MethodGet, "/api/my/stock", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Account 返回账户信息：积分余额来自 profile，本区单价来自 stock。
// 库存查询失败不致命（档案是主信息），单价缺失时 HasPrice 为 false。
func (c *kiroceoClient) Account() (*supplierAccount, error) {
	var p kiroceoProfile
	if err := c.do(http.MethodGet, "/api/my/profile", nil, &p); err != nil {
		return nil, err
	}
	name := p.Name
	if name == "" {
		name = p.Email
	}
	acc := &supplierAccount{
		Name:      name,
		Quota:     p.Quota.Float64(),
		Remaining: p.Remaining.Float64(),
		UsedQuota: p.UsedQuota.Float64(),
		HasQuota:  true,
	}
	if s, err := c.stock(); err == nil {
		if price, _, ok := s.zoneInfo(c.zone); ok && price > 0 {
			acc.KeyPrice = price
			acc.HasPrice = true
		}
	}
	return acc, nil
}

// Claim 提取 req.Count 个 Key。
//
// 幂等：req.ClientOrderID 由框架生成且非空（推送式补号会传入事件里的
// purchase_order_id），原样作为 client_order_id 上报。超时重试复用同一值即命中
// 重放，不会重复扣费；换新 id 会变成第二笔订单，因此调用方绝不能换 id 重试。
// req.BatchOrderID 被忽略：该家没有「按批次提取」的参数。
//
// zone 始终显式带上：文档说明不传默认美国区，而想要欧洲区必须显式传——
// 显式传两种情况都正确，也让请求自带审计信息。
//
// 库存并发争抢，purchased 可能小于 count，这不是错误；一个都没出 Key 才算失败。
func (c *kiroceoClient) Claim(req supplierClaimRequest) (*supplierClaim, error) {
	if req.Count <= 0 {
		return nil, errors.New("claim count must be positive")
	}
	orderID := strings.TrimSpace(req.ClientOrderID)
	if orderID == "" {
		orderID = newClientOrderID()
	}

	var r kiroceoPurchaseResp
	body := map[string]interface{}{
		"count":           req.Count,
		"zone":            c.zone,
		"client_order_id": orderID,
	}
	if err := c.do(http.MethodPost, "/api/my/purchase", body, &r); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(r.Keys))
	for _, k := range r.Keys {
		if s := strings.TrimSpace(k.Key); s != "" {
			keys = append(keys, s)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("kiro.ceo purchase returned no keys")
	}

	return &supplierClaim{
		Keys:      keys,
		Purchased: int(r.Purchased),
		Remaining: r.Remaining.Float64(),
		// 按积分计费，total_credits 是本单权威扣费额。
		Spent:   r.TotalCredits.Float64(),
		OrderID: orderID,
		// 回传成交区域，供编排层决定导入区域（上游未回传时用配置值兜底）。
		Zone: firstNonEmptyString(config.NormalizeSupplierZone(r.Zone), c.zone),
	}, nil
}

// SetWebhook 通过 PUT /api/my/webhook 把回调地址注册到该供应商。
func (c *kiroceoClient) SetWebhook(webhookURL string) error {
	body := map[string]interface{}{"webhook_url": webhookURL}
	return c.do(http.MethodPut, "/api/my/webhook", body, nil)
}
