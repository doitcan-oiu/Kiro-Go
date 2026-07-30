package proxy

// kiroapp.io 供应商客户端（补号的第三个供应商）。
//
// 与前两个供应商的差异：
//   1. 认证用 Authorization: Bearer km_<token>（设置 → API 令牌里签发）。
//   2. 提取接口 POST /api/me/purchase 必填 client_order_id（32 位十六进制），
//      幂等：同 id 重放返回字节一致的原响应，绝不重复扣款。因此超时可安全重试。
//   3. 阶梯定价：单价按母号累计产量分档，同一单里各 Key 可能不同价，便宜的先出货。
//      权威扣费数字是响应里的 total_debit，不是 count × 单价。
//   4. 有 webhook 推送，但回调地址只能在其站点后台手填（没有注册接口）。推送载荷
//      自带确定性派生的 client_order_id，收到后原样带上即可，无需自己生成。
//
// 接口（均在 /api/me/* 前台命名空间下，令牌调不了管理端）：
//   GET  /api/me/profile   {"user":{"name":...,"balance":2060,...}}
//   GET  /api/me/stock     {"stock":120,"price_min":30,"price_max":65,"balance":2060}
//   POST /api/me/purchase  {"count":N,"client_order_id":"<32hex>","order_id":"<批次>"}
//                          → {"purchased":5,"total_debit":190,"keys":[{"key":"sk-..."}],...}

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"kiro-go/config"
)

// kiroappioClient 对接 kiroapp.io 的 /api/me/* 接口。
type kiroappioClient struct {
	baseURL string
	apiKey  string
}

// newKiroappioClient 从补号配置构造客户端。baseURL 留空时用官方默认地址，
// 因此用户通常只需填令牌。
func newKiroappioClient(rc config.ReplenishConfig) (*kiroappioClient, error) {
	base := strings.TrimRight(strings.TrimSpace(rc.KiroappioBaseURL), "/")
	if base == "" {
		base = config.DefaultKiroappioBaseURL
	}
	key := strings.TrimSpace(rc.KiroappioApiKey)
	if key == "" {
		return nil, errors.New("kiroapp.io api token is not configured")
	}
	return &kiroappioClient{baseURL: base, apiKey: key}, nil
}

func (c *kiroappioClient) ProviderName() string { return config.ReplenishProviderKiroappio }

// do 发起一次带 Bearer 认证的请求并把 JSON 解析到 out。
// 失败响应统一是 {"error":"人类可读的原因"}；401=令牌无效/过期，403=无权限，429=限速。
func (c *kiroappioClient) do(method, path string, body interface{}, out interface{}) error {
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
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// 复用共享 REST client（遵循全局出站代理配置）。
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
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("kiroapp.io %s %s: HTTP %d: %s", method, path, resp.StatusCode, errResp.Error)
		}
		return fmt.Errorf("kiroapp.io %s %s: HTTP %d", method, path, resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("kiroapp.io %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// kiroappioProfile 对应 GET /api/me/profile 响应。
type kiroappioProfile struct {
	User struct {
		Name    string  `json:"name"`
		Email   string  `json:"email"`
		Balance float64 `json:"balance"`
	} `json:"user"`
}

// kiroappioStock 对应 GET /api/me/stock 响应。
// Price 是向后兼容字段，等于 PriceMin（阶梯定价下的最低价）。
type kiroappioStock struct {
	Stock    int     `json:"stock"`
	Price    float64 `json:"price"`
	PriceMin float64 `json:"price_min"`
	PriceMax float64 `json:"price_max"`
	Balance  float64 `json:"balance"`
}

// kiroappioPurchaseResp 对应 POST /api/me/purchase 响应。
//
// 只取 keys[].key：本项目导入的是 Kiro API Key，账号/密码/issuer_url 属于供应商
// 侧的开号材料，账号池不存这些字段。
type kiroappioPurchaseResp struct {
	Purchased  int     `json:"purchased"`
	Requested  int     `json:"requested"`
	Remaining  int     `json:"remaining"`
	UnitPrice  float64 `json:"unit_price"`
	TotalDebit float64 `json:"total_debit"`
	OrderID    string  `json:"order_id"`
	Replayed   bool    `json:"replayed"`
	Keys       []struct {
		Key   string  `json:"key"`
		Price float64 `json:"price"`
	} `json:"keys"`
}

// Stock 返回可提取数量。
func (c *kiroappioClient) Stock() (int, error) {
	s, err := c.stock()
	if err != nil {
		return 0, err
	}
	return s.Stock, nil
}

func (c *kiroappioClient) stock() (*kiroappioStock, error) {
	var s kiroappioStock
	if err := c.do(http.MethodGet, "/api/me/stock", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Account 返回账户信息：名称与余额来自 /api/me/profile，单价来自 /api/me/stock。
// 阶梯定价下单价取 price_min（最低价），面板据此给出下限参考。
// 库存查询失败不致命（档案是主信息），单价缺失时 HasPrice 为 false。
func (c *kiroappioClient) Account() (*supplierAccount, error) {
	var p kiroappioProfile
	if err := c.do(http.MethodGet, "/api/me/profile", nil, &p); err != nil {
		return nil, err
	}
	name := p.User.Name
	if name == "" {
		name = p.User.Email
	}
	acc := &supplierAccount{Name: name, Remaining: p.User.Balance}
	if s, err := c.stock(); err == nil {
		// price 是 price_min 的向后兼容别名，优先用显式的 price_min。
		price := s.PriceMin
		if price <= 0 {
			price = s.Price
		}
		if price > 0 {
			acc.KeyPrice = price
			acc.HasPrice = true
			// 仅在确有区间时给出上限，避免面板显示 "30~30"。
			if s.PriceMax > price {
				acc.PriceMax = s.PriceMax
			}
		}
	}
	return acc, nil
}

// Claim 提取 req.Count 个 Key。
//
// ClientOrderID 为空时自动生成一个 32 位十六进制幂等键；推送式补号应传入 webhook
// 载荷里的 client_order_id（供应商已按「批次 + 收件人」确定性派生），这样重复推送
// 与超时重试都会命中幂等重放而不二次扣费。
// BatchOrderID 非空时作为 order_id 传上去，只提取该批次产出的 Key。
//
// 余额不足时供应商按买得起的数量成交（purchased < requested），这不是错误；
// 只有一个都没出 Key 才算失败。
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
	if err := c.do(http.MethodPost, "/api/me/purchase", body, &resp); err != nil {
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
		Purchased: resp.Purchased,
		Spent:     resp.TotalDebit,
		// 回传我方使用的幂等键，而不是供应商的批次 order_id：面板上展示它才有
		// 「重试用同一个」的意义。
		OrderID: orderID,
	}
	// 响应里的 remaining 是剩余库存而非余额，补查一次档案取余额供面板展示；
	// 失败不影响本次提取结果。
	var p kiroappioProfile
	if err := c.do(http.MethodGet, "/api/me/profile", nil, &p); err == nil {
		claim.Remaining = p.User.Balance
	}
	return claim, nil
}
