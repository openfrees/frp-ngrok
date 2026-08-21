//go:build darwin && cgo

package hotkey

/*
#include <stdlib.h>
#include <util.h>
#include <sys/ioctl.h>

static int frp_openpty(int *master, int *slave) {
	struct winsize ws;
	ws.ws_row = 24;
	ws.ws_col = 80;
	ws.ws_xpixel = 0;
	ws.ws_ypixel = 0;
	return openpty(master, slave, NULL, NULL, &ws);
}
*/
import "C"

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func attachCommandTerminal(cmd *exec.Cmd) (*commandTerminal, error) {
	master, slave, err := openCommandPTY()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	return &commandTerminal{
		input:  master,
		output: master,
		afterStart: func() {
			_ = slave.Close()
		},
		close: func() {
			_ = master.Close()
			_ = slave.Close()
		},
	}, nil
}

func openCommandPTY() (*os.File, *os.File, error) {
	var master C.int
	var slave C.int
	if C.frp_openpty(&master, &slave) != 0 {
		return nil, nil, fmt.Errorf("openpty failed")
	}
	return os.NewFile(uintptr(master), "frpanel-pty-master"), os.NewFile(uintptr(slave), "frpanel-pty-slave"), nil
}
