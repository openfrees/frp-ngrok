// Package portsite 在守护进程里为每个本机站点 Listen 127.0.0.1:{port}。
//
// 不做子进程、不做路径分流。插件关闭、站点删除或进程退出时必须释放 listener。
package portsite

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/openfrees/frp-ngrok/internal/store"
)

// Manager 管理本插件拉起的全部 HTTP 监听。
type Manager struct {
	mu    sync.Mutex
	sites map[int]*running
}

type running struct {
	port int
	root string
	ln   net.Listener
	srv  *http.Server
	done chan struct{}
}

// New 创建一个尚未监听的管理器。
func New() *Manager {
	return &Manager{sites: map[int]*running{}}
}

// Start 在 127.0.0.1:{port} 上托管 root。已在跑且根目录相同则直接返回。
func (m *Manager) Start(port int, root string) error {
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("站点目录不合法: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sites[port]; ok {
		if cur.root == abs {
			return nil
		}
		if err := cur.stop(); err != nil {
			return err
		}
		delete(m.sites, port)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("端口 %d 已被本机其他进程占用", port)
	}
	srv := &http.Server{
		Handler:           NewFileHandler(abs),
		ReadHeaderTimeout: 10 * time.Second,
	}
	item := &running{port: port, root: abs, ln: ln, srv: srv, done: make(chan struct{})}
	m.sites[port] = item
	go func() {
		_ = srv.Serve(ln)
		close(item.done)
	}()
	return nil
}

// Stop 关掉某个端口的监听。未启动时直接返回。
func (m *Manager) Stop(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.sites[port]
	if !ok {
		return nil
	}
	err := item.stop()
	delete(m.sites, port)
	return err
}

// StopAll 关掉本插件拉起的全部监听。
func (m *Manager) StopAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for port, item := range m.sites {
		if err := item.stop(); err != nil && first == nil {
			first = err
		}
		delete(m.sites, port)
	}
	return first
}

// Running 表示这个端口当前由本管理器在听。
func (m *Manager) Running(port int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sites[port]
	return ok
}

func (r *running) stop() error {
	if r.srv == nil {
		return nil
	}
	err := r.srv.Close()
	if r.done != nil {
		<-r.done
	}
	r.ln = nil
	r.srv = nil
	return err
}

// NewFileHandler 按静态站规则提供站点文件，并拦截路径穿越。
func NewFileHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "只支持 GET", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if rel == "." {
			rel = ""
		}
		var full string
		var err error
		if rel == "" {
			full, err = filepath.Abs(root)
		} else {
			full, err = store.SafeJoinSite(root, rel)
		}
		if err != nil {
			http.Error(w, "路径超出站点目录", http.StatusForbidden)
			return
		}
		fi, err := os.Stat(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if fi.IsDir() {
			for _, name := range []string{"index.html", "index.htm"} {
				idx := filepath.Join(full, name)
				if s, err := os.Stat(idx); err == nil && !s.IsDir() {
					http.ServeFile(w, r, idx)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		if strings.EqualFold(filepath.Ext(full), ".md") {
			serveMarkdown(w, r, full)
			return
		}
		http.ServeFile(w, r, full)
	})
}

func serveMarkdown(w http.ResponseWriter, r *http.Request, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe()),
	)
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		http.Error(w, "Markdown 渲染失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 48em; margin: 2rem auto; padding: 0 1.25rem; color: #161a17; line-height: 1.7; }
  pre { background: #eff2ec; padding: 12px 14px; overflow: auto; border-radius: 6px; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  table { border-collapse: collapse; }
  th, td { border: 1px solid #dfe3da; padding: 6px 10px; }
</style>
</head>
<body>
%s
</body>
</html>
`, filepath.Base(path), buf.String())
}
