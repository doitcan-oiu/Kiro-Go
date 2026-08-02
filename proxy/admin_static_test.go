package proxy

// 面板静态资源服务的回归测试。
//
// 重点覆盖三件容易在重构里悄悄坏掉、且坏掉后表现得像「前端 bug」的事：
//   1. 前端路由（/admin/accounts）必须回退到 index.html，否则刷新页面就 404；
//   2. assets/ 下的缺失文件必须保持 404，不能回退成 HTML（否则浏览器把 HTML
//      当 JS 解析，报出与真实原因毫无关系的语法错误）；
//   3. 路径穿越必须被挡住。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// withWebRoot 把面板根目录指向一个临时目录，并重置 webRoot 的 sync.Once，
// 使每个用例都能独立设置产物内容。
func withWebRoot(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("KIRO_WEB_ROOT", dir)

	// webRoot 用 sync.Once 缓存，测试里必须复位，否则第二个用例仍看到第一个的根目录。
	webRootOnce = sync.Once{}
	webRootDir = ""
	staticFS = nil
	t.Cleanup(func() {
		webRootOnce = sync.Once{}
		webRootDir = ""
		staticFS = nil
	})
}

// newTestPanel 建出一个最小产物目录：index.html + assets/app-abc123.js。
func newTestPanel(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><div id=app>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app-abc123.js"), []byte("export default 1"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	return dir
}

func doGet(h *Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	if strings.HasPrefix(target, "/admin/") && target != "/admin/" {
		h.serveStaticFile(rec, req)
	} else {
		h.serveAdminPage(rec, req)
	}
	return rec
}

func TestServeAdminPageReturnsIndex(t *testing.T) {
	withWebRoot(t, newTestPanel(t))
	h := &Handler{}

	rec := doGet(h, "/admin/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id=app") {
		t.Errorf("body = %q, want the SPA index", rec.Body.String())
	}
	// index.html 必须不缓存，否则发版后旧 HTML 会引用已删除的哈希资源而白屏。
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// 前端路由刷新：/admin/accounts 在磁盘上不存在，必须回退到 index.html。
func TestServeStaticFileFallsBackToIndexForClientRoutes(t *testing.T) {
	withWebRoot(t, newTestPanel(t))
	h := &Handler{}

	for _, route := range []string{"/admin/accounts", "/admin/settings", "/admin/keys/nested"} {
		rec := doGet(h, route)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", route, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "id=app") {
			t.Errorf("%s: body = %q, want the SPA index", route, rec.Body.String())
		}
	}
}

func TestServeStaticFileServesHashedAssetWithImmutableCache(t *testing.T) {
	withWebRoot(t, newTestPanel(t))
	h := &Handler{}

	rec := doGet(h, "/admin/assets/app-abc123.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "export default 1") {
		t.Errorf("body = %q, want the asset contents", body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}
}

// 缺失的构建产物必须是 404。若回退成 HTML，浏览器会以 script 身份解析 HTML
// 并抛出误导性的语法错误。
func TestServeStaticFileMissingAssetStays404(t *testing.T) {
	withWebRoot(t, newTestPanel(t))
	h := &Handler{}

	for _, target := range []string{
		"/admin/assets/gone-deadbeef.js",
		"/admin/assets/gone.css",
		"/admin/missing.js",
	} {
		rec := doGet(h, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body=%q)", target, rec.Code, rec.Body.String())
		}
	}
}

func TestServeStaticFileRejectsTraversal(t *testing.T) {
	root := newTestPanel(t)
	// 在产物目录之外放一个「机密」文件，确认它拿不到。
	secretDir := filepath.Dir(root)
	secret := filepath.Join(secretDir, "config.json")
	if err := os.WriteFile(secret, []byte(`{"password":"s3cr3t"}`), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	withWebRoot(t, root)
	h := &Handler{}

	for _, target := range []string{
		"/admin/../config.json",
		"/admin/assets/../../config.json",
		"/admin/..%2fconfig.json",
	} {
		rec := doGet(h, target)
		if strings.Contains(rec.Body.String(), "s3cr3t") {
			t.Errorf("%s: leaked the out-of-root file (status=%d)", target, rec.Code)
		}
	}
}

// 忘记构建时要给出可操作的提示，而不是让人以为是路由坏了。
func TestServeAdminPageWithoutBuildExplainsHow(t *testing.T) {
	withWebRoot(t, t.TempDir())
	h := &Handler{}

	rec := doGet(h, "/admin/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "npm run build") {
		t.Errorf("body = %q, want build instructions", body)
	}
}
