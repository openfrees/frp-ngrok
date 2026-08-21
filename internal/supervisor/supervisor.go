// Package supervisor 负责 frpc 子进程的生命周期：启动、保活、停止与登录状态判定。
//
// 面板服务本身由 launchd 保活，frpc 则由面板保活，形成单一的责任链：
// 只要面板服务在跑，隧道就在跑；面板异常退出会被系统秒级拉起并自动重连。
package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/store"
)

// State 表示 frpc 的运行状态。
type State string

const (
	// StateStopped 表示用户主动停止，不会自动拉起。
	StateStopped State = "stopped"
	// StateStarting 表示进程已拉起，正在等待登录结果。
	StateStarting State = "starting"
	// StateRunning 表示已成功登录 frps，隧道可用。
	StateRunning State = "running"
	// StateLoginFailed 表示进程在跑但登录被拒（多为 token 不符或对端不是我们的 frps）。
	StateLoginFailed State = "login_failed"
	// StateUnreachable 表示连不上服务器：进程在跑也在重试，但对端一个字都没回。
	//
	// 必须与 StateLoginFailed 分开：被拒绝说明对端听见了我们，问题在密钥或
	// 对端程序；没回音说明话根本没送到，问题在网络层。两者的排查方向相反，
	// 混成一种状态就会把人送去反复核对没有错的 token。
	StateUnreachable State = "unreachable"
	// StateCrashed 表示进程反复异常退出。
	StateCrashed State = "crashed"
)

const (
	loginTimeout   = 15 * time.Second
	minRestartWait = 2 * time.Second
	maxRestartWait = 30 * time.Second
)

var (
	loginOKPatterns = []string{
		"login to server success",
	}
	loginFailPatterns = []string{
		"login to server failed",
		"login to the server failed",
		"authorization failed",
		"authentication failed",
		"token in login doesn't match",
		"invalid authentication",
	}
	// 服务端拒绝隧道注册时 frpc 打出的特征。登录成功与隧道可用是两回事。
	proxyRejectPatterns = []string{"start error", "start proxy error"}
)

// connectFailPattern 是 frpc 连不上服务端时打出的特征。
const connectFailPattern = "connect to server error"

// Status 是对外暴露的运行快照。
type Status struct {
	State     State     `json:"state"`
	Profile   string    `json:"profile"`
	PID       int       `json:"pid"`
	Since     time.Time `json:"since"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"lastError"`
}

// Running 表示隧道当前是否真正可用。
func (s Status) Running() bool { return s.State == StateRunning }

// Supervisor 监管单个 frpc 进程。同一时刻只连一台服务器。
type Supervisor struct {
	mu       sync.Mutex
	desired  bool // 期望运行；用户主动停止后为 false
	profile  store.Profile
	cmd      *exec.Cmd
	state    State
	since    time.Time
	restarts int
	lastErr  string
	gen      uint64 // 代际标记，避免旧监管协程干扰新一轮启动
}

// New 创建监管器。
func New() *Supervisor {
	return &Supervisor{state: StateStopped, since: time.Now()}
}

// Status 返回当前状态快照。
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		State:     s.state,
		Profile:   s.profile.Name,
		Since:     s.since,
		Restarts:  s.restarts,
		LastError: s.lastErr,
	}
	if s.cmd != nil && s.cmd.Process != nil {
		st.PID = s.cmd.Process.Pid
	}
	return st
}

// Start 启动指定档案的 frpc，并等待登录结果。
//
// 返回的 error 仅代表本次登录未成功；进程仍会被持续保活并自动重试。
func (s *Supervisor) Start(p store.Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(paths.FrpcBin()); err != nil {
		return fmt.Errorf("frpc 程序不存在，请先完成初始化: %w", err)
	}
	if err := store.EnsureConf(p); err != nil {
		return err
	}

	s.stopProcess()

	s.mu.Lock()
	s.desired = true
	s.profile = p
	s.state = StateStarting
	s.since = time.Now()
	s.restarts = 0
	s.lastErr = ""
	s.gen++
	gen := s.gen
	s.mu.Unlock()

	offset := logSize(p.LogPath())
	if err := s.spawn(gen); err != nil {
		s.setError(StateCrashed, err.Error())
		return err
	}

	return s.awaitLogin(p, offset)
}

// Stop 主动停止 frpc，停止后不会自动拉起。
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.desired = false
	s.gen++
	s.mu.Unlock()

	s.stopProcess()

	s.mu.Lock()
	s.state = StateStopped
	s.since = time.Now()
	s.lastErr = ""
	s.mu.Unlock()
}

// Desired 表示当前是否期望 frpc 处于运行状态。
func (s *Supervisor) Desired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.desired
}

// ApplyConfig 在配置变更后同步到磁盘，并仅在客户端本就该运行时重启。
//
// 用户手动关掉客户端后增删隧道，不应把客户端悄悄拉起来。
func (s *Supervisor) ApplyConfig(p store.Profile) error {
	if err := store.EnsureConf(p); err != nil {
		return err
	}
	if !s.Desired() {
		return nil
	}
	return s.Start(p)
}

