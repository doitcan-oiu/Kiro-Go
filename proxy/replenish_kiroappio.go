package proxy

// kiroapp.io 供应商客户端（对接文档/kiroapp.io.txt）。
//
// 认证：Authorization: Bearer km_…（站点「设置 → API 令牌」签发，只能调 /api/me/*）。
//
// 三个与编排相关的特性：
//  1. 幂等：purchase 必填 client_order_id（32 位十六进制），相同 id 重放返回字节
//     一致的原响应，绝不重复扣款。因此网络超时可安全重试。
//  2. 阶梯定价：单价按母号累计产量分档，同一单里各 Key 可能不同价，便宜的先出货。
//     权威扣费数字是 total_debit，不是 count × 单价。
//  3. webhook 推送自带确定性派生的 client_order_id 与批次 order_id；带上 order_id
//     只提取该批次产出的 Key。回调地址只能在其站点后台手填，没有注册接口。
//
// 用到的端点：
//   GET  /api/me/profile   {"user":{"name":…,"balance":2060}}
//   GET  /api/me/stock     {"stock":120,"price_min":30,"price_max":65,"balance":2060}
//   POST /api/me/purchase  {"count":N,"client_order_id":"<32hex>","order_id":"<批次>"}
//                          → {"purchased":5,"total_debit":190,"keys":[{"key":"sk-…"}]}

import (
	"errors"
	"net/http"
	"strings"

	"kiro-go/config"
)

// kiroappioClient 对接 kiroapp.io 的 /api/me/* 接口。
type kiroappioClient struct {
	baseURL string
	apiKey  string
}

// newKiroappioClient 从该家的配置构造客户端。baseURL 留空时用官方默认地址，
// 因此用户通常只需填令牌。
func newKiroappioClient(sc config.SupplierConfig) (*kiroappioClient, error) {
	base := strings.TrimRight(strings.TrimSpace(sc.BaseURL), "/")
	if base == "" {
		base = config.DefaultKiroappioBaseURL
	}
	key := strings.TrimSpace(sc.ApiKey)
	if key == "" {
		return nil, errors.New("kiroapp.io api token is not configured")
	}
	return &kiroappioClient{baseURL: base, apiKey: key}, nil
}

func (c *kiroappioClient) ProviderName() string { return config.ReplenishProviderKiroappio }

// do 发起一次带 Bearer 认证的请求。
// do 发起一次带 Bearer 认证的请求（非幂等，失败即返回）。
func (c *kiroappioClient) do(method, path string, body, out interface{}) error {
	return c.doIdem(method, path, body, out, false)
}

// doIdem 同 do，但 idempotent=true 时允许对 5xx/网络错误重发。
// 文档明确："网络超时后用同一个 client_order_id 重试即可，安全"。
func (c *kiroappioClient) doIdem(method, path string, body, out interface{}, idempotent bool) error {
	return supplierHTTPDo(supplierHTTPRequest{
		Provider:   "kiroapp.io",
		Method:     method,
		URL:        c.baseURL + path,
		Header:     http.Header{"Authorization": []string{"Bearer " + c.apiKey}},
		Body:       body,
		Out:        out,
		Idempotent: idempotent,
	})
}

// kiroappioProfile 对应 GET /api/me/profile 响应。
type kiroappioProfile struct {
	User struct {
		Name    string    `json:"name"`
		Email   string    `json:"email"`
		Balance flexFloat `json:"balance"`
	} `json:"user"`
}

// kiroappioStock 对应 GET /api/me/stock 响应。
// Price 是 PriceMin 的向后兼容别名（阶梯定价下的最低价）。
type kiroappioStock struct {
	Stock    flexInt   `json:"stock"`
	Price    flexFloat `json:"price"`
	PriceMin flexFloat `json:"price_min"`
	PriceMax flexFloat `json:"price_max"`
	Balance  flexFloat `json:"balance"`
}

