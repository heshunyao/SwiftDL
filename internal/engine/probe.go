package engine

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Meta 是探测到的远端文件信息。
type Meta struct {
	Size           int64  `json:"size"`
	RangeSupported bool   `json:"rangeSupported"`
	FinalURL       string `json:"finalUrl"`
	ETag           string `json:"etag"`
	LastModified   string `json:"lastModified"`
	Filename       string `json:"filename"`
	ContentType    string `json:"contentType"`
}

// Probe 用 Range: bytes=0-0 探测文件大小、是否支持分块，并推导文件名。
func Probe(ctx context.Context, client *http.Client, rawURL string) (*Meta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	meta := &Meta{
		FinalURL:     rawURL,
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
		ContentType:  resp.Header.Get("Content-Type"),
		Size:         -1,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		meta.FinalURL = resp.Request.URL.String()
	}

	switch resp.StatusCode {
	case http.StatusPartialContent:
		meta.RangeSupported = true
		meta.Size = parseContentRange(resp.Header.Get("Content-Range"))
		if meta.Size <= 0 && resp.ContentLength > 0 {
			meta.Size = resp.ContentLength
		}
	case http.StatusOK:
		meta.Size = resp.ContentLength
		meta.RangeSupported = strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes")
	case http.StatusRequestedRangeNotSatisfiable:
		// 空文件或服务器拒绝全部区间
		meta.Size = 0
		return meta, fmt.Errorf("服务器拒绝 Range 请求（文件可能为空或地址失效）")
	default:
		return meta, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	meta.Filename = filenameFrom(resp, rawURL)
	return meta, nil
}

// parseContentRange 解析 "bytes 0-0/12345" 中的总长度。
func parseContentRange(v string) int64 {
	i := strings.LastIndex(v, "/")
	if i < 0 {
		return -1
	}
	total := strings.TrimSpace(v[i+1:])
	if total == "*" || total == "" {
		return -1
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// filenameFrom 依次尝试 Content-Disposition、最终 URL 路径。
func filenameFrom(resp *http.Response, rawURL string) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn := params["filename"]; fn != "" {
				return sanitizeFilename(fn)
			}
		}
	}
	u := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		u = resp.Request.URL.String()
	}
	if parsed, err := url.Parse(u); err == nil {
		if base := path.Base(parsed.Path); base != "" && base != "/" && base != "." {
			if unescaped, err2 := url.PathUnescape(base); err2 == nil {
				base = unescaped
			}
			return sanitizeFilename(base)
		}
	}
	return "download.bin"
}

// sanitizeFilename 去掉 Windows 非法字符。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer(
		"\\", "_", "/", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	name = replacer.Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "download.bin"
	}
	return name
}

// drain 读取少量剩余数据以便连接复用。
func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 32*1024))
}
