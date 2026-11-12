// Package desktop 为原生宿主提供本机 HTTP 服务：界面资源、RPC、状态事件与媒体文件。
package desktop

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"yovoice/internal/diag"
	"yovoice/internal/msg"
	"yovoice/internal/workbench"
)

type Server struct {
	Workbench      *workbench.Workbench
	Assets, Secret string
	Shutdown       func()
	// LocalAPI 为空时不提供本机接口设置。
	LocalAPI *LocalAPI
	// AllowRemote 允许来自非本机地址的请求，供网页版部署在服务器上使用；凭证校验仍然生效。
	AllowRemote bool
	// BearerToken 供命令行客户端用 Authorization: Bearer 调用，为空时不接受这种凭证。
	BearerToken string
	// calls 允许 RPC 并发执行；关闭时独占，等待在途调用结束后再释放资源。
	calls   sync.RWMutex
	once    sync.Once
	closing context.Context
	stop    context.CancelFunc
}

// lifetime 在关闭时取消，用于中止转码等耗时调用，避免拖住退出。
func (s *Server) lifetime() context.Context {
	s.once.Do(func() { s.closing, s.stop = context.WithCancel(context.Background()) })
	return s.closing
}

// guard 在读锁内执行一次调用，并把 panic 转成错误返回：任一方法崩溃都不能让读锁悬挂，
// 否则关闭时的写锁和之后的所有请求都会卡死。
func (s *Server) guard(method string, fn func() (any, error)) (result any, err error) {
	s.calls.RLock()
	defer s.calls.RUnlock()
	defer func() {
		if r := recover(); r != nil {
			diag.Log(s.Workbench.Store.Root, "rpc.panic", "method", method, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			result, err = nil, msg.Err(msg.ErrUnknown, msg.Params{"detail": fmt.Sprintf("panic: %v", r)})
		}
	}()
	return fn()
}

// drainTimeout 是关闭时等待在途调用结束的上限：某个调用不响应取消时记录日志后继续关闭，不必靠宿主强杀。
var drainTimeout = 5 * time.Second

// Close 关闭服务：先取消在途调用（包括正在复制数据目录的迁移），等它们结束后再释放资源。
// /shutdown 与信号、标准输入关闭的退出路径共用；幂等。
func (s *Server) Close() {
	s.lifetime()
	s.stop()
	if s.drain() {
		defer s.calls.Unlock()
	}
	if s.LocalAPI != nil {
		s.LocalAPI.Close()
	}
	s.Workbench.Close()
}

// drain 在 drainTimeout 内等待在途调用全部结束并拿到写锁；超时返回假，调用方不持锁继续关闭。
func (s *Server) drain() bool {
	deadline := time.Now().Add(drainTimeout)
	for !s.calls.TryLock() {
		if time.Now().After(deadline) {
			diag.Log(s.Workbench.Store.Root, "rpc.drain_timeout", "timeout_s", int(drainTimeout.Seconds()))
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// call 先处理本机接口设置，其余方法交给 Call。
func (s *Server) call(ctx context.Context, method string, data json.RawMessage) (any, error) {
	if !strings.HasPrefix(method, "localApi.") {
		return Call(ctx, s.Workbench, method, data)
	}
	if s.LocalAPI == nil {
		return nil, msg.Err(msg.ErrMethodUnsupported, nil)
	}
	var input struct {
		Enabled bool `json:"enabled"`
		Port    int  `json:"port"`
	}
	if len(data) > 0 && string(data) != "null" {
		if e := json.Unmarshal(data, &input); e != nil {
			return nil, msg.Err(msg.ErrRequestInvalid, nil)
		}
	}
	return s.LocalAPI.call(method, input.Enabled, input.Port)
}

// serverMarkerPath 是网页版标记脚本的路径：前端读到它就知道页面由 Go 服务提供。
const serverMarkerPath = "__yovoice-server.js"

// serverMarkerScript 告诉前端页面由服务提供，并带上运行平台：
// Linux 与 macOS 的可选推理设备不同，桌面宿主也是用这个标记区分的。
var serverMarkerScript = func() string {
	script := "window.__yovoiceServer=true;\n"
	switch runtime.GOOS {
	case "linux":
		script += "window.__workbenchPlatform='linux';\n"
	case "darwin":
		script += "window.__workbenchPlatform='macos';\n"
	}
	return script
}()

// cookieName 返回本机凭证的 Cookie 名：用密钥前 12 位区分实例，避免共用同一个 Cookie。
func (s *Server) cookieName() string { return "vw-" + s.Secret[:12] }

// authorized 校验请求来源与凭证：本机模式只接受 127.0.0.1 上的 Cookie；
// 网页版部署允许任意地址，并额外接受 Authorization: Bearer 令牌，方便命令行客户端调用。
func (s *Server) authorized(r *http.Request) bool {
	if !s.AllowRemote && !strings.HasPrefix(r.Host, "127.0.0.1:") {
		return false
	}
	if !s.credential(r) {
		return false
	}
	return s.originAllowed(r)
}

// credential 判断请求是否带有效凭证：Bearer 令牌或登录 Cookie。
func (s *Server) credential(r *http.Request) bool {
	if s.BearerToken != "" {
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, "Bearer ")), []byte(s.BearerToken)) == 1 {
				return true
			}
		}
	}
	cookie, e := r.Cookie(s.cookieName())
	return e == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.Secret)) == 1
}

