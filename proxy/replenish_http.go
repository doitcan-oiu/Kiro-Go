package proxy

// 供应商客户端共用的 HTTP 收发。
//
// 两家供应商只在认证头与端点路径上不同，请求构造、代理遵循、响应体上限、错误格式
// 解析这些都一样，抽到这里避免两份几乎相同的 do()。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

// supplierHTTPRequest 描述一次供应商 API 调用。
type supplierHTTPRequest struct {
	// Provider 仅用于错误信息前缀，便于从日志区分是哪家出的问题。
	Provider string
	Method   string
	URL      string
	// Header 是该家的认证头（kiross 用 X-API-Key，kiroappio 用 Authorization）。
	Header http.Header
	// Body 非 nil 时按 JSON 编码，并自动带上 Content-Type。
	Body interface{}
	// Out 非 nil 时把成功响应体解析进去。
	Out interface{}
	// Idempotent 表示这次调用可以安全重试：要么是只读的，要么带幂等键
	// （client_order_id），上游对同一个 id 重试会原样返回首次结果而不重复扣费。
	//
	// 显式 opt-in 而非默认开启：没有幂等键的供应商重试就是重复扣费，让调用方
	// 必须主动声明「这笔可以重试」，避免以后新增供应商时默默继承了不安全的行为。
	Idempotent bool
}

// supplierHTTPMaxAttempts 是可重试调用的最大尝试次数（含首次）。
// 上游 5xx 多为瞬时故障，补号是低频操作，两次退避足够且不会给上游压力。
const supplierHTTPMaxAttempts = 3

// supplierHTTPRetryDelays 是各次重试前的等待时长，与 kiro.ss 文档建议的
// 重试节奏（3s / 8s）一致。
var supplierHTTPRetryDelays = []time.Duration{3 * time.Second, 8 * time.Second}

// supplierHTTPDo 发起一次供应商 API 调用并解析响应。
//
// 两家的失败响应都是 {"error":"..."}（kiroapp.io 明确如此，kiross 同格式），
// 另兼容 {"message":"..."}，把上游原因透传出来——否则面板只能显示一个裸状态码。
func supplierHTTPDo(req supplierHTTPRequest) error {
	// 请求体先序列化一次，重试时用同一份字节重建 reader（Body 只能读一遍）。
	var payload []byte
	if req.Body != nil {
		buf, err := json.Marshal(req.Body)
		if err != nil {
			return err
		}
		payload = buf
	}

	attempts := 1
	if req.Idempotent {
		attempts = supplierHTTPMaxAttempts
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := supplierHTTPRetryDelays[minInt(attempt-1, len(supplierHTTPRetryDelays)-1)]
			logger.Warnf("[Supplier] %s %s %s: %v; retrying in %s (attempt %d/%d)",
				req.Provider, req.Method, req.URL, lastErr, delay, attempt+1, attempts)
			time.Sleep(delay)
		}
		err := supplierHTTPAttempt(req, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		// 只重试瞬时失败：上游 5xx 与网络错误。4xx 是请求本身的问题，重试无意义；
		// 解码失败也不重试——响应已经到手，重发只会让上游再处理一遍。
		if !isRetryableSupplierError(err) {
			return err
		}
	}
	return lastErr
}

// isRetryableSupplierError 判断错误是否值得重试。
//
// 5xx：文档明确「用同一个 client_order_id 重试是安全的」。
// 网络错误（非 supplierHTTPError）：请求可能压根没到上游，也可能已成交——
// 正因为无从区分，才必须靠幂等键重试而不是放弃。
func isRetryableSupplierError(err error) bool {
	var he *supplierHTTPError
	if errors.As(err, &he) {
		return he.Status >= 500
	}
	// 解码失败不重试：响应体已拿到，问题在我方解析。
	if strings.Contains(err.Error(), "decode response") {
		return false
	}
	return true
}

