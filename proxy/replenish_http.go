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