// kiroappioPurchaseResp 对应 POST /api/me/purchase 响应。
//
// 只取 keys[].key：本项目导入的是 Kiro API Key，account/password/issuer_url 属于
// 供应商侧的开号材料，账号池不存这些字段。
// 注意 remaining 是剩余库存，不是余额。
type kiroappioPurchaseResp struct {
	Purchased  flexInt   `json:"purchased"`
	Requested  flexInt   `json:"requested"`
	Remaining  flexInt   `json:"remaining"`
	UnitPrice  flexFloat `json:"unit_price"`
	TotalDebit flexFloat `json:"total_debit"`
	OrderID    string    `json:"order_id"`
	Replayed   bool      `json:"replayed"`
	Keys       []struct {
		Key   string    `json:"key"`
		Price flexFloat `json:"price"`
	} `json:"keys"`
}

func (c *kiroappioClient) stock() (*kiroappioStock, error) {
	var s kiroappioStock
	if err := c.do(http.MethodGet, "/api/me/stock", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Stock 返回可提取数量。
func (c *kiroappioClient) Stock() (int, error) {
	s, err := c.stock()
	if err != nil {
		return 0, err
	}
	return int(s.Stock), nil
}

// Account 返回账户信息：名称与余额来自 profile，价格区间来自 stock。
// 库存查询失败不致命（档案是主信息），价格缺失时 HasPrice 为 false。
func (c *kiroappioClient) Account() (*supplierAccount, error) {
	var p kiroappioProfile
	if err := c.do(http.MethodGet, "/api/me/profile", nil, &p); err != nil {
		return nil, err
	}
	name := p.User.Name
	if name == "" {
		name = p.User.Email
	}
	acc := &supplierAccount{Name: name, Remaining: p.User.Balance.Float64()}
	if s, err := c.stock(); err == nil {
		// price 是 price_min 的别名，优先用显式的 price_min。
		price := s.PriceMin
		if price <= 0 {
			price = s.Price
		}
		if price > 0 {
			acc.KeyPrice = price.Float64()
			acc.HasPrice = true
			// 仅在确有区间时给出上限，避免面板显示 "30~30"。
			if s.PriceMax > price {
				acc.PriceMax = s.PriceMax.Float64()
			}
		}
	}
	return acc, nil
}

// Claim 提取 req.Count 个 Key。
//
// ClientOrderID 由框架保证非空；推送式补号会传入 webhook 载荷里的 client_order_id
// （供应商按「批次 + 收件人」确定性派生），使重复推送与超时重试都命中幂等重放。
// BatchOrderID 非空时作为 order_id 传上去，只提取该批次产出的 Key。
//
// 余额不足时供应商按买得起的数量成交（purchased < requested），这不算错误；
// 只有一个 Key 都没出才算失败。
func (c *kiroappioClient) Claim(req supplierClaimRequest) (*supplierClaim, error) {
	if req.Count <= 0 {
		return nil, errors.New("claim count must be positive")
	}
	orderID := strings.TrimSpace(req.ClientOrderID)
	if orderID == "" {
		orderID = newClientOrderID()
	}

	body := map[string]interface{}{
		"count":           req.Count,
		"client_order_id": orderID,
	}
	if batch := strings.TrimSpace(req.BatchOrderID); batch != "" {
		body["order_id"] = batch
	}

	var resp kiroappioPurchaseResp
	// 幂等重试：同 client_order_id 重放返回字节一致的原响应，绝不重复扣款。
	if err := c.doIdem(http.MethodPost, "/api/me/purchase", body, &resp, true); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(resp.Keys))
	for _, k := range resp.Keys {
		if s := strings.TrimSpace(k.Key); s != "" {
			keys = append(keys, s)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("kiroapp.io purchase returned no keys")
	}

	claim := &supplierClaim{
		Keys:      keys,
		Purchased: int(resp.Purchased),
		Spent:     resp.TotalDebit.Float64(),
		// 回传我方使用的幂等键而非上游批次 id：面板上展示它才有「重试用同一个」的意义。
		OrderID: orderID,
	}
	// 响应里的 remaining 是剩余库存而非余额，补查一次档案取余额供面板展示；
	// 失败不影响本次提取结果。
	var p kiroappioProfile
	if err := c.do(http.MethodGet, "/api/me/profile", nil, &p); err == nil {
		claim.Remaining = p.User.Balance.Float64()
	}
	return claim, nil
}
