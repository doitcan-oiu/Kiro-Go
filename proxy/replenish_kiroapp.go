package proxy

// kiroapp.cc 供应商客户端（补号的第二个供应商）。
//
// 与最初对接的供应商（replenish.go 的 supplierClient）相比有三点关键差异，
// 决定了它只能靠轮询补号：
//   1. 认证用 Authorization: Bearer <key>，不是 X-API-Key。
//   2. 提取接口无幂等订单号 —— 重试会重复扣费，因此调用方绝不能重试 claim。
//   3. 没有 webhook 推送接口，新 Key 就绪不会主动通知。
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

// kiroappClient 对接 kiroapp.cc 的 /openapi/* 接口。
type kiroappClient struct {
	baseURL string
	apiKey  string
}

// newKiroappClient 从补号配置构造客户端。baseURL 留空时用官方默认地址，
// 因此用户通常只需填密钥。
func newKiroappClient(rc config.ReplenishConfig) (*kiroappClient, error) {
	base := strings.TrimRight(strings.TrimSpace(rc.KiroappBaseURL), "/")
	if base == "" {
		base = config.DefaultKiroappBaseURL
	}
	key := strings.TrimSpace(rc.KiroappApiKey)
	if key == "" {
		return nil, errors.New("kiroapp.cc apiKey is not configured")
	}
	return &kiroappClient{baseURL: base, apiKey: key}, nil
}

func (c *kiroappClient) ProviderName() string { return config.ReplenishProviderKiroapp }

// do 发起一次带 Bearer 认证的请求并把 JSON 解析到 out。
// 失败响应可能是 {"error":...} 或 {"message":...}，两者都尝试。
func (c *kiroappClient) do(method, path string, body interface{}, out interface{}) error {
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
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &errResp) == nil {
			if msg := firstNonEmpty(errResp.Error, errResp.Message); msg != "" {
				return fmt.Errorf("kiroapp %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
			}
		}
		return fmt.Errorf("kiroapp %s %s: HTTP %d", method, path, resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("kiroapp %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// kiroappStock 对应 GET /openapi/stock 响应。
type kiroappStock struct {
	AvailableKeys int     `json:"availableKeys"`
	KeyPrice      float64 `json:"keyPrice"`
}

// kiroappBalance 对应 GET /openapi/balance 响应。
type kiroappBalance struct {
	Balance float64 `json:"balance"`
}

// kiroappClaimResp 覆盖 /openapi/claim 的两种响应形态：
// 单个提取返回 {"key":"..."}，批量提取返回 {"keys":[...]}。
type kiroappClaimResp struct {
	Key  string   `json:"key"`
	Keys []string `json:"keys"`
}

// Stock 返回可用库存数量（availableKeys）。
func (c *kiroappClient) Stock() (int, error) {
	var s kiroappStock
	if err := c.do(http.MethodGet, "/openapi/stock", nil, &s); err != nil {
		return 0, err
	}
	return s.AvailableKeys, nil
}

// Balance 返回剩余积分。
func (c *kiroappClient) Balance() (float64, error) {
	var b kiroappBalance
	if err := c.do(http.MethodGet, "/openapi/balance", nil, &b); err != nil {
		return 0, err
	}
	return b.Balance, nil
}

// Account 返回账户信息：余额来自 /openapi/balance，单价来自 /openapi/stock。
// 库存查询失败不致命（余额是主信息），单价缺失时 HasPrice 为 false。
func (c *kiroappClient) Account() (*supplierAccount, error) {
	balance, err := c.Balance()
	if err != nil {
		return nil, err
	}
	acc := &supplierAccount{Remaining: balance}
	var s kiroappStock
	if err := c.do(http.MethodGet, "/openapi/stock", nil, &s); err == nil {
		acc.KeyPrice = s.KeyPrice
		acc.HasPrice = true
	}
	return acc, nil
}

// Claim 提取 req.Count 个 Key。count <= 1 走单个提取（不带请求体），
// 否则带 {"count":N} 批量提取。
//
// req 里的订单号字段全部被忽略：kiroapp 没有幂等键，重试会重复扣费，因此调用方
// 必须把一次 Claim 失败当作终态处理，不要重试。
func (c *kiroappClient) Claim(req supplierClaimRequest) (*supplierClaim, error) {
	count := req.Count
	if count <= 0 {
		return nil, errors.New("claim count must be positive")
	}

	var resp kiroappClaimResp
	if count == 1 {
		// 单个提取：接口不需要请求体。
		if err := c.do(http.MethodPost, "/openapi/claim", nil, &resp); err != nil {
			return nil, err
		}
	} else {
		if err := c.do(http.MethodPost, "/openapi/claim", map[string]interface{}{"count": count}, &resp); err != nil {
			return nil, err
		}
	}

	// 兼容两种响应形态：合并 key 与 keys，去掉空串。
	var keys []string
	if s := strings.TrimSpace(resp.Key); s != "" {
		keys = append(keys, s)
	}
	for _, k := range resp.Keys {
		if s := strings.TrimSpace(k); s != "" {
			keys = append(keys, s)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("kiroapp claim returned no keys")
	}

	claim := &supplierClaim{Keys: keys}
	// claim 响应不含余额，补查一次供面板展示；失败不影响本次提取结果。
	if balance, err := c.Balance(); err == nil {
		claim.Remaining = balance
	}
	return claim, nil
}
