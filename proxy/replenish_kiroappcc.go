package proxy

// kiroapp.cc 供应商客户端（对接文档/kiroapp.cc.txt）。
//
// 与另两家的关键差异，直接影响调用策略：
//  1. 认证用 Authorization: Bearer <API Key>。
//  2. /openapi/claim 没有幂等键 —— 重试会重复扣积分。因此本客户端的失败
//     一律当作终态，绝不自动重试；上层也不会为它生成 client_order_id。
//  3. 错误体是嵌套的 {"error":{"type","message"}}，另两家是平铺的 {"error":"..."}，
//     所以这家不能复用共享的 supplierHTTPDo 错误解析，单独实现 do()。
//  4. 有频率限制：每 60 秒最多 60 次，超限进入 180 秒冷却并返回 429（带 Retry-After）。
//     补号是低频操作（推送触发 + 轮询兜底），正常不会触及；触发时把 Retry-After
//     透传到错误信息里，便于从面板判断要等多久。
//  5. Webhook 到货通知只说明「有新库存」，载荷不含数量，因此收到推送后按面板上
//     为这家配置的数量下单（见 handleProviderWebhookEvent）。
//
// 接口：
//   POST /openapi/claim            提取 1 个：{"key":"ksk_..."}
//   POST /openapi/claim {count:N}  批量提取：{"keys":["ksk_...", ...]}
//   GET  /openapi/stock            {"availableKeys":12,"keyPrice":1.5}
//   GET  /openapi/balance          {"balance":100}

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

// kiroappccClient 对接 kiroapp.cc 的 /openapi/* 接口。
type kiroappccClient struct {
	baseURL string
	apiKey  string
}

// newKiroappccClient 从该家的配置构造客户端。baseURL 留空时用官方默认地址，
// 因此用户通常只需填密钥。
func newKiroappccClient(sc config.SupplierConfig) (*kiroappccClient, error) {
	base := strings.TrimRight(strings.TrimSpace(sc.BaseURL), "/")
	if base == "" {
		base = config.DefaultKiroappccBaseURL
	}
	key := strings.TrimSpace(sc.ApiKey)
	if key == "" {
		return nil, errors.New("kiroapp.cc apiKey is not configured")
	}
	return &kiroappccClient{baseURL: base, apiKey: key}, nil
}

func (c *kiroappccClient) ProviderName() string { return config.ReplenishProviderKiroappcc }

// kiroappccError 对应该家统一的错误体：{"error":{"type","message","retryAfter"}}。
// 限流时 type 为 rate_limit_exceeded 并带 retryAfter（秒）。
type kiroappccError struct {
	Error struct {
		Type       string `json:"type"`
		Message    string `json:"message"`
		RetryAfter int    `json:"retryAfter"`
	} `json:"error"`
}

