package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/store"
)

func writeLog(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frpc.log")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}
	return path
}

// TestConnectFailReasonTakesLatest 取最近一次的原因：frpc 会一直重试，
// 早先那条可能已经不是现在的毛病了。
func TestConnectFailReasonTakesLatest(t *testing.T) {
	path := writeLog(t, `2026-08-13 02:04:41 [I] try to connect to server...
2026-08-13 02:04:51 [W] connect to server error: i/o timeout
2026-08-13 02:05:01 [I] try to connect to server...
2026-08-13 02:05:11 [W] connect to server error: connection write timeout
`)
	if got := connectFailReason(path, 0); got != "connection write timeout" {
		t.Fatalf("应取最近一次的原因，实得 %q", got)
	}
}

// TestConnectFailReasonEmptyWhenRejected 被拒绝的日志里没有连接错误。
// 这两种失败必须能分开，否则建议会把人指向错误的方向。
func TestConnectFailReasonEmptyWhenRejected(t *testing.T) {
	path := writeLog(t, `2026-08-13 02:04:41 [I] try to connect to server...
2026-08-13 02:04:42 [E] login to server failed: authorization failed
`)
	if got := connectFailReason(path, 0); got != "" {
		t.Fatalf("被拒绝时不该报出连接错误，实得 %q", got)
	}
}

// TestConnectFailReasonRespectsOffset 上一轮的旧错误不能算进这一轮，
// 否则重启后会一直背着上次的故障描述。
func TestConnectFailReasonRespectsOffset(t *testing.T) {
	old := "2026-08-13 02:04:51 [W] connect to server error: i/o timeout\n"
	path := writeLog(t, old+"2026-08-13 02:06:00 [I] login to server success\n")
	if got := connectFailReason(path, int64(len(old))); got != "" {
		t.Fatalf("偏移之前的旧错误不该算数，实得 %q", got)
	}
}

func TestConnectFailReasonEmptyOnCleanLog(t *testing.T) {
	path := writeLog(t, "2026-08-13 02:06:00 [I] login to server success\n")
	if got := connectFailReason(path, 0); got != "" {
		t.Fatalf("日志干净时应为空，实得 %q", got)
	}
}

// TestUnreachableIsNotLoginFailed 两个状态不能同值，否则界面与建议
// 又会把「没回音」当成「密钥不对」。
func TestUnreachableIsNotLoginFailed(t *testing.T) {
	if StateUnreachable == StateLoginFailed {
		t.Fatal("连不上与被拒绝必须是两个状态")
	}
	if Status(Status{State: StateUnreachable}).Running() {
		t.Fatal("连不上不该被当成运行中")
	}
}

// profileWithLog 造一个日志落在临时目录的档案。
func profileWithLog(t *testing.T, body string) store.Profile {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := store.Profile{Name: "visyc", ServerIP: "1.2.3.4", ServerPort: 7000}
	if err := os.MkdirAll(filepath.Dir(p.LogPath()), 0o700); err != nil {
		t.Fatalf("建日志目录失败: %v", err)
	}
	if err := os.WriteFile(p.LogPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}
	return p
}

// TestTimeoutFailureNeverReportsLoginFailed 是这套改动的核心不变量：
// 等满了没登录上，无论日志里有没有线索，都不能记成「被拒绝」——
// 记错了状态，界面和建议就会一起把人指向没有错的 token。
func TestTimeoutFailureNeverReportsLoginFailed(t *testing.T) {
	cases := map[string]string{
		"有连接错误": "2026-08-13 02:04:51 [W] connect to server error: connection write timeout\n",
		"日志空白":  "",
		"只有重试":  "2026-08-13 02:04:41 [I] try to connect to server...\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			state, msg := timeoutFailure(profileWithLog(t, body), 0)
			if state != StateUnreachable {
				t.Fatalf("超时该记成 unreachable，实得 %s", state)
			}
			if strings.Contains(msg, "token") || strings.Contains(msg, "密钥不") {
				t.Fatalf("超时的说明不该指向密钥，实得 %q", msg)
			}
			if msg == "" {
				t.Fatal("说明不能为空")
			}
		})
	}
}

