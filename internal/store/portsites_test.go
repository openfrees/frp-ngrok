package store

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

func TestPortSitesConfigPathIsUnderPluginsDir(t *testing.T) {
	isolateHome(t)
	got := paths.PortSitesConfigFile()
	if !strings.HasPrefix(got, paths.PluginsDir()) {
		t.Fatalf("端口站点配置应落在插件目录下: %s", got)
	}
	if filepath.Base(filepath.Dir(got)) != "port-sites" {
		t.Fatalf("配置应在 plugins/port-sites/ 下: %s", got)
	}
}

func TestPortSitesRoundTrip(t *testing.T) {
	isolateHome(t)
	cfg := PortSitesConfig{
		Enabled: true,
		Sites: []PortSite{{
			Port: 5555, Root: DefaultPortSiteRoot(5555), CustomRoot: false, Running: true,
		}},
	}
	if err := SavePortSites(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := LoadPortSites()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !got.Enabled || len(got.Sites) != 1 || got.Sites[0].Port != 5555 || !got.Sites[0].Running {
		t.Fatalf("往返丢失: %+v", got)
	}
}

func TestCreatePortSiteWritesDefaultIndex(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	site, err := CreatePortSite(port, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if site.CustomRoot || site.Running {
		t.Fatalf("默认站点不该标自定义或已运行: %+v", site)
	}
	index := filepath.Join(site.Root, "index.html")
	body, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("应写入初始 index.html: %v", err)
	}
	wantAddr := "http://127.0.0.1:" + strconv.Itoa(port)
	if !strings.Contains(string(body), strconv.Itoa(port)) {
		t.Fatalf("占位页应提到端口: %s", body)
	}
	if !strings.Contains(string(body), wantAddr) {
		t.Fatalf("占位页应写出真实本机地址: %s", body)
	}
	if !strings.Contains(string(body), "Site is ready") {
		t.Fatalf("默认英文环境应写入英文占位页: %s", body)
	}
	if strings.Contains(string(body), "站点已就绪") {
		t.Fatal("默认英文环境不得写入中文占位文案")
	}
	if !strings.Contains(string(body), `lang="en"`) {
		t.Fatal("英文占位页 html lang 应为 en")
	}
	if strings.Contains(string(body), "Inter") || strings.Contains(strings.ToLower(string(body)), "purple") {
		t.Fatal("占位页不应使用 Inter 或紫色装饰")
	}
	html := string(body)
	if !strings.Contains(html, "frp-ngrok:portsite-placeholder") {
		t.Fatal("占位页应带可识别的生成标记，便于以后只覆盖自动页")
	}
	if !strings.Contains(html, "id=\"copy\"") {
		t.Fatal("占位页应提供复制本机地址的按钮")
	}
	if !strings.Contains(html, "#f4f6f2") {
		t.Fatal("占位页应沿用控制台暖白纸面底色")
	}
	if strings.Contains(html, "fonts.google") || strings.Contains(html, "cdn.") {
		t.Fatal("占位页不得外链字体或 CDN")
	}
	if !IsManagedPortSiteRoot(site.Root, port) {
		t.Fatal("默认目录应识别为插件托管")
	}
	if !DefaultDeleteFiles(site) {
		t.Fatal("托管目录删除时应默认勾选删文件夹")
	}
}

func TestCreatePortSiteWritesChinesePlaceholderWhenLocaleZH(t *testing.T) {
	isolateHome(t)
	if err := SavePrefs(Prefs{Locale: LocaleZH}); err != nil {
		t.Fatal(err)
	}
	port := freeLocalPort(t)
	site, err := CreatePortSite(port, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(site.Root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "站点已就绪") || !strings.Contains(html, `lang="zh-CN"`) {
		t.Fatalf("中文环境应写入中文占位页: %s", html)
	}
	if strings.Contains(html, "Site is ready") {
		t.Fatal("中文环境不得写入英文占位文案")
	}
}

func TestPlaceholderLanguageStaysAtCreateTime(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	site, err := CreatePortSite(port, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := SavePrefs(Prefs{Locale: LocaleZH}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(site.Root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Site is ready") || strings.Contains(string(body), "站点已就绪") {
		t.Fatalf("已创建的占位页不该随后来的语言切换改写: %s", body)
	}
}

func TestWriteDefaultIndexUpgradesGeneratedPlaceholder(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	root := DefaultPortSiteRoot(port)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `<!DOCTYPE html><html><body><h1>站点已就绪</h1><p>这是本地端口管理插件自动生成的占位页。</p></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePortSite(port, ""); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "frp-ngrok:portsite-placeholder") {
		t.Fatalf("旧占位页应被新模板覆盖: %s", html)
	}
	if !strings.Contains(html, "http://127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatal("覆盖后仍应写出真实本机地址")
	}
	if !strings.Contains(html, "Site is ready") {
		t.Fatal("覆盖旧占位页时应按当前语言重写")
	}
}

func TestWriteDefaultIndexPreservesCustomIndex(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	root := DefaultPortSiteRoot(port)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "<!DOCTYPE html><html><body>my-app</body></html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePortSite(port, ""); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != custom {
		t.Fatalf("用户自己的 index.html 不得覆盖: %s", body)
	}
}

func TestCreatePortSiteCustomRootSkipsIndex(t *testing.T) {
	isolateHome(t)
	custom := t.TempDir()
	site, err := CreatePortSite(6000, custom)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if !site.CustomRoot || site.Root != filepath.Clean(custom) {
		t.Fatalf("自定义根目录没记下: %+v", site)
	}
	if _, err := os.Stat(filepath.Join(custom, "index.html")); !os.IsNotExist(err) {
		t.Fatal("第三方目录不该被写入初始页")
	}
	if DefaultDeleteFiles(site) {
		t.Fatal("第三方目录删除时不该默认勾选删文件夹")
	}
}

func TestCreatePortSiteRejectsDuplicateSite(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	if _, err := CreatePortSite(port, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePortSite(port, ""); err == nil || !strings.Contains(err.Error(), "已被本插件另一站点使用") {
		t.Fatalf("重复站点应报中文冲突，实得 %v", err)
	}
}

func TestCreatePortSiteRejectsTunnelLocalPort(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 8888, "web")
	if _, err := CreatePortSite(8888, ""); err == nil || !strings.Contains(err.Error(), "已被隧道占用") {
		t.Fatalf("隧道 localPort 应冲突，实得 %v", err)
	}
}

func TestCreatePortSiteRejectsDefaultConsolePort(t *testing.T) {
	isolateHome(t)
	if _, err := CreatePortSite(paths.DefaultPort, ""); err == nil || !strings.Contains(err.Error(), "控制台") {
		t.Fatalf("默认控制台端口应冲突，实得 %v", err)
	}
}

func TestCreatePortSiteRejectsActualConsolePort(t *testing.T) {
	isolateHome(t)
	if err := os.MkdirAll(paths.AppDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PortFile(), []byte("18111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePortSite(18111, ""); err == nil || !strings.Contains(err.Error(), "控制台") {
		t.Fatalf("实际控制台端口应冲突，实得 %v", err)
	}
}

func TestCreatePortSiteRejectsOccupiedListen(t *testing.T) {
	isolateHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := CreatePortSite(port, ""); err == nil || !strings.Contains(err.Error(), "已被本机其他进程占用") {
		t.Fatalf("本机占用应冲突，实得 %v", err)
	}
}

func TestDeletePortSiteKeepsFilesByDefault(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	site, err := CreatePortSite(port, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeletePortSite(port, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site.Root, "index.html")); err != nil {
		t.Fatalf("不勾选时应保留文件: %v", err)
	}
	cfg, err := LoadPortSites()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sites) != 0 {
		t.Fatalf("应从列表移除: %+v", cfg.Sites)
	}
}

func TestDeletePortSiteRemovesManagedDir(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	site, err := CreatePortSite(port, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeletePortSite(port, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(site.Root); !os.IsNotExist(err) {
		t.Fatal("勾选后应删除托管目录")
	}
}

func TestDeletePortSiteRemovesCustomDirWhenAsked(t *testing.T) {
	isolateHome(t)
	custom := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePortSite(6001, custom); err != nil {
		t.Fatal(err)
	}
	if err := DeletePortSite(6001, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Fatal("勾选后应删除自定义站点目录")
	}
}

func TestSafeJoinSiteRejectsTraversal(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	if _, err := SafeJoinSite(root, "../secret.txt"); err == nil {
		t.Fatal("应拒绝 .. 穿越")
	}
	if _, err := SafeJoinSite(root, "/etc/passwd"); err == nil {
		t.Fatal("应拒绝绝对路径")
	}
	got, err := SafeJoinSite(root, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != root {
		t.Fatalf("合法文件应落在根下: %s", got)
	}
}

func TestSafeJoinSiteDirAllowsRootAndRejectsTraversal(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	got, err := SafeJoinSiteDir(root, "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("空 dir 应落在站点根: %s", got)
	}
	if _, err := SafeJoinSiteDir(root, "../outside"); err == nil {
		t.Fatal("子目录穿越应拒绝")
	}
	if err := os.Mkdir(filepath.Join(root, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	css, err := SafeJoinSiteDir(root, "css")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(css) != "css" {
		t.Fatalf("应进入 css: %s", css)
	}
}

func TestListPortSiteFilesReadsSubdirSortedAndPaged(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "css", "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, dir, err := ListPortSiteFiles(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		t.Fatalf("根目录 dir 应为空，得到 %q", dir)
	}
	if len(files) < 3 {
		t.Fatalf("根目录应列出文件夹和文件: %+v", files)
	}
	if !files[0].IsDir || !files[1].IsDir || files[2].IsDir {
		t.Fatalf("应文件夹在前: %+v", files)
	}
	if files[0].Name != "assets" || files[1].Name != "css" {
		t.Fatalf("文件夹应按名字排: %+v", files)
	}

	inner, dir, err := ListPortSiteFiles(root, "css")
	if err != nil {
		t.Fatal(err)
	}
	if dir != "css" {
		t.Fatalf("当前 dir 应为 css，得到 %q", dir)
	}
	if len(inner) != 1 || inner[0].Name != "app.css" || inner[0].IsDir {
		t.Fatalf("css 目录应只有 app.css: %+v", inner)
	}
	if _, _, err := ListPortSiteFiles(root, "../"); err == nil {
		t.Fatal("列出上级应拒绝")
	}

	page, total, offset, limit := PaginatePortSiteFiles(files, 0, 2)
	if total != len(files) || offset != 0 || limit != 2 || len(page) != 2 {
		t.Fatalf("分页切片不对: page=%+v total=%d offset=%d limit=%d", page, total, offset, limit)
	}
	page2, total2, offset2, _ := PaginatePortSiteFiles(files, 2, 2)
	if total2 != total || offset2 != 2 || len(page2) < 1 {
		t.Fatalf("第二页不对: %+v total=%d offset=%d", page2, total2, offset2)
	}
}

func TestWritePortSiteFileIntoSubdir(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WritePortSiteFile(root, "css", "theme.css", bytes.NewReader([]byte("ok"))); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "css", "theme.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("应写入子目录: %s", body)
	}
	if err := WritePortSiteFile(root, "../", "x.css", bytes.NewReader([]byte("no"))); err == nil {
		t.Fatal("上传穿越应拒绝")
	}
}

func TestSetPortSiteRunningPersists(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	if _, err := CreatePortSite(port, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetPortSiteRunning(port, true); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPortSites()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Sites[0].Running {
		t.Fatal("运行意图应落盘，重启才能自动拉起")
	}
}

func TestCheckPortConflictAcceptsExtraReserved(t *testing.T) {
	isolateHome(t)
	if err := CheckPortConflict(19100, []int{19100}); err == nil || !strings.Contains(err.Error(), "控制台") {
		t.Fatalf("额外预留的控制台端口应冲突，实得 %v", err)
	}
}

func TestPortSiteRootUsesPortName(t *testing.T) {
	isolateHome(t)
	root := DefaultPortSiteRoot(5555)
	if !strings.HasSuffix(root, string(filepath.Separator)+"5555") && !strings.HasSuffix(root, "/5555") {
		t.Fatalf("默认根应是 port-sites/{port}: %s", root)
	}
	if filepath.Base(root) != strconv.Itoa(5555) {
		t.Fatalf("目录名应是端口号: %s", root)
	}
}

func TestDefaultIndexUsesRealPortNotHardcoded(t *testing.T) {
	isolateHome(t)
	port := freeLocalPort(t)
	if port == 5555 {
		port = freeLocalPort(t)
	}
	site, err := CreatePortSite(port, "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(site.Root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	want := "http://127.0.0.1:" + strconv.Itoa(port)
	if !strings.Contains(html, want) {
		t.Fatalf("占位页必须写入创建时的端口 %s: %s", want, html)
	}
	if port != 5555 && strings.Contains(html, "5555") {
		t.Fatal("占位页不得写死 5555")
	}
}

func TestDeletePortSiteFileRemovesFileAndRejectsTraversal(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := DeletePortSiteFile(root, "index.html"); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "index.html")); !os.IsNotExist(err) {
		t.Fatal("文件应被删掉")
	}

	if err := DeletePortSiteFile(root, "../secret.txt"); err == nil {
		t.Fatal("应拒绝路径穿越")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("站外文件不得被删")
	}

	if err := DeletePortSiteFile(root, "assets"); err == nil || !strings.Contains(err.Error(), "文件夹") {
		t.Fatalf("V1 不应删目录，实得 %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets")); err != nil {
		t.Fatal("目录应还在")
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
