// Package server 提供本地控制台的 HTTP 服务。
//
// 安全边界：只监听 127.0.0.1、校验 Host 头防止 DNS 重绑定、
// 所有写接口要求携带本机令牌。控制台能改配置并拉起进程，
// 这三道防线缺一不可。
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openfrees/frp-ngrok/internal/accesslog"
	"github.com/openfrees/frp-ngrok/internal/apitypes"
	"github.com/openfrees/frp-ngrok/internal/hotkey"
	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/portsite"
	"github.com/openfrees/frp-ngrok/internal/store"
	"github.com/openfrees/frp-ngrok/internal/supervisor"
	"github.com/openfrees/frp-ngrok/web"
)

// Server 是控制台服务实例。
type Server struct {
	sup     *supervisor.Supervisor
	hotkeys *hotkey.Manager
	access  *accesslog.Proxy
	sites   *portsite.Manager
	token   string
	port    int
	version string
	stamp   int64
}

// New 创建控制台服务。
func New(sup *supervisor.Supervisor, port int, version string) (*Server, error) {
	token, err := LoadOrCreateToken()
	if err != nil {
		return nil, err
	}
	s := &Server{
		sup:     sup,
		access:  accesslog.New(),
		sites:   portsite.New(),
		token:   token,
		port:    port,
		version: version,
		stamp:   binaryStamp(),
	}
	store.SetFrpcPortMapper(store.AccessLogFrpcPort)
	dispatchHotkey := func(item store.HotkeyItem) {
		log.Printf("[快捷键] 触发 %q（%s）", item.Name, item.Combo)
		if err := hotkey.Execute(item); err != nil {
			log.Printf("[快捷键] 执行 %q 失败: %v", item.Name, err)
		}
	}
	s.hotkeys = hotkey.NewManager(dispatchHotkey, hotkey.ShowPalette)
	// 服务重启后按上次的配置恢复快捷键注册；失败只记日志，不影响控制台本身。
	if cfg, err := store.LoadHotkeys(); err != nil {
		log.Printf("[快捷键] 读取配置失败: %v", err)
	} else if cfg.Enabled {
		if err := s.hotkeys.Apply(cfg); err != nil {
			log.Printf("[快捷键] 启用失败: %v", err)
		}
	}
	if cfg, err := store.LoadAccessLog(); err != nil {
		log.Printf("[访问日志] 读取配置失败: %v", err)
	} else if cfg.Enabled {
		if err := s.syncAccessLog(true); err != nil {
			log.Printf("[访问日志] 启用失败: %v", err)
		}
	}
	if cfg, err := store.LoadPortSites(); err != nil {
		log.Printf("[端口管理] 读取配置失败: %v", err)
	} else if cfg.Enabled {
		s.restorePortSites()
	}
	return s, nil
}

// binaryStamp 记录本进程所用程序文件的修改时间。
//
// 程序被新版本覆盖后该值不再匹配，启动器据此知道要重启服务。
func binaryStamp() int64 {
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}

// Token 返回本机访问令牌。
func (s *Server) Token() string { return s.token }