// supplierHTTPAttempt 发起一次调用。payload 为 nil 表示无请求体。
func supplierHTTPAttempt(req supplierHTTPRequest, payload []byte) error {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}

	httpReq, err := http.NewRequest(req.Method, req.URL, reqBody)
	if err != nil {
		return err
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// 复用共享 REST client（遵循全局出站代理配置）。
	resp, err := GetRestClientForProxy(config.GetProxyURL()).Do(httpReq)
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
			if msg := firstNonEmptyString(errResp.Error, errResp.Message); msg != "" {
				return &supplierHTTPError{
					Provider: req.Provider, Method: req.Method, URL: req.URL,
					Status: resp.StatusCode, Detail: msg,
				}
			}
		}
		// 上游未必返回 JSON（网关的 502/500 页面、纯文本报错都常见）。带上响应体
		// 片段，否则面板上只有一个裸状态码，无从判断是上游故障还是我方请求有误。
		return &supplierHTTPError{
			Provider: req.Provider, Method: req.Method, URL: req.URL,
			Status: resp.StatusCode, Detail: bodySnippet(data),
		}
	}

	if req.Out != nil {
		if err := json.Unmarshal(data, req.Out); err != nil {
			return fmt.Errorf("%s %s %s: decode response: %w",
				req.Provider, req.Method, req.URL, err)
		}
	}
	return nil
}

// firstNonEmptyString 返回第一个非空字符串。
func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// flexFloat 是宽松解析的数值：既接受 JSON 数字，也接受字符串形式的数字。
//
// 起因是 kiro.ss 的 purchase 把 remaining 返回成 "80" 而非 80，严格的 float64
// 会让整个响应解码失败——而 purchase 是已经成交扣费的操作，解码失败等于钱花了
// 但 Key 没入库。展示用的数值字段一律用这个类型，避免一个次要字段的类型漂移
// 毁掉整笔订单。
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	// 字符串形式：剥掉引号再按数字解析。空字符串按 0 处理。
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseFloat(inner, 64)
		if err != nil {
			// 非数字字符串不视为错误：这是展示字段，宁可显示 0 也不要毁掉订单。
			*f = 0
			return nil
		}
		*f = flexFloat(v)
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexFloat(v)
	return nil
}

// Float64 返回底层数值。
func (f flexFloat) Float64() float64 { return float64(f) }

// flexInt 与 flexFloat 同理，用于可能被上游写成字符串的整数字段
// （purchased、stock 之类）。
type flexInt int

func (i *flexInt) UnmarshalJSON(data []byte) error {
	var f flexFloat
	if err := f.UnmarshalJSON(data); err != nil {
		return err
	}
	*i = flexInt(int(f))
	return nil
}

// Int 返回底层数值。
func (i flexInt) Int() int { return int(i) }

// supplierHTTPError 是一次非 2xx 的上游响应。带上 Status 让调用方能按状态码决策
// （5xx 可安全重试，4xx 不该重试），而不是去解析错误字符串。
type supplierHTTPError struct {
	Provider string
	Method   string
	URL      string
	Status   int
	Detail   string
}

func (e *supplierHTTPError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s %s %s: HTTP %d", e.Provider, e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("%s %s %s: HTTP %d: %s", e.Provider, e.Method, e.URL, e.Status, e.Detail)
}

// Retryable 报告该状态码是否值得用同一个幂等键重试。
//
// 5xx 与 429 是上游侧的临时故障；各家文档都明确「用同一个 client_order_id 重试是
// 安全的」，服务端会识别成同一笔订单。4xx（参数错、密钥无效、积分不足）重试只会
// 得到同样的结果，不重试。
func (e *supplierHTTPError) Retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests
}

// bodySnippet 把响应体压成一行短文本，用于错误信息。
// 上游的 HTML 错误页会很长，这里截断并去掉换行，避免污染日志。
func bodySnippet(data []byte) string {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
