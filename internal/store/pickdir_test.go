package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPickDirectoryCanceledIsNotError(t *testing.T) {
	restore := OverridePickDirectory(func() (string, bool, error) {
		return "", true, nil
	})
	t.Cleanup(restore)

	path, canceled, err := PickDirectory()
	if err != nil {
		t.Fatalf("取消不该当错误: %v", err)
	}
	if !canceled {
		t.Fatal("应标记为取消")
	}
	if path != "" {
		t.Fatalf("取消时路径应为空，实得 %q", path)
	}
}

func TestPickDirectoryReturnsStubbedPath(t *testing.T) {
	want := t.TempDir()
	restore := OverridePickDirectory(func() (string, bool, error) {
		return want, false, nil
	})
	t.Cleanup(restore)

	path, canceled, err := PickDirectory()
	if err != nil || canceled || path != want {
		t.Fatalf("got path=%q canceled=%v err=%v", path, canceled, err)
	}
}

func TestIsDirPickerCanceled(t *testing.T) {
	if !isDirPickerCanceled([]byte("execution error: User canceled. (-128)"), errors.New("exit status 1"), 0) {
		t.Fatal("osascript 用户取消应识别")
	}
	if !isDirPickerCanceled([]byte("用户已取消。 (-128)"), errors.New("exit status 1"), 0) {
		t.Fatal("带 (-128) 的取消应识别")
	}
	if isDirPickerCanceled([]byte("Unable to init server: Could not connect: Connection refused"), errors.New("exit status 1"), 1) {
		t.Fatal("无 DISPLAY 不应当成用户取消")
	}
	if isDirPickerCanceled([]byte("zenity: command not found"), errors.New("executable file not found"), 1) {
		t.Fatal("找不到程序不应当成取消")
	}
}

func TestFinishPickerTreatsEmptyFailureAsCancel(t *testing.T) {
	path, canceled, err := finishPicker(nil, &exitStatus{code: 1}, 1)
	if err != nil || !canceled || path != "" {
		t.Fatalf("zenity 空输出+exit 1 应视为取消: path=%q canceled=%v err=%v", path, canceled, err)
	}
	path, canceled, err = finishPicker(nil, &exitStatus{code: 2}, 2)
	if err != nil || !canceled {
		t.Fatalf("Windows exit 2 应视为取消: canceled=%v err=%v", canceled, err)
	}
}

type exitStatus struct{ code int }

func (e *exitStatus) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *exitStatus) ExitCode() int { return e.code }

func TestFinishPickerReportsRealError(t *testing.T) {
	_, canceled, err := finishPicker([]byte("Unable to init server"), errors.New("exit status 1"), 1)
	if err == nil || canceled {
		t.Fatal("无 GUI 应返回错误而不是取消")
	}
	if !strings.Contains(err.Error(), "无法弹出选目录对话框") {
		t.Fatalf("错误应说明对话框失败: %v", err)
	}
}
