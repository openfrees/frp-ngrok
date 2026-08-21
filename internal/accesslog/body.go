package accesslog

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	maxLoggedBody   = 8 << 10 // 日志里最多留 8KB，避免一条请求撑爆文件
	maxBufferedBody = 1 << 20 // 为了原样转发给本机服务，最多在内存里缓冲 1MB
)

// snapshotRequest 抄一份请求体供日志使用，并把原内容塞回 Body，后端仍能读到。
func snapshotRequest(r *http.Request) string {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return ""
	}
	media := mediaType(r.Header.Get("Content-Type"))

	limited, err := io.ReadAll(io.LimitReader(r.Body, maxBufferedBody+1))
	if err != nil {
		return ""
	}
	if int64(len(limited)) > maxBufferedBody {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(limited), r.Body))
		return fmt.Sprintf("[body >%dKB，已省略]", maxBufferedBody/1024)
	}
	_ = r.Body.Close()
	body := limited
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	if len(body) == 0 {
		return ""
	}
	if strings.HasPrefix(media, "multipart/") {
		return snapshotMultipart(body, r.Header.Get("Content-Type"))
	}
	if skipBodyMedia(media) || !looksLikeText(body) {
		if media == "" {
			media = "binary"
		}
		return fmt.Sprintf("[%s %dB]", media, len(body))
	}
	logged := body
	truncated := false
	if len(logged) > maxLoggedBody {
		logged = logged[:maxLoggedBody]
		truncated = true
		for !utf8.Valid(logged) && len(logged) > 0 {
			logged = logged[:len(logged)-1]
		}
	}
	text := compactLogText(string(logged))
	if truncated {
		text += "…"
	}
	return text
}

func mediaType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

func skipBodyMedia(media string) bool {
	if media == "" {
		return false
	}
	switch {
	case media == "application/octet-stream":
		return true
	case strings.HasPrefix(media, "image/"):
		return true
	case strings.HasPrefix(media, "video/"):
		return true
	case strings.HasPrefix(media, "audio/"):
		return true
	}
	return false
}

func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}

func compactLogText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}

func snapshotMultipart(body []byte, contentType string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || strings.TrimSpace(params["boundary"]) == "" {
		return fmt.Sprintf("[multipart/form-data %dB]", len(body))
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var fields []string
	for {
		part, nextErr := mr.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			break
		}
		name := part.FormName()
		if name == "" {
			name = "field"
		}
		filename := part.FileName()
		slurp, _ := io.ReadAll(io.LimitReader(part, maxLoggedBody+1))
		_ = part.Close()
		if filename != "" {
			fields = append(fields, fmt.Sprintf("%s=@%s(%dB)", name, filename, len(slurp)))
			continue
		}
		if !looksLikeText(slurp) {
			fields = append(fields, fmt.Sprintf("%s=[binary %dB]", name, len(slurp)))
			continue
		}
		val := compactLogText(string(slurp))
		if len(val) > 512 {
			val = val[:512] + "…"
		}
		fields = append(fields, name+"="+val)
	}
	if len(fields) == 0 {
		return fmt.Sprintf("[multipart/form-data %dB]", len(body))
	}
	text := strings.Join(fields, "&")
	if len(text) > maxLoggedBody {
		text = text[:maxLoggedBody] + "…"
	}
	return text
}
