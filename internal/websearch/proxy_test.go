package websearch

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProxyChangeAndCredentials(t *testing.T) {
	store := openStore(t)
	list, err := SaveService(t.Context(), store, ServiceChange{Name: "GitHub", Config: Config{Kind: "github"}, Proxy: pointer("http://name:private-password@old.example:7897")}, "test")
	if err != nil {
		t.Fatal(err)
	}
	change := ServiceChange{ID: list[0].ID, Name: "GitHub", Config: Config{Kind: "github"}, ProxyConfig: &ProxyChange{Protocol: "socks5", Host: "::1", Port: 1080}}
	list, err = SaveService(t.Context(), store, change, "test")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(list[0].Proxy)
	pass, _ := u.User.Password()
	if u.Host != "[::1]:1080" || u.User.Username() != "name" || pass != "private-password" {
		t.Fatal("credential preservation failed")
	}
	v := PublicProxy(list[0].Proxy)
	if v.Protocol != "socks5" || v.Host != "::1" || v.Port != 1080 || !v.HasAuth {
		t.Fatal(v)
	}
	change.ProxyConfig.Protocol = "socks4a"
	if _, err := SaveService(t.Context(), store, change, "test"); err == nil {
		t.Fatal("SOCKS4a silently dropped password")
	}
	change.ProxyConfig.ClearAuth = true
	list, err = SaveService(t.Context(), store, change, "test")
	if err != nil || PublicProxy(list[0].Proxy).HasAuth {
		t.Fatal("explicit clear failed", err)
	}
	change.ProxyConfig.Protocol = "direct"
	list, err = SaveService(t.Context(), store, change, "test")
	if err != nil || list[0].Proxy != "" {
		t.Fatal("direct did not clear proxy", err)
	}
	change.Proxy = pointer("")
	if _, err := SaveService(t.Context(), store, change, "test"); err == nil {
		t.Fatal("conflicting formats accepted")
	}
	for _, raw := range []string{"socks4a://user@localhost:1080", "socks5://localhost", "http://localhost:8080", "https://localhost"} {
		if err := ValidateProxy(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"socks4a://u:p@localhost:1080", "http://localhost:0", "http://localhost:65536", "socks4://localhost:1080", "http://localhost/proxy", "http://localhost?q=secret"} {
		if err := ValidateProxy(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestSOCKS4aHTTPAndRemoteDNS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		r := bufio.NewReader(conn)
		var h [8]byte
		_, err = io.ReadFull(r, h[:])
		if err != nil {
			done <- err
			return
		}
		user, _ := r.ReadString(0)
		host, _ := r.ReadString(0)
		if h[0] != 4 || h[1] != 1 || binary.BigEndian.Uint16(h[2:4]) != 80 || h[4] != 0 || h[7] != 1 || user != "tester\x00" || host != "remote-only.invalid\x00" {
			t.Error("invalid remote DNS handshake")
		}
		// Fragmented replies exercise full response reading.
		conn.Write([]byte{0, 90, 0})
		conn.Write([]byte{0, 0, 0, 0, 0})
		req, err := http.ReadRequest(r)
		if err != nil {
			done <- err
			return
		}
		if req.Host != "remote-only.invalid" || req.URL.Path != "/file" {
			t.Error("HTTP tunnel target changed")
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		done <- err
	}()
	client, err := clientFor("socks4a://tester@" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://remote-only.invalid/file")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "ok" {
		t.Fatal("tunnel failed", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS4aRejectAndCancel(t *testing.T) {
	for _, mode := range []string{"reject", "short", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			accepted := make(chan struct{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				close(accepted)
				if mode == "reject" {
					c.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
				}
				if mode == "short" {
					c.Write([]byte{0, 90})
				}
				if mode == "cancel" {
					c.SetReadDeadline(time.Now().Add(3 * time.Second))
					io.Copy(io.Discard, c)
				}
			}()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel" {
				go func() { <-accepted; cancel() }()
			}
			u, _ := url.Parse("socks4a://" + ln.Addr().String())
			start := time.Now()
			conn, err := dialSOCKS4a(ctx, u, "tcp", "unresolved.invalid:443")
			if conn != nil {
				conn.Close()
			}
			if err == nil {
				t.Fatal("accepted failed handshake")
			}
			if strings.Contains(err.Error(), "unresolved") {
				t.Fatal("unexpected target disclosure")
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("cancellation did not interrupt handshake")
			}
			<-finished
		})
	}
}
