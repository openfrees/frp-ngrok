// 访问日志插件的配置、按隧道开关与日志文件。
//
// 配置与档案/隧道数据分开存放（~/.frpanel/plugins/access-log/），
// 插件关掉后只是不再记录、不再改写 frpc 端口，已有日志文件仍留着直到用户删除。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

// AccessLogTunnel 是某条隧道在访问日志插件里的开关。
type AccessLogTunnel struct {
	Enabled bool `json:"enabled"`
}

// AccessLogConfig 是访问日志插件的完整配置。
type AccessLogConfig struct {
	Enabled bool `json:"enabled"`
	// ListenPort 是本机拦截器端口；写进 frpc.toml 的 localPort。
	ListenPort int `json:"listenPort"`
	// Tunnels 以 "档案名:端口" 为键。缺省视为开启——新建隧道默认记日志。
	Tunnels map[string]AccessLogTunnel `json:"tunnels"`
}

// AccessLogKey 是一条隧道在插件配置里的键。
func AccessLogKey(profileID string, port int) string {
	return fmt.Sprintf("%s:%d", profileID, port)
}

// AccessLogPath 返回某条隧道的访问日志文件路径。
func AccessLogPath(profileID string, port int) string {
	return paths.AccessLogFile(profileID, port)
}

// TunnelLogEnabled 判断这条隧道要不要记访问日志。没写过的默认开。
func TunnelLogEnabled(cfg AccessLogConfig, profileID string, port int) bool {
	if cfg.Tunnels == nil {
		return true
	}
	item, ok := cfg.Tunnels[AccessLogKey(profileID, port)]
	if !ok {
		return true
	}
	return item.Enabled
}

// AccessLogFrpcPort 给出写进 frpc.toml 的 localPort。
// 插件开启且这条隧道在记日志时，指向拦截器；否则仍是用户的本机端口。
func AccessLogFrpcPort(p Profile, t Tunnel) int {
	cfg, err := LoadAccessLog()
	if err != nil || !cfg.Enabled || cfg.ListenPort <= 0 {
		return t.LocalPort
	}
	if !TunnelLogEnabled(cfg, p.Name, t.LocalPort) {
		return t.LocalPort
	}
	return cfg.ListenPort
}

// LoadAccessLog 读取访问日志配置；文件不存在时返回空配置。
func LoadAccessLog() (AccessLogConfig, error) {
	var c AccessLogConfig
	data, err := os.ReadFile(paths.AccessLogConfigFile())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(data) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("解析访问日志配置失败: %w", err)
	}
	return c, nil
}

// SaveAccessLog 写入访问日志配置。
func SaveAccessLog(c AccessLogConfig) error {
	if err := os.MkdirAll(paths.AccessLogDir(), 0o755); err != nil {
		return err
	}
	if c.Tunnels == nil {
		c.Tunnels = map[string]AccessLogTunnel{}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(paths.AccessLogConfigFile(), append(data, '\n'), 0o600)
}

// EnsureTunnelLogDefault 给新建隧道显式写下「默认开启」。
func EnsureTunnelLogDefault(profileID string, port int) error {
	cfg, err := LoadAccessLog()
	if err != nil {
		return err
	}
	if cfg.Tunnels == nil {
		cfg.Tunnels = map[string]AccessLogTunnel{}
	}
	key := AccessLogKey(profileID, port)
	if _, ok := cfg.Tunnels[key]; ok {
		return nil
	}
	cfg.Tunnels[key] = AccessLogTunnel{Enabled: true}
	return SaveAccessLog(cfg)
}

// SetTunnelLogEnabled 单独开关某条隧道的访问日志。
func SetTunnelLogEnabled(profileID string, port int, enabled bool) error {
	cfg, err := LoadAccessLog()
	if err != nil {
		return err
	}
	if cfg.Tunnels == nil {
		cfg.Tunnels = map[string]AccessLogTunnel{}
	}
	cfg.Tunnels[AccessLogKey(profileID, port)] = AccessLogTunnel{Enabled: enabled}
	return SaveAccessLog(cfg)
}

// RemoveTunnelAccessLog 删掉这条隧道的开关记录和日志文件。
func RemoveTunnelAccessLog(profileID string, port int) error {
	cfg, err := LoadAccessLog()
	if err != nil {
		return err
	}
	if cfg.Tunnels != nil {
		delete(cfg.Tunnels, AccessLogKey(profileID, port))
		if err := SaveAccessLog(cfg); err != nil {
			return err
		}
	}
	return DeleteAccessLog(profileID, port)
}

// RemoveProfileAccessLogs 清掉某台服务器档案留下的全部访问日志与开关。
func RemoveProfileAccessLogs(profileID string) error {
	dir := paths.AccessLogProfileDir(profileID)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	cfg, err := LoadAccessLog()
	if err != nil {
		return err
	}
	if cfg.Tunnels == nil {
		return nil
	}
	prefix := profileID + ":"
	changed := false
	for k := range cfg.Tunnels {
		if strings.HasPrefix(k, prefix) {
			delete(cfg.Tunnels, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return SaveAccessLog(cfg)
}

// AppendAccessLog 往这条隧道的日志文件末尾追加一行。
func AppendAccessLog(profileID string, port int, line string) error {
	path := paths.AccessLogFile(profileID, port)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	if err == nil && !strings.HasSuffix(line, "\n") {
		_, err = f.WriteString("\n")
	}
	return err
}

// AccessLogSize 返回日志文件字节数；文件不存在时为 0。
func AccessLogSize(profileID string, port int) (int64, error) {
	fi, err := os.Stat(paths.AccessLogFile(profileID, port))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// DeleteAccessLog 删除这条隧道的日志文件。文件不存在不算错。
// 该档案下已经没有其它日志时，连空目录一起拆掉，避免留下空壳。
func DeleteAccessLog(profileID string, port int) error {
	path := paths.AccessLogFile(profileID, port)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	if dir == paths.AccessLogDir() {
		return nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
	return nil
}

// FormatSize 把字节数写成可读体积。
func FormatSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}
