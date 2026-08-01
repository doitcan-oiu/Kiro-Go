package proxy

// 供应商客户端共用的 HTTP 收发。
//
// 两家供应商只在认证头与端点路径上不同，请求构造、代理遵循、响应体上限、错误格式
// 解析这些都一样，抽到这里避免两份几乎相同的 do()。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"kiro-go/config"
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
}

// supplierHTTPDo 发起一次供应商 API 调用并解析响应。
//
// 两家的失败响应都是 {"error":"..."}（kiroapp.io 明确如此，kiross 同格式），
// 另兼容 {"message":"..."}，把上游原因透传出来——否则面板只能显示一个裸状态码。
func supplierHTTPDo(req supplierHTTPRequest) error {
	var reqBody io.Reader
	if req.Body != nil {
		buf, err := json.Marshal(req.Body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(buf)
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
	if req.Body != nil {
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
				return fmt.Errorf("%s %s %s: HTTP %d: %s",
					req.Provider, req.Method, req.URL, resp.StatusCode, msg)
			}
		}
		return fmt.Errorf("%s %s %s: HTTP %d", req.Provider, req.Method, req.URL, resp.StatusCode)
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
