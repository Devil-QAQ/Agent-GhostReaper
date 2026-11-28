package websearch

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Endpoint fields are public; credentials are write-only and retained when omitted.
type ProxyChange struct {
	Protocol  string  `json:"protocol"`
	Host      string  `json:"host"`
	Port      int     `json:"port"`
	Username  *string `json:"username,omitempty"`
	Password  *string `json:"password,omitempty"`
	ClearAuth bool    `json:"clear_auth,omitempty"`
}

type ProxyEndpoint struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	HasAuth  bool   `json:"has_auth"`
}

func PublicProxy(raw string) ProxyEndpoint {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ProxyEndpoint{Protocol: "direct"}
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 1080
		if u.Scheme == "http" {
			port = 80
		}
		if u.Scheme == "https" {
			port = 443
		}
	}
	return ProxyEndpoint{Protocol: u.Scheme, Host: u.Hostname(), Port: port, HasAuth: u.User != nil}
}

func (p ProxyChange) apply(previous string) (string, error) {
	if p.Protocol == "direct" {
		return "", nil
	}
	host := strings.TrimSpace(p.Host)
	// IPv6 is accepted without URL brackets; URL syntax belongs to the backend.
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" || strings.ContainsAny(host, "/?#@\\ \t\r\n\x00") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) || p.Port < 1 || p.Port > 65535 {
		return "", errors.New("填写代理地址和 1 至 65535 的端口")
	}
	u := &url.URL{Scheme: p.Protocol, Host: net.JoinHostPort(host, strconv.Itoa(p.Port))}
	old, _ := url.Parse(previous)
	if !p.ClearAuth && old != nil {
		u.User = old.User
	}
	if p.Username != nil || p.Password != nil {
		var name, pass string
		if u.User != nil {
			name = u.User.Username()
			pass, _ = u.User.Password()
		}
		if p.Username != nil {
			name = *p.Username
		}
		if p.Password != nil {
			pass = *p.Password
		}
		if pass != "" {
			u.User = url.UserPassword(name, pass)
		} else {
			u.User = url.User(name)
		}
	}
	if err := ValidateProxy(u.String()); err != nil {
		return "", err
	}
	return u.String(), nil
}

func ValidateProxy(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Host, "\\ \t\r\n\x00") {
		return errors.New("代理地址无效")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return errors.New("代理端口须为 1 至 65535")
		}
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks4a":
	default:
		return errors.New("代理协议须为 HTTP、HTTPS、SOCKS4a 或 SOCKS5")
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		if strings.ContainsRune(u.User.Username(), 0) || strings.ContainsRune(pass, 0) {
			return errors.New("代理认证格式无效")
		}
		if u.Scheme == "socks4a" && pass != "" {
			return errors.New("SOCKS4a 不支持密码，请清除认证或选择其他协议")
		}
	}
	return nil
}

// SOCKS4a resolves destination names at the proxy, without a local DNS lookup.
func dialSOCKS4a(ctx context.Context, proxyURL *url.URL, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("SOCKS4a 仅支持 TCP")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsRune(host, 0) {
		return nil, errors.New("SOCKS4a 目标地址无效")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("SOCKS4a 目标端口无效")
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return nil, errors.New("SOCKS4a 不支持 IPv6 目标地址")
	}
	endpoint := PublicProxy(proxyURL.String())
	conn, err := (&net.Dialer{Timeout: searchTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)))
	if err != nil {
		return nil, errors.New("SOCKS4a 代理连接失败")
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	deadline := time.Now().Add(searchTimeout)
	if at, exists := ctx.Deadline(); exists && at.Before(deadline) {
		deadline = at
	}
	conn.SetDeadline(deadline)
	// Cancellation must interrupt a stalled handshake even without a deadline.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	user := ""
	if proxyURL.User != nil {
		user = proxyURL.User.Username()
	}
	request := []byte{4, 1, 0, 0, 0, 0, 0, 1}
	binary.BigEndian.PutUint16(request[2:4], uint16(port))
	request = append(request, []byte(user)...)
	request = append(request, 0)
	request = append(request, []byte(host)...)
	request = append(request, 0)
	for len(request) > 0 {
		n, e := conn.Write(request)
		if e != nil {
			return nil, errors.New("SOCKS4a 请求未发送完整")
		}
		if n == 0 {
			return nil, io.ErrShortWrite
		}
		request = request[n:]
	}
	var reply [8]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, errors.New("SOCKS4a 响应未读取完整")
	}
	if reply[0] != 0 || reply[1] != 90 {
		return nil, errors.New("SOCKS4a 代理拒绝连接")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}
