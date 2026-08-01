package proxy

// kiross 供应商客户端（取号文档 对接文档/kiro.ss.txt）。
//
// 特点：
//   1. 认证用 X-API-Key: usr-xxx。
//   2. POST /api/my/purchase 必填 client_order_id（32 位十六进制），幂等：同一订单号
//      + 相同 count 重试返回首次结果，因此超时可安全重试。同一订单号改 count 返回 409，
//      所以每次「新的」提取必须换新订单号 —— 框架为每次 Claim 生成一个。
//   3. 有 PUT /api/my/webhook 注册接口，回调地址可由本服务自动注册。
//   4. 提取不再次扣费（费用在预订时已产生），库存不足时按实际可领取数量成交。
//
// 接口：
//   POST /api/my/purchase  {"count":N,"client_order_id":"<32hex>"}
//                          → {"purchased":2,"remaining":80,"keys":[{"key":"ksk_..."}]}
//   PUT  /api/my/webhook   {"webhook_url":"http://..."}
//
// 该家没有库存/档案/余额接口，因此 Stock 返回 -1（未知，调用方不夹取），
// Account 只能用 POST /api/my/webhook/test 做鉴权探测，且不返回余额与配额。

import (
	"errors"
	"net/http"
	"strings"

	"kiro-go/config"
)

// kirossClient 对接 kiross 的 /api/my/* 接口。
type kirossClient struct {
	baseURL string
	apiKey  string
}

// newKirossClient 从该家的供应商配置构造客户端；baseURL/apiKey 缺失时返回错误
// （该家没有公开的官方默认地址，必须由用户填写）。
func newKirossClient(sc config.SupplierConfig) (*kirossClient, error) {
	base := strings.TrimRight(strings.TrimSpace(sc.BaseURL), "/")
	key := strings.TrimSpace(sc.ApiKey)
	if base == "" {
		return nil, errors.New("kiross baseUrl is not configured")
	}
	if key == "" {
		return nil, errors.New("kiross apiKey is not configured")
	}
	return &kirossClient{baseURL: base, apiKey: key}, nil
}

func (c *kirossClient) ProviderName() string { return config.ReplenishProviderKiross }

// do 发起一次带 X-API-Key 认证的请求。
// do 发起一次带 X-API-Key 认证的请求。
//
// idempotent 表示该调用可安全重发：文档明确 5xx/超时应当用同一个 client_order_id
// 重试而不是换新的（换 id 会变成第二笔订单）。因此 purchase 传 true，靠上游的
// 幂等键去重；webhook 的写入与探测本身也是幂等的。
func (c *kirossClient) do(method, path string, body, out interface{}) error {
	return c.doIdem(method, path, body, out, false)
}

func (c *kirossClient) doIdem(method, path string, body, out interface{}, idempotent bool) error {
	return supplierHTTPDo(supplierHTTPRequest{
		Provider:   "kiross",
		Method:     method,
		URL:        c.baseURL + path,
		Header:     http.Header{"X-API-Key": []string{c.apiKey}},
		Body:       body,
		Out:        out,
		Idempotent: idempotent,
	})
}

// kirossPurchaseResp 对应 POST /api/my/purchase 响应。
type kirossPurchaseResp struct {
	ClientOrderID string    `json:"client_order_id"`
	Purchased     flexInt   `json:"purchased"`
	Remaining     flexFloat `json:"remaining"`
	Keys          []struct {
		Key string `json:"key"`
	} `json:"keys"`
}

// Stock 返回 -1 表示「上限未知」：该家没有库存查询接口，由 purchase 自行按实际
// 可领取数量成交，调用方不应据此夹取请求量。
func (c *kirossClient) Stock() (int, error) { return -1, nil }

// Account 用于面板「测试连接」。
//
// 该家的文档只有三个接口：purchase、PUT webhook、POST webhook/test，没有任何
// 档案/余额/库存查询。因此这里用 POST /api/my/webhook/test 做连通性与鉴权探测：
// 它是文档里唯一「无副作用、不扣费」的可调用端点（只是让上游给已配置的回调地址
// 发一条测试推送）。返回的 supplierAccount 不含余额与配额——没有的数据不编造，
// 面板据 Has* 标志位不展示这些字段。
//
// 副作用提示：本服务的入站端点会因此收到一条 test 事件，它只记录、不下单。
func (c *kirossClient) Account() (*supplierAccount, error) {
	if err := c.do(http.MethodPost, "/api/my/webhook/test", nil, nil); err != nil {
		return nil, err
	}
	return &supplierAccount{}, nil
}

// Claim 提取 req.Count 个 Key。
//
// 幂等：req.ClientOrderID 由框架生成且非空，原样作为 client_order_id 上报；
// 超时重试复用同一值即命中重放，不会重复领取。
// req.BatchOrderID 被忽略：该家没有「按批次提取」的参数（推送里的
// purchase_order_id 已由框架当作幂等键传入 ClientOrderID）。
//
// 库存不足时按实际可领取数量成交（purchased < count），这不是错误；
// 一个都没出 Key 才算失败。
func (c *kirossClient) Claim(req supplierClaimRequest) (*supplierClaim, error) {
	if req.Count <= 0 {
		return nil, errors.New("claim count must be positive")
	}
	orderID := strings.TrimSpace(req.ClientOrderID)
	if orderID == "" {
		orderID = newClientOrderID()
	}

	var r kirossPurchaseResp
	body := map[string]interface{}{
		"count":           req.Count,
		"client_order_id": orderID,
	}
	// 幂等重试：client_order_id 已固定，5xx/网络抖动重发不会二次成交。
	if err := c.doIdem(http.MethodPost, "/api/my/purchase", body, &r, true); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(r.Keys))
	for _, k := range r.Keys {
		if s := strings.TrimSpace(k.Key); s != "" {
			keys = append(keys, s)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("kiross purchase returned no keys")
	}

	return &supplierClaim{
		Keys:      keys,
		Purchased: int(r.Purchased),
		Remaining: r.Remaining.Float64(),
		OrderID:   orderID,
	}, nil
}

// SetWebhook 通过 PUT /api/my/webhook 把回调地址注册到该供应商。
func (c *kirossClient) SetWebhook(webhookURL string) error {
	body := map[string]interface{}{"webhook_url": webhookURL}
	return c.do(http.MethodPut, "/api/my/webhook", body, nil)
}