// originAllowed 只放行同源请求：浏览器发起的跨站请求带 Origin，直接拒绝以防 CSRF。
// 网页版部署在反向代理后面时按转发的主机名判断；代理终止 TLS 时来源是 https，两种协议都放行。
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "http://"+r.Host {
		return true
	}
	if !s.AllowRemote {
		return false
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return origin == "http://"+host || origin == "https://"+host
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "Forbidden", 403)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	limit := int64(28 << 20)
	if r.URL.Path == "/api/export-audio" {
		// 导出的长音频直接写入临时文件，按 WAV 的 4 GB 上限放宽。
		limit = 4 << 30
	}
	if r.URL.Path == "/api/transcribe" {
		// 转写可以上传视频，文件直接写入临时文件；比转写上限多留一点，超出时由转写返回明确的错误。
		limit = workbench.MaxTranscribeUpload + 1<<20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	switch {
	case r.URL.Path == "/api/export-audio" && r.Method == "POST":
		// loudness 为目标响度（LUFS），不带时原样保存；带了但不是数字视为请求无效。
		var loudness float64
		if raw := r.URL.Query().Get("loudness"); raw != "" {
			var e error
			if loudness, e = strconv.ParseFloat(raw, 64); e != nil {
				http.Error(w, "请求无效。", 400)
				return
			}
		}
		reply, err := s.guard("export-audio", func() (any, error) {
			id, normalized, err := s.Workbench.SaveExport(r.Body, loudness)
			reply := map[string]any{"id": id}
			if normalized != nil {
				reply["normalized"] = normalized
			}
			return reply, err
		})
		if err != nil {
			reply = map[string]any{"error": msg.Encode(err)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	case r.URL.Path == "/api/transcribe" && r.Method == "POST":
		// 转写上传走原始请求体，不经 base64 消息，视频等大文件也能上传；name 是原文件名。
		started := time.Now()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		release := context.AfterFunc(s.lifetime(), func() {
			cancel()
			_ = r.Body.Close()
		})
		defer release()
		result, err := s.guard("speech.transcribe", func() (any, error) {
			selection, err := s.Workbench.PrepareTranscribeSpeech(ctx, r.URL.Query().Get("name"), r.Body)
			if selection != nil {
				return selection, err
			}
			return true, err
		})
		diag.Log(s.Workbench.Store.Root, "speech.transcribe", "elapsed_ms", time.Since(started).Milliseconds(), "error", diag.Error(err))
		reply := map[string]any{"result": result}
		if err != nil {
			reply = map[string]any{"error": msg.Encode(err)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	case r.URL.Path == "/api/call" && r.Method == "POST":
		var message struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Data   json.RawMessage `json:"data"`
		}
		if e := json.NewDecoder(r.Body).Decode(&message); e != nil {
			http.Error(w, "请求无效。", 400)
			return
		}
		// 调用之间互不阻塞：导入音色时仍可保存作品；数据一致性由 Workbench 保证。
		// 宿主断开或服务关闭时取消调用。
		ctx, cancel := context.WithCancel(r.Context())
		release := context.AfterFunc(s.lifetime(), cancel)
		result, err := s.guard(message.Method, func() (any, error) { return s.call(ctx, message.Method, message.Data) })
		release()
		cancel()
		reply := map[string]any{"id": message.ID, "result": result}
		if err != nil {
			reply = map[string]any{"id": message.ID, "error": msg.Encode(err)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	case r.URL.Path == "/api/state-events" && r.Method == "GET":
		ch, unsubscribe := s.Workbench.Store.Subscribe()
		defer unsubscribe()
		audio, unsubscribeAudio := s.Workbench.SubscribeAudio()
		defer unsubscribeAudio()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		controller := http.NewResponseController(w)
		writeEvent := func(data []byte) bool {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return false
			}
			return controller.Flush() == nil
		}
		var stream stateStream
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-audio:
				data, e := json.Marshal(event)
				if e != nil {
					return
				}
				if !writeEvent(data) {
					return
				}
			case <-ch:
				snapshot, e := s.Workbench.Store.ReadJSON()
				if e != nil {
					return
				}
				b, e := stream.next(snapshot)
				if e != nil {
					return
				}
				if b == nil {
					continue
				}
				if !writeEvent(b) {
					return
				}
			}
		}
	case r.URL.Path == "/api/export-file" && r.Method == "GET":
		// 网页版把导出结果交给浏览器下载：id 来自导出接口的回复。
		path, e := s.Workbench.ExportPath(r.URL.Query().Get("id"))
		if e != nil {
			http.Error(w, "文件不存在。", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", exportContentType(path))
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(struct{ http.ResponseWriter }{w}, r, path)
	case strings.HasPrefix(r.URL.Path, "/media/") && (r.Method == "GET" || r.Method == "HEAD"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/media/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		path, e := s.Workbench.Store.MediaPath(parts[0], parts[1])
		if e != nil {
			http.Error(w, "无效路径", 400)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		// 音频本就在本机磁盘上，禁止 WebView 再缓存一份；未压缩的 wav 会让 web-cache 不断膨胀。
		w.Header().Set("Cache-Control", "no-store")
		// 隐藏 io.ReaderFrom，避免 Windows sendfile 与连接后台读在 Go 1.26 中的数据竞争。
		http.ServeFile(struct{ http.ResponseWriter }{w}, r, path)
	case r.URL.Path == "/shutdown" && r.Method == "POST":
		s.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		if s.Shutdown != nil {
			go s.Shutdown()
		}
	default:
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if s.AllowRemote && r.URL.Path == "/"+serverMarkerPath {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(serverMarkerScript))
			return
		}
		if s.AllowRemote && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			s.serveIndex(w)
			return
		}
		http.FileServer(http.Dir(s.Assets)).ServeHTTP(w, r)
	}
}

// exportContentType 按导出文件的扩展名给出类型，浏览器据此保存文件。
func exportContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	}
	return "audio/wav"
}

// serveIndex 提供网页版入口页：注入服务标记，前端据此改用 HTTP 接口，而不是浏览器预览的模拟后端。
// 页面 CSP 只允许同源脚本，所以标记用同源脚本文件引入，不用内联代码。
func (s *Server) serveIndex(w http.ResponseWriter) {
	page, err := os.ReadFile(filepath.Join(s.Assets, "index.html"))
	if err != nil {
		http.Error(w, "网页版前端产物缺失。", http.StatusInternalServerError)
		return
	}
	marker := []byte(`<script src="./` + serverMarkerPath + `"></script>`)
	if bytes.Contains(page, []byte("</head>")) {
		page = bytes.Replace(page, []byte("</head>"), append(marker, []byte("</head>")...), 1)
	} else {
		page = append(page, marker...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}
