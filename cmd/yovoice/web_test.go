package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 网页版令牌：命令行优先，其次令牌文件与环境变量，都没有就临时生成一个。
func TestWebToken(t *testing.T) {
	t.Setenv("YOVOICE_WEB_TOKEN", "")
	t.Setenv("YOVOICE_API_TOKEN", "")
	generated, e := webToken(webOptions{})
	if e != nil || len(generated) < 32 {
		t.Fatal(generated, e)
	}
	if other, e := webToken(webOptions{}); e != nil || other == generated {
		t.Fatal("每次生成的令牌应不同")
	}
	t.Setenv("YOVOICE_WEB_TOKEN", strings.Repeat("c", 64))
	if token, e := webToken(webOptions{}); e != nil || token != strings.Repeat("c", 64) {
		t.Fatal(token, e)
	}
	if token, e := webToken(webOptions{token: strings.Repeat("d", 64)}); e != nil || token != strings.Repeat("d", 64) {
		t.Fatal("命令行令牌应优先于环境变量", token, e)
	}
	file := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(file, []byte(strings.Repeat("e", 64)+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if token, e := webToken(webOptions{tokenFile: file}); e != nil || token != strings.Repeat("e", 64) {
		t.Fatal(token, e)
	}
	if _, e := webToken(webOptions{token: "short"}); e == nil {
		t.Fatal("过短的令牌应报错")
	}
}

// 网页版前端目录：参数、环境变量、常见位置依次尝试，缺 index.html 时报错。
func TestWebAssets(t *testing.T) {
	t.Setenv("YOVOICE_WEB_DIR", "")
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0600); e != nil {
		t.Fatal(e)
	}
	if got, e := webAssets(dir); e != nil || got != dir {
		t.Fatal(got, e)
	}
	t.Setenv("YOVOICE_WEB_DIR", dir)
	if got, e := webAssets(""); e != nil || got != dir {
		t.Fatal(got, e)
	}
	if _, e := webAssets(filepath.Join(dir, "missing")); e == nil {
		t.Fatal("明确指定的目录缺少前端产物时应报错")
	}
	t.Setenv("YOVOICE_WEB_DIR", "")
	if _, e := webAssets(""); e == nil {
		t.Fatal("找不到前端产物时应报错")
	}
}
