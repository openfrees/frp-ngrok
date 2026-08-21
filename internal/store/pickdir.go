package store

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// pickDirectory 弹出系统选目录对话框。测试里替换它，避免真弹窗（CI / 无 GUI 会挂死）。
var pickDirectory = nativePickDirectory

// OverridePickDirectory 仅供测试替换原生选目录实现，返回恢复函数。
func OverridePickDirectory(fn func() (path string, canceled bool, err error)) func() {
	prev := pickDirectory
	pickDirectory = fn
	return func() { pickDirectory = prev }
}

// PickDirectory 弹出原生选目录对话框。canceled 表示用户取消，不当成错误。
func PickDirectory() (path string, canceled bool, err error) {
	return pickDirectory()
}

func nativePickDirectory() (string, bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return pickDirDarwin()
	case "windows":
		return pickDirWindows()
	default:
		return pickDirLinux()
	}
}

func pickDirDarwin() (string, bool, error) {
	script := `POSIX path of (choose folder with prompt "选择站点目录")`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	return finishPicker(out, err, 0)
}

func pickDirLinux() (string, bool, error) {
	if p, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.Command(p, "--file-selection", "--directory", "--title=选择站点目录").CombinedOutput()
		return finishPicker(out, err, 1)
	}
	if p, err := exec.LookPath("kdialog"); err == nil {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/"
		}
		out, err := exec.Command(p, "--getexistingdirectory", home, "选择站点目录").CombinedOutput()
		return finishPicker(out, err, 1)
	}
	return "", false, fmt.Errorf("当前系统无法弹出选目录对话框：请安装 zenity 或 kdialog，或手动把路径填进输入框")
}

func pickDirWindows() (string, bool, error) {
	ps, err := exec.LookPath("powershell")
	if err != nil {
		ps, err = exec.LookPath("pwsh")
		if err != nil {
			return "", false, fmt.Errorf("当前系统无法弹出选目录对话框：找不到 PowerShell，请手动把路径填进输入框")
		}
	}
	script := "Add-Type -AssemblyName System.Windows.Forms | Out-Null; " +
		"$d = New-Object System.Windows.Forms.FolderBrowserDialog; " +
		"$d.Description = '选择站点目录'; $d.ShowNewFolderButton = $true; " +
		"if ($d.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { exit 2 }; " +
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; [Console]::Write($d.SelectedPath)"
	out, err := exec.Command(ps, "-NoProfile", "-STA", "-Command", script).CombinedOutput()
	return finishPicker(out, err, 2)
}

func finishPicker(out []byte, err error, cancelExit int) (string, bool, error) {
	if err != nil {
		if isDirPickerCanceled(out, err, cancelExit) {
			return "", true, nil
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", false, fmt.Errorf("无法弹出选目录对话框: %s", msg)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", true, nil
	}
	return filepath.Clean(path), false, nil
}

func isDirPickerCanceled(out []byte, err error, cancelExit int) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(strings.TrimSpace(string(out) + " " + err.Error()))
	if strings.Contains(combined, "user canceled") ||
		strings.Contains(combined, "user cancelled") ||
		strings.Contains(combined, "(-128)") {
		return true
	}
	code, ok := pickerExitCode(err)
	if !ok {
		return false
	}
	if cancelExit != 0 && code == cancelExit {
		return true
	}
	if code != 1 {
		return false
	}
	if strings.Contains(combined, "display") ||
		strings.Contains(combined, "unable to init") ||
		strings.Contains(combined, "cannot open") ||
		strings.Contains(combined, "not found") {
		return false
	}
	return strings.TrimSpace(string(out)) == ""
}

type exitCoder interface {
	ExitCode() int
}

func pickerExitCode(err error) (int, bool) {
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode(), true
	}
	return 0, false
}
