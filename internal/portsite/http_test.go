package portsite

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileHandlerServesIndexHTML(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.html", "<html>hello-5555</html>")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	NewFileHandler(root).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("首页应 200，实得 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "hello-5555") {
		t.Fatalf("应返回 index.html: %s", w.Body.String())
	}
}

func TestFileHandlerFallsBackToIndexHTM(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.htm", "<html>legacy</html>")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	NewFileHandler(root).ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "legacy") {
		t.Fatalf("应回退 index.htm: %d %s", w.Code, w.Body.String())
	}
}

func TestFileHandlerRendersMarkdown(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "readme.md", "# 标题\n\n一段 **粗体**。")

	req := httptest.NewRequest(http.MethodGet, "/readme.md", nil)
	w := httptest.NewRecorder()
	NewFileHandler(root).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("markdown 应 200，实得 %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("markdown 应渲染成 HTML，Content-Type=%q", ct)
	}
	body := w.Body.String()
	if strings.Contains(body, "# 标题") {
		t.Fatal("不应把 markdown 源码原样吐出")
	}
	if !strings.Contains(body, "<h1") || !strings.Contains(body, "粗体") {
		t.Fatalf("应渲染 GFM HTML: %s", body)
	}
}

func TestFileHandlerRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ok.txt", "inside")
	secret := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/../secret.txt", nil)
	w := httptest.NewRecorder()
	NewFileHandler(root).ServeHTTP(w, req)
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "outside") {
		t.Fatal("路径穿越读到了站点外的文件")
	}
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "outside") {
		t.Fatal("不应读出站点根目录")
	}
	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
		t.Fatalf("穿越应拒绝，实得 %d %s", w.Code, w.Body.String())
	}
}

func TestManagerStartStopReleasesPort(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.html", "up")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	m := New()
	t.Cleanup(func() { _ = m.StopAll() })
	if err := m.Start(port, root); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if !m.Running(port) {
		t.Fatal("启动后应标记运行中")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		t.Fatalf("本机访问失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "up" {
		t.Fatalf("应读到站点内容: %d %s", resp.StatusCode, body)
	}

	if err := m.Stop(port); err != nil {
		t.Fatal(err)
	}
	if m.Running(port) {
		t.Fatal("停止后不应仍标记运行中")
	}

	probe, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("停止后端口应释放: %v", err)
	}
	_ = probe.Close()
}

func TestManagerStopAllReleasesEveryListener(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.html", "x")

	ports := make([]int, 2)
	for i := range ports {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
	}

	m := New()
	for _, p := range ports {
		if err := m.Start(p, root); err != nil {
			t.Fatalf("启动 %d 失败: %v", p, err)
		}
	}
	if err := m.StopAll(); err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			t.Fatalf("StopAll 后端口 %d 未释放: %v", p, err)
		}
		_ = ln.Close()
	}
}