// TestTimeoutFailureQuotesFrpcReason 有线索时要把 frpc 的原话端出来。
func TestTimeoutFailureQuotesFrpcReason(t *testing.T) {
	p := profileWithLog(t,
		"2026-08-13 02:04:51 [W] connect to server error: connection write timeout\n")
	_, msg := timeoutFailure(p, 0)
	if !strings.Contains(msg, "connection write timeout") {
		t.Fatalf("该带上 frpc 报的原因，实得 %q", msg)
	}
	if !strings.Contains(msg, "1.2.3.4:7000") {
		t.Fatalf("该点明连的是哪台，实得 %q", msg)
	}
	// 「能握手却没人回话」最常见的成因就是对端假死，这条得排在安全组前面。
	if !strings.Contains(msg, "假死") {
		t.Fatalf("该先怀疑对端过载假死，实得 %q", msg)
	}
	if strings.Contains(msg, "代理") || strings.Contains(msg, "VPN") {
		t.Fatalf("诊断不应再引导用户排查代理或 VPN，实得 %q", msg)
	}
}

// noisyLog 造一段超过 2 MiB 的日志，末尾放一行标记。
//
// 专挑这个体量：任何「先读固定长度再判断」的写法都会在这里悄悄丢掉末尾，
// 判定随之退回错误答案且不报错。
func noisyLog(t *testing.T, tail string) string {
	t.Helper()
	var b strings.Builder
	for b.Len() < 2<<20 {
		b.WriteString("2026-08-13 02:04:41 [I] try to connect to server... 填充填充填充填充\n")
	}
	b.WriteString(tail)
	return writeLog(t, b.String())
}

// TestConnectFailReasonSurvivesHugeLog 末尾那行原因不能因为日志太长就读不到。
func TestConnectFailReasonSurvivesHugeLog(t *testing.T) {
	path := noisyLog(t,
		"2026-08-13 02:05:11 [W] connect to server error: connection write timeout\n")
	if got := connectFailReason(path, 0); got != "connection write timeout" {
		t.Fatalf("日志再长也该读到末尾的原因，实得 %q", got)
	}
}

// TestLoginOutcomeSurvivesHugeLog 登录结论同理：读不到末尾就会把「已经登录上了」
// 误判成「连不上」，这比不判还糟。
func TestLoginOutcomeSurvivesHugeLog(t *testing.T) {
	okPath := noisyLog(t,
		"2026-08-13 02:05:11 [I] login to server success, get run id [abc]\n")
	if ok, rejected := loginOutcome(okPath, 0); !ok || rejected {
		t.Fatalf("末尾的登录成功该被读到，实得 ok=%v rejected=%v", ok, rejected)
	}

	badPath := noisyLog(t,
		"2026-08-13 02:05:11 [E] login to server failed: authorization failed\n")
	if _, rejected := loginOutcome(badPath, 0); !rejected {
		t.Fatal("末尾的登录被拒该被读到")
	}
}

// TestLoginOutcomeLetsRejectionWin 先成功后被拒时以拒绝为准：
// 那说明连接已经掉了又没能登回去，报「已连上」会掩盖故障。
func TestLoginOutcomeLetsRejectionWin(t *testing.T) {
	path := writeLog(t, `2026-08-13 02:04:00 [I] login to server success, get run id [abc]
2026-08-13 02:05:00 [E] login to server failed: authorization failed
`)
	if _, rejected := loginOutcome(path, 0); !rejected {
		t.Fatal("后来的拒绝该压过先前的成功")
	}
}

// TestLoginOutcomeReadsUnterminatedTail frpc 正在写的那一行还没有换行符，
// 漏掉它会白等一整个超时窗口。
func TestLoginOutcomeReadsUnterminatedTail(t *testing.T) {
	path := writeLog(t, "2026-08-13 02:05:11 [I] login to server success, get run id [abc]")
	if ok, _ := loginOutcome(path, 0); !ok {
		t.Fatal("没有换行符的末行也该被读到")
	}
}