// Restart 以最新配置重启 frpc。
func (s *Supervisor) Restart() error {
	s.mu.Lock()
	p := s.profile
	s.mu.Unlock()
	if p.Name == "" {
		var err error
		if p, err = store.ResolveCurrent(); err != nil {
			return err
		}
	}
	return s.Start(p)
}

// spawn 拉起进程并挂上监管协程。
func (s *Supervisor) spawn(gen uint64) error {
	s.mu.Lock()
	p := s.profile
	s.mu.Unlock()

	cmd := exec.Command(paths.FrpcBin(), "-c", p.ConfPath())
	cmd.Dir = paths.DataDir()
	// 独立进程组：便于整组回收，避免留下孤儿进程。
	cmd.SysProcAttr = detachedAttr()
	// frpc 自身按 log.to 写文件，这里丢弃标准流即可。
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 frpc 失败: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	writePIDFile(cmd.Process.Pid)

	go s.watch(cmd, gen)
	return nil
}

// watch 等待进程退出，并在非主动停止时按退避策略拉起。
func (s *Supervisor) watch(cmd *exec.Cmd, gen uint64) {
	err := cmd.Wait()

	s.mu.Lock()
	stale := gen != s.gen
	desired := s.desired
	if !stale {
		s.restarts++
	}
	restarts := s.restarts
	s.mu.Unlock()

	// 已被新一轮启动接管，或用户已主动停止：本协程直接退出。
	if stale || !desired {
		return
	}

	reason := "frpc 进程退出"
	if err != nil {
		reason = fmt.Sprintf("frpc 异常退出: %v", err)
	}
	s.setError(StateCrashed, reason)

	wait := minRestartWait * time.Duration(restarts)
	if wait > maxRestartWait {
		wait = maxRestartWait
	}
	time.Sleep(wait)

	s.mu.Lock()
	if gen != s.gen || !s.desired {
		s.mu.Unlock()
		return
	}
	s.state = StateStarting
	p := s.profile
	s.mu.Unlock()

	offset := logSize(p.LogPath())
	if err := s.spawn(gen); err != nil {
		s.setError(StateCrashed, err.Error())
		return
	}
	go func() { _ = s.awaitLogin(p, offset) }()
}

// awaitLogin 在超时窗口内轮询日志，判定本轮是否登录成功。
func (s *Supervisor) awaitLogin(p store.Profile, offset int64) error {
	deadline := time.Now().Add(loginTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)

		s.mu.Lock()
		alive := s.cmd != nil && s.cmd.ProcessState == nil
		s.mu.Unlock()

		ok, rejected := loginOutcome(p.LogPath(), offset)
		if rejected {
			msg := "登录被拒绝：token 不一致，或对端 " + p.ServerIP + " 上跑的不是本方案的 frps"
			s.setError(StateLoginFailed, msg)
			return errors.New(msg)
		}
		if ok {
			s.mu.Lock()
			s.state = StateRunning
			s.since = time.Now()
			s.lastErr = proxyRejectReason(p.LogPath(), offset)
			s.mu.Unlock()
			return nil
		}
		if !alive {
			msg := "frpc 启动后立即退出，请查看日志"
			s.setError(StateCrashed, msg)
			return errors.New(msg)
		}
	}
	state, msg := timeoutFailure(p, offset)
	s.setError(state, msg)
	return errors.New(msg)
}

// timeoutFailure 判定「等满了还没登录上」该记成哪种失败。
//
// 走到这里说明既没被拒也没成功。frpc 若已经报出连接错误，就把它的原话端给
// 用户：那是「话根本没送到」的实锤，比让人去猜密钥准得多。无论哪一种，都不
// 能记成 StateLoginFailed——没回音和被拒绝的排查方向是相反的。
func timeoutFailure(p store.Profile, offset int64) (State, string) {
	if reason := connectFailReason(p.LogPath(), offset); reason != "" {
		return StateUnreachable, fmt.Sprintf(
			"连不上 %s:%d（%s）：对端一个字都没回，这不是密钥的问题。"+
				"头一个要怀疑的是那台服务器过载假死了——内核照样应答握手，上面的进程却排不上队回话；"+
				"去看它的 CPU、内存和磁盘 IO，再顺手试试 SSH 和宝塔，要是一起打不开就是整机的事。"+
				"排除了再看安全组有没有放行你的出口 IP。",
			p.ServerIP, p.ServerPort, reason)
	}
	return StateUnreachable, fmt.Sprintf("%d 秒内未收到登录成功回执，请检查 %s:%d 是否可达",
		int(loginTimeout.Seconds()), p.ServerIP, p.ServerPort)
}

// connectFailReason 取日志里最近一次连接失败的原因原文。
//
// frpc 会把底层错误照原样打出来（connection write timeout、i/o timeout、
// connection refused 等），转述它比我们自己编一个原因准确得多。
func connectFailReason(logPath string, offset int64) string {
	var reason string
	scanLogFrom(logPath, offset, func(line string) bool {
		// 在小写副本上定位并切分，避免大小写转换改变字节长度时切错位置。
		lower := strings.ToLower(line)
		if i := strings.Index(lower, connectFailPattern); i >= 0 {
			detail := strings.TrimSpace(lower[i+len(connectFailPattern):])
			reason = strings.TrimSpace(strings.TrimPrefix(detail, ":"))
		}
		return true
	})
	return reason
}

