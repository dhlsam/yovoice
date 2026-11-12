package desktop

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"yovoice/internal/workbench"
)

// 网页版部署允许远程访问：令牌或登录 Cookie 都能通过，没有凭证或跨站请求仍然拒绝。
func TestRemoteAuthorization(t *testing.T) {
	w, e := workbench.New(t.TempDir())
	must(t, e)
	defer w.Close()
	secret := strings.Repeat("b", 64)
	server := httptest.NewServer(&Server{Workbench: w, Assets: t.TempDir(), Secret: secret, AllowRemote: true, BearerToken: secret})
	defer server.Close()
	request := func(cookie, bearer, origin string) *http.Request {
		r, e := http.NewRequest("GET", server.URL+"/", nil)
		must(t, e)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "vw-" + secret[:12], Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	for _, r := range []*http.Request{request("", secret, ""), request(secret, "", "")} {
		res, e := server.Client().Do(r)
		must(t, e)
		res.Body.Close()
		if res.StatusCode == http.StatusForbidden {
			t.Fatal("有效凭证被拒绝")
		}
	}
	for _, r := range []*http.Request{request("", "", ""), request("", secret, "https://untrusted.invalid")} {
		res, e := server.Client().Do(r)
		must(t, e)
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatal(res.StatusCode)
		}
	}
}

// 网页版入口页要引入服务标记脚本，前端据此改用 HTTP 接口；标记脚本本身是同源文件。
func TestServerMarker(t *testing.T) {
	w, e := workbench.New(t.TempDir())
	must(t, e)
	defer w.Close()
	assets := t.TempDir()
	must(t, os.WriteFile(filepath.Join(assets, "index.html"), []byte("<html><head></head><body></body></html>"), 0600))
	secret := strings.Repeat("c", 64)
	server := httptest.NewServer(&Server{Workbench: w, Assets: assets, Secret: secret, AllowRemote: true})
	defer server.Close()
	r, e := http.NewRequest("GET", server.URL+"/", nil)
	must(t, e)
	r.AddCookie(&http.Cookie{Name: "vw-" + secret[:12], Value: secret})
	res, e := server.Client().Do(r)
	must(t, e)
	defer res.Body.Close()
	body, e := io.ReadAll(res.Body)
	must(t, e)
	if !strings.Contains(string(body), serverMarkerPath) {
		t.Fatal(string(body))
	}
	res2, e := http.NewRequest("GET", server.URL+"/"+serverMarkerPath, nil)
	must(t, e)
	res2.AddCookie(&http.Cookie{Name: "vw-" + secret[:12], Value: secret})
	res, e = server.Client().Do(res2)
	must(t, e)
	defer res.Body.Close()
	marker, e := io.ReadAll(res.Body)
	must(t, e)
	if !strings.Contains(string(marker), "__yovoiceServer") {
		t.Fatal(string(marker))
	}
}
