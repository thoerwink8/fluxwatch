// Package mihomo 找到 mihomo（Clash Party 等）的控制接口，读 /connections。
//
// 接口有三种形态：mac / Linux 上的 Unix socket（Clash Party 默认，免密钥）、
// Windows 上的命名管道（未在真机验证）、以及 external-controller TCP 地址 + secret（手填）。
package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Kind 是控制接口的形态。
type Kind string

const (
	KindUnix Kind = "unix"
	KindPipe Kind = "pipe"
	KindTCP  Kind = "tcp"
)

// Endpoint 是一个找到的控制接口。
type Endpoint struct {
	Kind   Kind
	Addr   string // socket 路径、管道名或 host:port
	Secret string // 只有 TCP 用
}

func (e Endpoint) String() string {
	return string(e.Kind) + ":" + e.Addr
}

// Manual 是手填的控制器：Addr 非空时优先，不再自动探测。
type Manual struct {
	Addr   string // host:port，或带 http:// 前缀
	Secret string
}

// ErrNotFound 表示自动探测没找到能连上的接口。
var ErrNotFound = errors.New("没找到 mihomo 控制接口")

// Locate 先看手填，再自动探测（平台相关）。只返回 /version 能答上的接口。
func Locate(ctx context.Context, m Manual) (Endpoint, error) {
	if addr := strings.TrimSpace(m.Addr); addr != "" {
		addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
		addr = strings.TrimSuffix(addr, "/")
		ep := Endpoint{Kind: KindTCP, Addr: addr, Secret: m.Secret}
		if _, err := New(ep).Version(ctx); err != nil {
			return Endpoint{}, fmt.Errorf("手填的 mihomo 控制器 %s 连不上：%w", addr, err)
		}
		return ep, nil
	}
	return autoLocate(ctx)
}

// Client 通过一个 Endpoint 说 HTTP。
type Client struct {
	ep   Endpoint
	http *http.Client
	base string
}

// New 按接口形态造 HTTP 客户端；socket / 管道上也照常走 HTTP（/connections 可能是 chunked，标准库会处理）。
func New(ep Endpoint) *Client {
	tr := &http.Transport{DisableCompression: true, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second}
	base := "http://" + ep.Addr
	switch ep.Kind {
	case KindUnix:
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", ep.Addr)
		}
		base = "http://mihomo"
	case KindPipe:
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialPipe(ctx, ep.Addr)
		}
		base = "http://mihomo"
	}
	return &Client{ep: ep, base: base, http: &http.Client{Transport: tr, Timeout: 3 * time.Second}}
}

// Endpoint 返回客户端连的接口。
func (c *Client) Endpoint() Endpoint { return c.ep }

func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.ep.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.ep.Secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%s 返回 401：secret 不对", path)
		}
		return fmt.Errorf("%s 返回 HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("%s 不是合法 JSON：%w", path, err)
	}
	return nil
}

// Version 读 /version；用来确认接口真是 mihomo。
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.get(ctx, "/version", &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", errors.New("/version 没有版本号，不像 mihomo")
	}
	return v.Version, nil
}

// Metadata 是 /connections 里每条连接的 metadata（只取用得到的字段）。
type Metadata struct {
	Network       string `json:"network"`
	Host          string `json:"host"`
	SniffHost     string `json:"sniffHost"`
	DestinationIP string `json:"destinationIP"`
	SourceIP      string `json:"sourceIP"`
	SourcePort    string `json:"sourcePort"`
	Process       string `json:"process"`
}

// Connection 是 /connections 的一条。
type Connection struct {
	ID       string    `json:"id"`
	Metadata Metadata  `json:"metadata"`
	Upload   int64     `json:"upload"`
	Download int64     `json:"download"`
	Start    time.Time `json:"start"`
	Chains   []string  `json:"chains"`
}

// HostName 取连接的域名：host，没有就 sniffHost，再没有就目标 IP。
func (c Connection) HostName() string {
	switch {
	case c.Metadata.Host != "":
		return strings.ToLower(c.Metadata.Host)
	case c.Metadata.SniffHost != "":
		return strings.ToLower(c.Metadata.SniffHost)
	default:
		return c.Metadata.DestinationIP
	}
}

// Node 取出口节点名：chains[0]。
func (c Connection) Node() string {
	if len(c.Chains) == 0 {
		return ""
	}
	return c.Chains[0]
}

// Connections 是 /connections 的回包。
type Connections struct {
	DownloadTotal int64        `json:"downloadTotal"`
	UploadTotal   int64        `json:"uploadTotal"`
	Connections   []Connection `json:"connections"`
}

// Connections 读一次 /connections。
func (c *Client) Connections(ctx context.Context) (*Connections, error) {
	var v Connections
	if err := c.get(ctx, "/connections", &v); err != nil {
		return nil, err
	}
	return &v, nil
}
