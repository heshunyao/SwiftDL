package engine

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

// UserAgent 统一请求标识。
const UserAgent = "SwiftDL/1.0"

// NewClient 创建共享的 HTTP 客户端：
//   - 连接池复用，避免每个分块都重新握手
//   - 开启 HTTP/2 协商（服务器支持时多路复用）
//   - proxy 为空时使用系统环境变量中的代理
func NewClient(proxy string) *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil && u.Host != "" {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{
		Transport: transport,
		// 不设置整体超时，大文件下载靠 context 控制
		Timeout: 0,
	}
}