// proxyRejectReason 在登录成功后再看一眼隧道有没有被服务端拒掉。
//
// 「登录成功」只证明 token 对、对端是 frps；隧道能不能注册上是另一回事。
// 典型场景：本地切成单域名了，服务端 frps 还留着旧的 subDomainHost，
// 于是 customDomains 被判成它的子域直接拒收——此时界面若只报「已连上」，
// 用户会以为好了，实际隧道一条都没起来。
func proxyRejectReason(logPath string, offset int64) string {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		var reason string
		scanLogFrom(logPath, offset, func(line string) bool {
			if !containsAny(strings.ToLower(line), proxyRejectPatterns) {
				return true
			}
			detail := strings.TrimSpace(line)
			if i := strings.Index(strings.ToLower(detail), "start error"); i >= 0 {
				detail = strings.TrimSpace(strings.TrimPrefix(detail[i:], "start error"))
				detail = strings.TrimSpace(strings.TrimPrefix(detail, ":"))
			}
			reason = "隧道没能注册到服务端：" + detail +
				"　多半是服务端 frps 的配置和本地域名模式对不上，按「服务端部署」的脚本在服务器上重跑一遍。"
			return false
		})
		if reason != "" {
			return reason
		}
	}
	return ""
}

// stopProcess 结束当前进程组并清理 PID 文件。
func (s *Supervisor) stopProcess() {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		removePIDFile()
		return
	}
	pid := cmd.Process.Pid
	// 优先温和退出，给 frpc 机会向 frps 注销。
	_ = terminateProcess(pid)

	for i := 0; i < 20; i++ {
		if !processAlive(pid) {
			removePIDFile()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = killProcess(pid)
	_ = cmd.Process.Kill()
	removePIDFile()
}

func (s *Supervisor) setError(st State, msg string) {
	s.mu.Lock()
	s.state = st
	s.lastErr = msg
	s.since = time.Now()
	s.mu.Unlock()
}

// ---------- 残留进程清理 ----------

// CleanupStrays 清理不受本进程监管的 frpc 残留。
//
// 两种来源：面板异常退出后遗留的孤儿进程，以及旧版 .command 脚本注册的
// 系统服务。不清理会出现多个 frpc 抢同一份配置。
func CleanupStrays() {
	// 旧脚本的服务带保活策略，必须先摘掉再杀进程，否则杀了会被秒拉活。
	unloadLegacyService()

	if pid := readPIDFile(); pid > 0 && processAlive(pid) {
		_ = terminateProcess(pid)
	}
	removePIDFile()

	// 兜底：按命令行特征清掉指向本数据目录的 frpc。
	for _, pid := range findFrpcProcesses() {
		if pid > 0 && pid != os.Getpid() {
			_ = terminateProcess(pid)
		}
	}
	time.Sleep(300 * time.Millisecond)
}

func pidFilePath() string { return filepath.Join(paths.AppDir(), "frpc.pid") }

func writePIDFile(pid int) {
	_ = os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0o644)
}

func readPIDFile() int {
	b, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

func removePIDFile() { _ = os.Remove(pidFilePath()) }

// ---------- 日志读取 ----------

func logSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// scanLogFrom 逐行扫描指定偏移之后的新日志，fn 返回 false 即停止。
//
// 刻意不按固定长度把整段读进内存：日志刷得比预期猛时，截断读会悄悄丢掉末尾
// 那几行，而「有没有登录成功」「最近一次为什么连不上」恰恰都在末尾——那种
// 丢失不会报错，只会让判定悄悄退回错误答案。逐行流式读则没有这个上限。
// 日志被轮转截断时从头读起。
func scanLogFrom(path string, offset int64, fn func(line string) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return
	}

	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		// 末行可能没有换行符，此时 ReadString 连内容带 EOF 一起给出。
		if line != "" && !fn(strings.TrimRight(line, "\r\n")) {
			return
		}
		if err != nil {
			return
		}
	}
}

// loginOutcome 扫描本轮新日志，看服务端有没有给出登录结论。
//
// 被拒是终局，一出现就不必再往后看；成功则要扫完整段——后面可能又冒出一次
// 拒绝，那时以拒绝为准。
func loginOutcome(logPath string, offset int64) (ok, rejected bool) {
	scanLogFrom(logPath, offset, func(line string) bool {
		lower := strings.ToLower(line)
		if containsAny(lower, loginFailPatterns) {
			rejected = true
			return false
		}
		if containsAny(lower, loginOKPatterns) {
			ok = true
		}
		return true
	})
	return ok, rejected
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// TailLog 返回日志末尾若干行，供控制台展示。
func TailLog(path string, lines int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const maxRead = 256 * 1024
	size := fi.Size()
	start := size - maxRead
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return ""
	}
	buf := make([]byte, size-start)
	n, _ := f.Read(buf)
	if n <= 0 {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
