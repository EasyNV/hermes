package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// Result holds the outcome of a proxy health check.
type Result struct {
	Reachable  bool
	CanRoute   bool
	LatencyMs  int32
	ExternalIP string
}

// Checker performs connectivity tests on proxies.
type Checker interface {
	Check(ctx context.Context, host string, port int32, proxyType, username, password string) *Result
}

// NetChecker implements Checker using real network calls.
type NetChecker struct {
	Timeout time.Duration
}

func NewChecker(timeout time.Duration) *NetChecker {
	return &NetChecker{Timeout: timeout}
}

func (c *NetChecker) Check(ctx context.Context, host string, port int32, proxyType, username, password string) *Result {
	result := &Result{}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	// TCP probe — measure latency and basic reachability.
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, c.Timeout)
	if err != nil {
		return result
	}
	conn.Close()
	result.Reachable = true
	result.LatencyMs = int32(time.Since(start).Milliseconds())

	// HTTP probe through proxy — verify traffic routing and discover external IP.
	transport := c.buildTransport(addr, proxyType, username, password)
	if transport == nil {
		return result
	}

	client := &http.Client{Transport: transport, Timeout: c.Timeout}
	resp, err := client.Get("http://api.ipify.org")
	if err != nil {
		return result
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return result
	}

	result.CanRoute = true
	result.ExternalIP = strings.TrimSpace(string(body))
	return result
}

func (c *NetChecker) buildTransport(addr, proxyType, username, password string) *http.Transport {
	if proxyType == "http" {
		proxyURL := &url.URL{Scheme: "http", Host: addr}
		if username != "" {
			proxyURL.User = url.UserPassword(username, password)
		}
		return &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}

	// SOCKS5
	var auth *proxy.Auth
	if username != "" {
		auth = &proxy.Auth{User: username, Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
	if err != nil {
		return nil
	}
	return &http.Transport{
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		},
	}
}