// Handler 组装全部路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 存活探测：不含敏感信息，供启动器判断服务是否就绪、是否需要升级重启。
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, apitypes.Ping{
			OK:          true,
			Version:     s.version,
			BinaryStamp: s.stamp,
		})
	})

	mux.HandleFunc("GET /api/state", s.auth(s.handleState))
	mux.HandleFunc("GET /api/logs", s.auth(s.handleLogs))
	mux.HandleFunc("GET /api/deploy-script", s.auth(s.handleDeployScript))
	// 只读预览：按表单参数算脚本，不写任何档案
	mux.HandleFunc("POST /api/profiles", s.auth(s.handleCreateProfile))
	mux.HandleFunc("GET /api/profiles/{id}/deploy-plan", s.auth(s.handleProfileDeployPlan))
	mux.HandleFunc("PUT /api/profiles/{id}", s.auth(s.handleUpdateProfile))
	mux.HandleFunc("POST /api/profiles/{id}/activate", s.auth(s.handleActivateProfile))
	mux.HandleFunc("DELETE /api/profiles/{id}", s.auth(s.handleDeleteProfile))

	mux.HandleFunc("POST /api/tunnels", s.auth(s.handleCreateTunnel))
	mux.HandleFunc("DELETE /api/tunnels/{port}", s.auth(s.handleDeleteTunnel))

	mux.HandleFunc("POST /api/client/{action}", s.auth(s.handleClientAction))
	mux.HandleFunc("POST /api/autostart", s.auth(s.handleAutostart))
	mux.HandleFunc("GET /api/prefs", s.auth(s.handleGetPrefs))
	mux.HandleFunc("PUT /api/prefs", s.auth(s.handlePutPrefs))
	mux.HandleFunc("POST /api/check/server", s.auth(s.handleCheckServer))
	mux.HandleFunc("POST /api/check/tunnels", s.auth(s.handleCheckTunnels))
	mux.HandleFunc("POST /api/service/stop", s.auth(s.handleServiceStop))

	mux.HandleFunc("GET /api/plugins/hotkeys", s.auth(s.handleGetHotkeys))
	mux.HandleFunc("PUT /api/plugins/hotkeys", s.auth(s.handlePutHotkeys))
	mux.HandleFunc("POST /api/plugins/hotkeys/run", s.auth(s.handleRunHotkey))
	mux.HandleFunc("GET /api/plugins/access-log", s.auth(s.handleGetAccessLog))
	mux.HandleFunc("PUT /api/plugins/access-log", s.auth(s.handlePutAccessLog))
	mux.HandleFunc("PUT /api/plugins/access-log/tunnels/{port}", s.auth(s.handlePutAccessLogTunnel))
	mux.HandleFunc("GET /api/plugins/access-log/tunnels/{port}/log", s.auth(s.handleGetAccessLogFile))
	mux.HandleFunc("DELETE /api/plugins/access-log/tunnels/{port}/log", s.auth(s.handleDeleteAccessLogFile))
	mux.HandleFunc("GET /api/plugins/port-sites", s.auth(s.handleGetPortSites))
	mux.HandleFunc("PUT /api/plugins/port-sites", s.auth(s.handlePutPortSites))
	mux.HandleFunc("POST /api/plugins/port-sites/sites", s.auth(s.handleCreatePortSite))
	mux.HandleFunc("POST /api/plugins/port-sites/sites/{port}/start", s.auth(s.handleStartPortSite))
	mux.HandleFunc("POST /api/plugins/port-sites/sites/{port}/stop", s.auth(s.handleStopPortSite))
	mux.HandleFunc("DELETE /api/plugins/port-sites/sites/{port}", s.auth(s.handleDeletePortSite))
	mux.HandleFunc("POST /api/plugins/port-sites/pick-dir", s.auth(s.handlePickPortSiteDir))
	mux.HandleFunc("GET /api/plugins/port-sites/sites/{port}/files", s.auth(s.handleListPortSiteFiles))
	mux.HandleFunc("POST /api/plugins/port-sites/sites/{port}/files", s.auth(s.handleUploadPortSiteFile))
	mux.HandleFunc("DELETE /api/plugins/port-sites/sites/{port}/files/{name}", s.auth(s.handleDeletePortSiteFile))
	mux.HandleFunc("POST /api/plugins/port-sites/sites/{port}/open", s.auth(s.handleOpenPortSiteFolder))

	static, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		log.Printf("加载内置页面失败: %v", err)
	} else {
		files := http.FileServer(http.FS(static))
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 页面资源跟着二进制发布，复用旧缓存等同于升级后继续运行旧前端。
			w.Header().Set("Cache-Control", "no-store")
			files.ServeHTTP(w, r)
		}))
	}

	return s.guardLocal(mux)
}

// Listen 启动监听。端口被占用说明已有实例在跑，直接报错退出。
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return nil, fmt.Errorf("端口 %d 无法监听（可能已有服务在跑）: %w", s.port, err)
	}
	return ln, nil
}

// guardLocal 拒绝非本机 Host 的请求，阻断 DNS 重绑定攻击。
func (s *Server) guardLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch host {
		case "127.0.0.1", "localhost", "[::1]", "::1":
		default:
			http.Error(w, "仅允许通过 127.0.0.1 访问", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// auth 校验访问令牌，支持请求头与查询参数两种携带方式。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": store.T("Invalid access token. Double-click the frp-ngrok launcher to open this page again.", "访问令牌无效，请重新双击「frp-ngrok」启动器打开页面"),
			})
			return
		}
		next(w, r)
	}
}

// LoadOrCreateToken 读取本机令牌，不存在则生成。
func LoadOrCreateToken() (string, error) {
	if b, err := os.ReadFile(paths.TokenFile()); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= 32 {
			return t, nil
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := os.MkdirAll(paths.AppDir(), 0o755); err != nil {
		return "", err
	}
	// 令牌等同于控制台的钥匙，仅当前用户可读。
	if err := os.WriteFile(paths.TokenFile(), []byte(token), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("写响应失败: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("请求格式有误: %w", err)
	}
	return nil
}

// Serve 启动 HTTP 服务并阻塞。
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// 连通性探测最长约 15 秒，写超时留出余量。
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return srv.Serve(ln)
}
