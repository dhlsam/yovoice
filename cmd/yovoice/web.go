package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"yovoice/internal/desktop"
	"yovoice/internal/workbench"
)

// webOptions 是网页版服务的启动参数。
type webOptions struct {
	listen    string
	assets    string
	token     string
	tokenFile string
	cert      string
	key       string
}

// serveWeb 在服务器上提供网页版：浏览器直接访问界面与本地接口，不需要桌面宿主。
func serveWeb(ctx context.Context, w *workbench.Workbench, opts webOptions, out io.Writer) error {
	token, err := webToken(opts)
	if err != nil {
		return err
	}
	assets, err := webAssets(opts.assets)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	api := &desktop.Server{Workbench: w, Assets: assets, Secret: token, AllowRemote: true, BearerToken: token}
	server := &http.Server{Handler: &webAuth{inner: api, token: token, secure: opts.cert != ""}, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	stopped := make(chan struct{})
	finished := make(chan struct{})
	defer func() { close(stopped); <-finished }()
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			w.Close()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				_ = server.Close()
			}
		case <-stopped:
		}
	}()
	scheme := "http"
	if opts.cert != "" {
		scheme = "https"
	}
	base := scheme + "://" + listener.Addr().String()
	// 登录链接带上令牌，打开即可完成登录，不用手动输入。
	login := base + "/login?token=" + url.QueryEscape(token)
	if err = json.NewEncoder(out).Encode(map[string]string{"url": base, "loginUrl": login}); err != nil {
		return err
	}
	if opts.cert != "" {
		err = server.ServeTLS(listener, opts.cert, opts.key)
	} else {
		err = server.Serve(listener)
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// webToken 决定访问令牌：命令行参数优先，其次是令牌文件与环境变量，都没有就临时生成一个。
func webToken(opts webOptions) (string, error) {
	candidates := []string{opts.token}
	if opts.tokenFile != "" {
		b, err := os.ReadFile(opts.tokenFile)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, strings.TrimSpace(string(b)))
	}
	candidates = append(candidates, os.Getenv("YOVOICE_WEB_TOKEN"), os.Getenv("YOVOICE_API_TOKEN"))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if len(candidate) < 32 {
			return "", fmt.Errorf("网页版令牌至少 32 个字符")
		}
		return candidate, nil
	}
	// 没有配置令牌时临时生成一个：容器重启后令牌会变，用启动日志里的登录链接进入即可。
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// webAssets 找到网页版的前端产物目录：命令行参数与环境变量优先，都没有时用常见位置。
// 明确指定的目录里没有产物时报错，不静默回退到别处。
func webAssets(dir string) (string, error) {
	explicit := dir
	if explicit == "" {
		explicit = os.Getenv("YOVOICE_WEB_DIR")
	}
	if explicit != "" {
		if info, err := os.Stat(filepath.Join(explicit, "index.html")); err == nil && !info.IsDir() {
			return explicit, nil
		}
		return "", fmt.Errorf("找不到网页版前端产物 index.html：%s", explicit)
	}
	for _, candidate := range []string{"web/dist", "/app/web"} {
		if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("找不到网页版前端产物（index.html），请用 --web-dir 指定目录")
}

// webAuth 校验网页版的访问凭证：Cookie 或 Bearer 令牌；未认证时给出登录页，接口请求返回 401。
type webAuth struct {
	inner  http.Handler
	token  string
	secure bool
}

func (a *webAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.valid(r) {
		a.inner.ServeHTTP(w, r)
		return
	}
	switch {
	case r.URL.Path == "/login" && r.Method == "POST":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "请求无效。", 400)
			return
		}
		if !a.match(r.PostFormValue("token")) {
			a.denied(w)
			return
		}
		a.setCookie(w, r)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case r.URL.Path == "/login" && r.Method == "GET":
		if token := r.URL.Query().Get("token"); token != "" {
			if !a.match(token) {
				a.denied(w)
				return
			}
			a.setCookie(w, r)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginPage))
	case r.URL.Path == "/healthz":
		// 健康检查不带凭证，供容器编排探活使用。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case strings.HasPrefix(r.URL.Path, "/api/"), strings.HasPrefix(r.URL.Path, "/media/"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	default:
		// 未登录时只给登录页，不暴露界面资源。
		if filepath.Ext(r.URL.Path) != "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginPage))
	}
}

// valid 判断请求是否带有效凭证。
func (a *webAuth) valid(r *http.Request) bool {
	if cookie, err := r.Cookie("vw-" + a.token[:12]); err == nil && a.match(cookie.Value) {
		return true
	}
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return a.match(strings.TrimPrefix(header, "Bearer "))
	}
	return false
}

func (a *webAuth) match(value string) bool {
	return subtle.ConstantTimeCompare([]byte(value), []byte(a.token)) == 1
}

// setCookie 写入登录凭证：HttpOnly 禁止脚本读取，SameSite=Lax 挡住跨站请求。
func (a *webAuth) setCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "vw-" + a.token[:12], Value: a.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure, MaxAge: 30 * 24 * 3600})
}

// denied 提示令牌不对，页面不区分令牌长度等细节。
func (a *webAuth) denied(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(loginPage))
}

// loginPage 是登录页：只有一个令牌输入框，样式与桌面界面的深色主题一致。
const loginPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>yovoice</title>
<style>
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #14110f; color: #f3ece2;
         font-family: system-ui, -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
  form { width: min(360px, 90vw); background: #1e1a17; border: 1px solid #332c26; border-radius: 14px; padding: 28px; box-sizing: border-box; }
  h1 { margin: 0 0 6px; font-size: 20px; }
  p { margin: 0 0 18px; color: #b3a596; font-size: 13px; line-height: 1.7; }
  input { width: 100%; padding: 10px 12px; border-radius: 8px; border: 1px solid #3a322b; background: #14110f; color: #f3ece2; font-size: 14px; box-sizing: border-box; }
  button { margin-top: 14px; width: 100%; padding: 10px 12px; border-radius: 8px; border: 0; background: #d9822b; color: #1a1410; font-weight: 600; font-size: 14px; cursor: pointer; }
  .hint { margin: 14px 0 0; font-size: 12px; color: #8d8175; }
</style>
</head>
<body>
<form method="post" action="/login">
  <h1>yovoice</h1>
  <p>请输入访问令牌登录。令牌由服务端配置（环境变量 YOVOICE_WEB_TOKEN），也可直接打开启动日志中的登录链接。</p>
  <input type="password" name="token" placeholder="访问令牌" autocomplete="current-password" autofocus required>
  <button type="submit">登录</button>
  <p class="hint">Enter the access token to continue.</p>
</form>
</body>
</html>
`
