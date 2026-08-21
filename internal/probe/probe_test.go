package probe

import (
	"net"
	"testing"
	"time"
)

// listenLocal 起一个本机监听，返回它的端口。
func listenLocal(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// closedPort 返回一个刚被释放、确定没人在听的端口。
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestCheckTCPUnreachable(t *testing.T) {
	got := checkTCP("127.0.0.1", closedPort(t), time.Second, TCPOpen)
	if got.Result != TCPUnreachable {
		t.Fatalf("没人在听应判 unreachable，实得 %s", got.Result)
	}
	if !got.Unreachable() {
		t.Fatal("Unreachable() 应为真")
	}
}

func TestCheckTCPReachable(t *testing.T) {
	got := checkTCP("127.0.0.1", listenLocal(t), time.Second, TCPOpen)
	if got.Result != TCPReachable {
		t.Fatalf("对照端口不通时应判 reachable，实得 %s", got.Result)
	}
	if got.Unreachable() {
		t.Fatal("端口通时不该断言连不上")
	}
}

// TestCheckTCPIgnoresOtherOpenPorts 钉住产品语义：端口检测只看用户填写的目标端口，
// 不再拿随机端口推测本机有没有代理。
func TestCheckTCPIgnoresOtherOpenPorts(t *testing.T) {
	calls := 0
	got := checkTCP("1.2.3.4", 7000, time.Second, func(host string, port int, _ time.Duration) bool {
		calls++
		if host != "1.2.3.4" || port != 7000 {
			t.Fatalf("不应探测目标以外的地址或端口: %s:%d", host, port)
		}
		return true
	})
	if got.Result != TCPReachable {
		t.Fatalf("目标端口握手成功就应判 reachable，实得 %s", got.Result)
	}
	if got.Unreachable() {
		t.Fatal("目标端口握手成功时不该断言连不上")
	}
	if calls != 1 {
		t.Fatalf("只应探测一次目标端口，实得 %d 次", calls)
	}
}
