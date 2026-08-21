// Package installer 负责把程序装到本机、注册开机自启、拉起后台服务并打开浏览器。
package installer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openfrees/frp-ngrok/internal/apitypes"
	"github.com/openfrees/frp-ngrok/internal/paths"
)

// serveProcessPatterns 是 pgrep -f 用来找后台 serve 进程的模式。
// 升级后新二进制叫 frp-ngrok，旧的 frpanel 可能仍以独立进程占着端口，两边都要杀。
func serveProcessPatterns() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, bin := range []string{paths.InstalledBin(), paths.LegacyInstalledBin()} {
		if bin == "" {
			continue
		}
		if _, ok := seen[bin]; ok {
			continue
		}
		seen[bin] = struct{}{}
		out = append(out, bin+" serve")
	}
	return out
}

// Installed 表示程序是否已复制到本机安装目录。
func Installed() bool {
	fi, err := os.Stat(paths.InstalledBin())
	return err == nil && !fi.IsDir()
}

// RunningFromInstallDir 判断当前进程是否就是已安装的那份程序。
func RunningFromInstallDir() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	exe, _ = filepath.EvalSymlinks(exe)
	target, _ := filepath.EvalSymlinks(paths.InstalledBin())
	return exe == target && target != ""
}

// InstallSelf 把当前可执行文件复制到安装目录。
func InstallSelf() error {
	if err := paths.EnsureDirs(); err != nil {
		return err
	}
	if RunningFromInstallDir() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身程序失败: %w", err)
	}
	dstPath := paths.InstalledBin()
	same, err := filesIdentical(exe, dstPath)
	if err != nil {
		return fmt.Errorf("检查已安装程序失败: %w", err)
	}
	if same {
		// 内容没变就保留修改时间，避免被 DaemonOutdated 误判成升级。
		return os.Chmod(dstPath, 0o755)
	}
	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()

	// 先写临时文件再改名：正在运行的旧程序不会被写坏，升级也是原子的。
	tmpPath := dstPath + ".new"
	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Chmod(dstPath, 0o755)
}

func filesIdentical(first, second string) (bool, error) {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false, err
	}
	secondInfo, err := os.Stat(second)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if firstInfo.Size() != secondInfo.Size() {
		return false, nil
	}

	firstHash, err := fileSHA256(first)
	if err != nil {
		return false, err
	}
	secondHash, err := fileSHA256(second)
	if err != nil {
		return false, err
	}
	return firstHash == secondHash, nil
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// ---------- 端口与令牌 ----------

// ReadPort 读取服务实际监听的端口。
func ReadPort() int {
	b, err := os.ReadFile(paths.PortFile())
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return p
}

// WritePort 记录服务监听端口，供启动器与后续会话读取。
func WritePort(port int) error {
	return os.WriteFile(paths.PortFile(), []byte(strconv.Itoa(port)), 0o644)
}

// PickPort 从首选端口起向后寻找一个可用端口。
func PickPort(preferred int) (int, error) {
	for p := preferred; p < preferred+50; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			_ = ln.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("在 %d-%d 之间找不到可用端口", preferred, preferred+50)
}

// ---------- 服务健康探测 ----------

// DaemonAlive 检测后台服务是否已在指定端口就绪。
func DaemonAlive(port int) bool {
	_, ok := DaemonPing(port)
	return ok
}

// DaemonPing 读取后台服务的握手信息。
func DaemonPing(port int) (apitypes.Ping, bool) {
	var out apitypes.Ping
	if port <= 0 {
		return out, false
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", port))
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return out, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, false
	}
	return out, true
}

// DaemonOutdated 判断正在运行的服务是否还在跑被替换掉的旧程序。
//
// 安装新版本只是替换了磁盘文件，已运行的进程仍是旧代码，必须重启才生效。
func DaemonOutdated(port int) bool {
	ping, ok := DaemonPing(port)
	if !ok {
		return false
	}
	fi, err := os.Stat(paths.InstalledBin())
	if err != nil {
		return false
	}
	return ping.BinaryStamp != fi.ModTime().Unix()
}

// loadedServiceNeedsReload 判断 launchd 里已加载的任务是否还在跑过期程序。
// 只看进程还活着不够：plist 文件已经改成新路径，但 kickstart 不会重读 ProgramArguments。
func loadedServiceNeedsReload(port int) bool {
	if port <= 0 || !DaemonAlive(port) {
		return true
	}
	return DaemonOutdated(port)
}

// WaitDaemon 在给定时限内等待服务就绪。
func WaitDaemon(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if DaemonAlive(port) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// ConsoleURL 拼出带访问令牌的控制台地址。
func ConsoleURL(port int, token string) string {
	q := url.Values{}
	if token != "" {
		q.Set("token", token)
	}
	// 同一端口上的后台换过二进制后必须得到一个新 URL；否则浏览器可能只激活
	// 已打开的旧页面，用户会以为新包没有生效。
	if ping, ok := DaemonPing(port); ok && ping.BinaryStamp > 0 {
		q.Set("v", strconv.FormatInt(ping.BinaryStamp, 10))
	}
	base := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if query := q.Encode(); query != "" {
		return base + "?" + query
	}
	return base
}
