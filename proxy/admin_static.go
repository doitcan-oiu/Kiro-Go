package proxy

// 管理面板静态资源服务。
//
// 面板是 Vue 3 + Vite 构建的单页应用（源码在 web/，产物在 web/dist）：
//   - /admin、/admin/ 以及任何不存在的 /admin/<路由> 都返回 index.html，
//     由前端路由接管（history 模式的 SPA fallback）；
//   - /admin/assets/* 等真实文件直接回传。
//
// 与旧实现的三个关键差别：
//
//  1. 路径穿越已封堵。旧实现是 http.ServeFile(w, r, "web/"+path)，path 直接来自
//     URL。http.ServeFile 虽然会拒绝含 ".." 的**请求路径**，但这里拼出的字符串
//     被当作文件名传入，一旦拼接前做过任何解码/改写就可能逃出 web/。现在统一走
//     http.FileServer + http.Dir，由标准库负责把请求路径限制在根目录内。
//
//  2. 缓存策略分离。Vite 产物带内容哈希（index-BTfRl_f8.js），可以长期强缓存；
//     index.html 必须不缓存，否则发版后浏览器仍然加载旧 HTML、引用已被删除的
//     哈希文件，页面直接白屏。旧实现靠前端在 URL 上拼 ?v=Date.now() 绕过缓存，
//     代价是每次刷新都重新下载全部静态资源。
//
//  3. 根目录可配置。默认 web/dist；KIRO_WEB_ROOT 可覆盖，便于容器里把产物放到
//     其他路径，也方便测试。
import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

const defaultWebRoot = "web/dist"

var (
	webRootOnce sync.Once
	webRootDir  string
	staticFS    http.Handler
)

// webRoot 返回面板产物目录，KIRO_WEB_ROOT 可覆盖默认的 web/dist。
func webRoot() string {
	webRootOnce.Do(func() {
		webRootDir = defaultWebRoot
		if env := strings.TrimSpace(os.Getenv("KIRO_WEB_ROOT")); env != "" {
			webRootDir = env
		}
		// StripPrefix 去掉 /admin/ 前缀后交给 FileServer，由 http.Dir 保证不会
		// 逃出根目录。
		staticFS = http.StripPrefix("/admin/", http.FileServer(http.Dir(webRootDir)))
	})
	return webRootDir
}

// indexPath 返回 SPA 入口文件路径。
func indexPath() string {
	return filepath.Join(webRoot(), "index.html")
}

// serveAdminPage 返回 SPA 入口。
func (h *Handler) serveAdminPage(w http.ResponseWriter, r *http.Request) {
	serveSPAIndex(w, r)
}

// serveSPAIndex 回传 index.html，并禁止缓存。
//
// 不缓存是刚性要求：index.html 里引用的是带哈希的资源名，发版后旧 HTML 指向的
// 文件已经不存在，浏览器若复用缓存副本就会拿到 404 的 JS/CSS 而白屏。
func serveSPAIndex(w http.ResponseWriter, r *http.Request) {
	idx := indexPath()
	if _, err := os.Stat(idx); err != nil {
		// 产物缺失通常是「忘了 npm run build」。返回可操作的提示而不是裸 404，
		// 否则排查方向很容易跑偏到路由或权限上。
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(
			"admin panel assets not found: " + idx + "\n\n" +
				"build the panel first:\n  cd web && npm install && npm run build\n"))
		return
	}

	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, idx)
}

// serveStaticFile 处理 /admin/ 下的静态资源请求。
//
// 命中真实文件则回传该文件；否则（前端路由如 /admin/accounts）回退到 index.html。
func (h *Handler) serveStaticFile(w http.ResponseWriter, r *http.Request) {
	root := webRoot()

	// path.Clean + 前导 "/" 把 "a/../../etc/passwd" 归一化为 "/etc/passwd"，
	// 再 TrimPrefix 得到相对路径；结合 filepath.Join(root, rel) 之后仍要复核
	// 结果落在 root 内，双重保险。
	rel := strings.TrimPrefix(r.URL.Path, "/admin/")
	cleaned := path.Clean("/" + rel)
	rel = strings.TrimPrefix(cleaned, "/")

	if rel == "" || rel == "index.html" {
		serveSPAIndex(w, r)
		return
	}

	full := filepath.Join(root, filepath.FromSlash(rel))
	if !withinRoot(root, full) {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		// 不是真实文件：可能是前端路由（/admin/accounts）。
		//
		// 但 assets/ 下的 404 必须保持 404 —— 那是真的资源丢失。若把缺失的 .js
		// 也回退成 HTML，浏览器会以 script 身份解析 HTML 并抛出难以理解的语法
		// 错误，把「文件没构建出来」伪装成「代码有 bug」。
		if isAssetRequest(rel) {
			http.NotFound(w, r)
			return
		}
		serveSPAIndex(w, r)
		return
	}

	// Vite 产物文件名带内容哈希，内容变则文件名变，可以安全地长期强缓存。
	if strings.HasPrefix(rel, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// favicon 等固定名字的文件：允许缓存但必须回源校验。
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}

	staticFS.ServeHTTP(w, r)
}

// withinRoot 确认 target 位于 root 之内，挡住符号链接与拼接逃逸。
func withinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isAssetRequest 判断该请求要的是构建产物（而非前端路由）。
//
// 依据两点：位于 assets/ 目录下，或带有静态资源扩展名。前端路由形如
// /admin/accounts、/admin/keys，不带扩展名。
func isAssetRequest(rel string) bool {
	if strings.HasPrefix(rel, "assets/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".js", ".mjs", ".css", ".map", ".json",
		".woff", ".woff2", ".ttf", ".otf", ".eot",
		".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".avif", ".ico":
		return true
	}
	return false
}
