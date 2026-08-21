// Package frpcbin 负责确保本机存在可用的 frpc 客户端程序。
package frpcbin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

// Version 是随面板分发的 frp 客户端版本。
const Version = "0.70.1"

// 官方发布地址优先，失败后退到镜像；两者内容一致，镜像仅解决国内直连慢的问题。
var mirrors = []string{
	"https://github.com/fatedier/frp/releases/download/v%s/%s",
	"https://ghproxy.net/https://github.com/fatedier/frp/releases/download/v%s/%s",
}

// Present 判断 frpc 是否已就绪。
func Present() bool {
	fi, err := os.Stat(paths.FrpcBin())
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// InstalledVersion 返回本机 frpc 的版本号，取不到时返回空串。
func InstalledVersion() string {
	if !Present() {
		return ""
	}
	out, err := exec.Command(paths.FrpcBin(), "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Ensure 确保 frpc 存在，缺失时从官方发布页下载。
func Ensure(progress func(string)) error {
	if Present() {
		return nil
	}
	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	arch := runtime.GOARCH
	// 官方发布包在 Windows 上是 zip，其他平台是 tar.gz。
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	pkg := fmt.Sprintf("frp_%s_%s_%s.%s", Version, runtime.GOOS, arch, ext)
	note := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	var lastErr error
	for i, tpl := range mirrors {
		url := fmt.Sprintf(tpl, Version, pkg)
		if i == 0 {
			note(fmt.Sprintf("正在下载 frpc v%s (%s)，约 12MB...", Version, arch))
		} else {
			note("官方源较慢，改用加速镜像重试...")
		}
		if err := downloadAndExtract(url); err != nil {
			lastErr = err
			continue
		}
		note("frpc 安装完成")
		return nil
	}
	return fmt.Errorf("下载 frpc 失败: %w", lastErr)
}

func downloadAndExtract(url string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}

	if strings.HasSuffix(url, ".zip") {
		return extractZip(resp.Body)
	}
	return extractTarGz(resp.Body)
}

// wantedName 是压缩包内目标可执行文件的名字。
func wantedName() string {
	if runtime.GOOS == "windows" {
		return "frpc.exe"
	}
	return "frpc"
}

func extractTarGz(r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("压缩包内未找到 %s", wantedName())
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != wantedName() {
			continue
		}
		return writeBinary(tr)
	}
}

// extractZip 需要可随机读取的数据源，先落到临时文件再解析。
func extractZip(r io.Reader) error {
	tmp, err := os.CreateTemp("", "frp-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, io.LimitReader(r, 256<<20))
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != wantedName() {
			continue
		}
		rc, openErr := f.Open()
		if openErr != nil {
			return openErr
		}
		defer rc.Close()
		return writeBinary(rc)
	}
	return fmt.Errorf("压缩包内未找到 %s", wantedName())
}

func writeBinary(r io.Reader) error {
	tmp := paths.FrpcBin() + ".download"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	// 限制解压体积，避免异常压缩包耗尽磁盘。
	const maxSize = 128 << 20
	if _, err := io.Copy(f, io.LimitReader(r, maxSize)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, paths.FrpcBin())
}