// do 发起一次带 Bearer 认证的请求并把 JSON 解析到 out。
//
// 不复用 supplierHTTPDo：这家的错误体是嵌套结构，且限流信息（type/retryAfter 与
// Retry-After 响应头）需要透传出来，共享版按平铺 {"error":"..."} 解析会丢掉这些。
func (c *kiroappccClient) do(method, path string, body interface{}, out interface{}) error {
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
		return c.decodeError(method, path, resp, data)
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("kiroapp.cc %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// decodeError 把上游失败响应转成带原因的错误。限流时附上还需等待的秒数，
// 优先用响应体里的 retryAfter，其次用 Retry-After 响应头。
func (c *kiroappccClient) decodeError(method, path string, resp *http.Response, data []byte) error {
	var e kiroappccError
	msg, kind, retry := "", "", 0
	if json.Unmarshal(data, &e) == nil {
		msg, kind, retry = e.Error.Message, e.Error.Type, e.Error.RetryAfter
	}
	if retry == 0 {
		// 响应头形式：Retry-After: <秒>。
		if h := strings.TrimSpace(resp.Header.Get("Retry-After")); h != "" {
			var n int
			if _, err := fmt.Sscanf(h, "%d", &n); err == nil {
				retry = n
			}
		}
	}

	detail := msg
	if kind != "" {
		detail = strings.TrimSpace(kind + ": " + msg)
	}
	if retry > 0 {
		detail = fmt.Sprintf("%s (retry after %ds)", detail, retry)
	}
	if strings.TrimSpace(detail) == "" {
		return fmt.Errorf("kiroapp.cc %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return fmt.Errorf("kiroapp.cc %s %s: HTTP %d: %s", method, path, resp.StatusCode, detail)
}

// kiroappccStock 对应 GET /openapi/stock 响应。
type kiroappccStock struct {
	AvailableKeys flexInt   `json:"availableKeys"`
	KeyPrice      flexFloat `json:"keyPrice"`
}

// kiroappccBalance 对应 GET /openapi/balance 响应。
type kiroappccBalance struct {
	Balance flexFloat `json:"balance"`
}

// kiroappccClaimResp 覆盖 /openapi/claim 的两种响应形态：
// 单个提取返回 {"key":"..."}，批量提取返回 {"keys":[...]}。
//
// PointsCost 为 0 表示这批是自己投放凭证产出的，不扣积分（车主自取）。
type kiroappccClaimResp struct {
	Key        string    `json:"key"`
	Keys       []string  `json:"keys"`
	PointsCost flexFloat `json:"pointsCost"`
}

// Stock 返回可用库存数量。
func (c *kiroappccClient) Stock() (int, error) {
	var s kiroappccStock
	if err := c.do(http.MethodGet, "/openapi/stock", nil, &s); err != nil {
		return 0, err
	}
	return int(s.AvailableKeys), nil
}

// Account 返回账户信息：余额来自 /openapi/balance，单价来自 /openapi/stock。
// 库存查询失败不致命（余额是主信息），单价缺失时 HasPrice 为 false。
func (c *kiroappccClient) Account() (*supplierAccount, error) {
	var b kiroappccBalance
	if err := c.do(http.MethodGet, "/openapi/balance", nil, &b); err != nil {
		return nil, err
	}
	acc := &supplierAccount{Remaining: b.Balance.Float64()}
	var s kiroappccStock
	if err := c.do(http.MethodGet, "/openapi/stock", nil, &s); err == nil && s.KeyPrice > 0 {
		acc.KeyPrice = s.KeyPrice.Float64()
		acc.HasPrice = true
	}
	return acc, nil
}

// Claim 提取 req.Count 个 Key。count == 1 走单个提取（不带请求体），
// 否则带 {"count":N} 批量提取。
//
// req.ClientOrderID / BatchOrderID 都被忽略：这家没有幂等键与批次概念。
// 重要：因此本方法的失败必须当作终态，调用方不得自动重试 —— 重试会重复扣积分。
func (c *kiroappccClient) Claim(req supplierClaimRequest) (*supplierClaim, error) {
	if req.Count <= 0 {
		return nil, errors.New("claim count must be positive")
	}

	var resp kiroappccClaimResp
	if req.Count == 1 {
		// 单个提取：接口不需要请求体。
		if err := c.do(http.MethodPost, "/openapi/claim", nil, &resp); err != nil {
			return nil, err
		}
	} else {
		if err := c.do(http.MethodPost, "/openapi/claim",
			map[string]interface{}{"count": req.Count}, &resp); err != nil {
			return nil, err
		}
	}

	// 兼容两种响应形态：合并 key 与 keys，去掉空串。
	keys := make([]string, 0, len(resp.Keys)+1)
	if s := strings.TrimSpace(resp.Key); s != "" {
		keys = append(keys, s)
	}
	for _, k := range resp.Keys {
		if s := strings.TrimSpace(k); s != "" {
			keys = append(keys, s)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("kiroapp.cc claim returned no keys")
	}

	claim := &supplierClaim{
		Keys:  keys,
		Spent: resp.PointsCost.Float64(),
		// 无订单号：这家没有幂等键，不伪造一个，否则面板会显示一个
		// 「重试可用」的假象。
	}
	// claim 响应不含余额，补查一次供面板展示；失败不影响本次提取结果。
	var b kiroappccBalance
	if err := c.do(http.MethodGet, "/openapi/balance", nil, &b); err == nil {
		claim.Remaining = b.Balance.Float64()
	}
	return claim, nil
}
