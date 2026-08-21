package accesslog

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func parseLogTime(t *testing.T) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04:05.000", "2026-08-18 01:45:01.000", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func readAllString(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	b, err := io.ReadAll(r.Body)
	return string(b), err
}

func TestSnapshotRequestCapturesJSONAndForm(t *testing.T) {
	jsonReq, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/login", strings.NewReader(`{"account":"neo"}`))
	if err != nil {
		t.Fatal(err)
	}
	jsonReq.Header.Set("Content-Type", "application/json")
	if got := snapshotRequest(jsonReq); !strings.Contains(got, `"account":"neo"`) {
		t.Fatalf("JSON 体没记下来: %q", got)
	}
	rest, err := readAllString(jsonReq)
	if err != nil || rest != `{"account":"neo"}` {
		t.Fatalf("快照后请求体应还能转发给后端，实得 %q / %v", rest, err)
	}

	formReq, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/form", strings.NewReader("a=1&b=2"))
	if err != nil {
		t.Fatal(err)
	}
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := snapshotRequest(formReq); !strings.Contains(got, "a=1") || !strings.Contains(got, "b=2") {
		t.Fatalf("表单没记下来: %q", got)
	}
}

func TestSnapshotRequestSkipsEmptyAndBinary(t *testing.T) {
	getReq, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotRequest(getReq); got != "" {
		t.Fatalf("GET 无体不应记 payload，实得 %q", got)
	}

	bin := strings.Repeat("\x00\x01", 32)
	binReq, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/upload", strings.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	binReq.Header.Set("Content-Type", "application/octet-stream")
	got := snapshotRequest(binReq)
	if !strings.Contains(got, "octet-stream") || strings.Contains(got, "\x00") {
		t.Fatalf("二进制应只记类型和大小，实得 %q", got)
	}
}

func TestSnapshotRequestParsesMultipartFormFields(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("name", "jame"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("city", "beijing"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("avatar", "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("PNGDATA")); err != nil {
		t.Fatal(err)
	}
	ct := mw.FormDataContentType()
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()

	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/?name=jame", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ct)

	got := snapshotRequest(req)
	if !strings.Contains(got, "name=jame") || !strings.Contains(got, "city=beijing") {
		t.Fatalf("multipart 文本字段应解析出来，实得 %q", got)
	}
	if strings.Contains(got, "PNGDATA") {
		t.Fatalf("文件内容不该进日志: %q", got)
	}
	if !strings.Contains(got, "avatar=@a.png") {
		t.Fatalf("文件字段应记下文件名: %q", got)
	}

	rest, err := readAllString(req)
	if err != nil || rest != raw {
		t.Fatalf("解析后仍应把原始 multipart 转发给后端，长度 %d / %d / %v", len(rest), len(raw), err)
	}
}

func TestFormatLineAppendsPayload(t *testing.T) {
	got := FormatLine(parseLogTime(t), "1.2.3.4", "POST", "/login", 401, 0, `{"a":1}`)
	if !strings.Contains(got, `POST /login  401  0ms  {"a":1}`) {
		t.Fatalf("日志行应带上参数: %q", got)
	}
	plain := FormatLine(parseLogTime(t), "1.2.3.4", "GET", "/", 200, 0, "")
	if strings.Contains(plain, "  200  0ms  \n") {
		t.Fatalf("没有参数时不该多出空字段: %q", plain)
	}
}
