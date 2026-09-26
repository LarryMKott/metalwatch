package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func webuiTestFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<html><body>webui-index</body></html>")},
		"favicon.ico":      {Data: []byte("icon-bytes")},
		"assets/app.js":    {Data: []byte("console.log(1)")},
		"assets/app.css":   {Data: []byte("body{}")},
		"assets/index.css": {Data: []byte("html{}")},
	}
}

func newWebUITestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewWebUIHandler(webuiTestFS()).Register(r)
	return r
}

func doGet(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestWebUIIndexServed(t *testing.T) {
	r := newWebUITestEngine(t)
	for _, path := range []string{"/", "/index.html"} {
		w := doGet(r, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("%s: content-type = %q", path, ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: cache-control = %q, want no-cache", path, cc)
		}
		if body := w.Body.String(); body != "<html><body>webui-index</body></html>" {
			t.Fatalf("%s: body = %q", path, body)
		}
	}
}

func TestWebUIAssetsImmutableCache(t *testing.T) {
	r := newWebUITestEngine(t)
	w := doGet(r, "/assets/app.js")
	if w.Code != http.StatusOK || w.Body.String() != "console.log(1)" {
		t.Fatalf("assets/app.js: status = %d body = %q", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control = %q", cc)
	}
	if w := doGet(r, "/assets/missing.js"); w.Code != http.StatusNotFound {
		t.Fatalf("missing asset: status = %d, want 404", w.Code)
	}
}

func TestWebUIUnknownPathFallsBackToIndex(t *testing.T) {
	r := newWebUITestEngine(t)
	w := doGet(r, "/hosts/3/metrics")
	if w.Code != http.StatusOK || w.Body.String() != "<html><body>webui-index</body></html>" {
		t.Fatalf("SPA fallback broken: status = %d body = %q", w.Code, w.Body.String())
	}
}

func TestWebUIRootLevelFileServedViaFallback(t *testing.T) {
	r := newWebUITestEngine(t)
	w := doGet(r, "/favicon.ico")
	if w.Code != http.StatusOK || w.Body.String() != "icon-bytes" {
		t.Fatalf("favicon.ico: status = %d body = %q", w.Code, w.Body.String())
	}
}

func TestWebUIAPIPrefixNeverFallsBack(t *testing.T) {
	r := newWebUITestEngine(t)
	w := doGet(r, "/api/v1/nonexistent")
	if w.Code != http.StatusNotFound {
		t.Fatalf("/api 未知路径: status = %d, want 404", w.Code)
	}
	// API 契约不变：仍是 JSON 错误体（带 code），不是 HTML
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("/api 未知路径 content-type = %q, want json", ct)
	}
	if w := doGet(r, "/api/v1/agent/enroll"); w.Code != http.StatusNotFound {
		t.Fatalf("agent 未知路径: status = %d", w.Code)
	}
}

func TestWebUINonGETNeverServesHTML(t *testing.T) {
	r := newWebUITestEngine(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/anything", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST 未知路径: status = %d, want 404", w.Code)
	}
}
