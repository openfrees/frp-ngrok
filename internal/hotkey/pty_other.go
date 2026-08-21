//go:build !darwin || !cgo

package hotkey

import (
	"io"
	"os/exec"
)

func attachCommandTerminal(cmd *exec.Cmd) (*commandTerminal, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stdoutW
	return &commandTerminal{
		input:      stdinW,
		output:     stdoutR,
		afterStart: func() {},
		close: func() {
			_ = stdinW.Close()
			_ = stdinR.Close()
			_ = stdoutR.Close()
			_ = stdoutW.Close()
		},
	}, nil
}
