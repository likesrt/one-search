package fetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestIsPrivateIPBlockedRanges 覆盖必须拦截的地址段。
//
// 除标准库判定的环回、私网、链路本地、多播之外，还包含 CGNAT(100.64/10) 与
// 192.0.0.0/24 —— 这两段不在标准库判定内，但同样可能指向运营商或本机内部服务。
func TestIsPrivateIPBlockedRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.1", "172.16.5.4", "192.168.1.1", "fc00::1", "fd12:3456::1",
		"169.254.169.254", "fe80::1",
		"0.0.0.0", "224.0.0.1", "ff02::1",
		"100.64.0.1", "100.127.255.254",
		"192.0.0.1", "192.0.0.255",
	}
	for _, raw := range blocked {
		t.Run("拦截 "+raw, func(t *testing.T) {
			ip := net.ParseIP(raw)
			if ip == nil {
				t.Fatalf("解析测试地址失败: %s", raw)
			}
			if !isPrivateIP(ip) {
				t.Fatalf("%s 应被判定为内网地址", raw)
			}
		})
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111", "100.63.255.255", "100.128.0.1", "192.0.1.1"}
	for _, raw := range allowed {
		t.Run("放行 "+raw, func(t *testing.T) {
			ip := net.ParseIP(raw)
			if ip == nil {
				t.Fatalf("解析测试地址失败: %s", raw)
			}
			if isPrivateIP(ip) {
				t.Fatalf("%s 不应被判定为内网地址", raw)
			}
		})
	}
}

// TestFetchBlocksPrivateTargets 验证护栏在抓取主流程上真的生效。
//
// 用假地址断言错误：全程不发起真实网络请求，因此不依赖外网。
// 覆盖 IP 字面量与内网主机名两种写法。
func TestFetchBlocksPrivateTargets(t *testing.T) {
	fetcher := NewFetcher(Config{Timeout: 2 * time.Second})
	targets := []string{
		"http://127.0.0.1:9/x",
		"http://10.0.0.5/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/x",
		"http://192.0.0.1/x",
		"http://[::1]:9/x",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			request := mustParseRequest(t, map[string]any{"url": target})
			if _, err := fetcher.Fetch(context.Background(), request); err == nil {
				t.Fatalf("%s 应被 SSRF 护栏拦截", target)
			}
		})
	}
}

// TestFetchAllowPrivatePermitsLoopback 验证 AllowPrivate 打开后环回目标可访问。
func TestFetchAllowPrivatePermitsLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	request := mustParseRequest(t, map[string]any{"url": server.URL})
	if _, err := NewFetcher(Config{AllowPrivate: true, Timeout: 5 * time.Second}).
		Fetch(context.Background(), request); err != nil {
		t.Fatalf("AllowPrivate 打开后应放行环回目标: %v", err)
	}
}

// TestGuardDialerTrustsConfiguredProxy 是本功能相对源工具的核心改动断言：
// 管理员配置的代理地址被视为可信（拨号层放行），而其他私网目标仍被拦截。
//
// 断言分两层：
//  1. guard 层：放行名单里的 IP 通过，名单外的私网 IP 被拦；
//  2. 端到端：配置一个指向 httptest 服务器的「代理」（127.0.0.1）后，
//     经它抓取公网字面量地址不会被拨号护栏拦住代理本身（错误来自连接阶段而非护栏）。
func TestGuardDialerTrustsConfiguredProxy(t *testing.T) {
	dialer := newGuardDialer(false, map[string]bool{"127.0.0.1": true})
	raw := &fakeRawConn{}
	if err := dialer.Control("tcp", "127.0.0.1:7890", raw); err != nil {
		t.Fatalf("已配置代理的 IP 应被放行: %v", err)
	}
	if err := dialer.Control("tcp", "10.1.2.3:7890", raw); err == nil {
		t.Fatal("非代理的私网地址仍应被拦截")
	}
	if err := dialer.Control("tcp", "8.8.8.8:443", raw); err != nil {
		t.Fatalf("公网地址应放行: %v", err)
	}
	// 无法拆出 host:port 时返回错误而非放行
	if err := dialer.Control("tcp", "not-an-address", raw); err == nil {
		t.Fatal("地址无法解析时应报错而非放行")
	}
	// 开关打开时整层放行
	open := newGuardDialer(true, nil)
	if err := open.Control("tcp", "10.1.2.3:80", raw); err != nil {
		t.Fatalf("AllowPrivate 打开后应放行: %v", err)
	}
}

// fakeRawConn 是 syscall.RawConn 的最小替身：护栏不用它，只是签名要求。
type fakeRawConn struct{}

// Control 是 syscall.RawConn 接口的占位实现；护栏不使用它，仅为满足类型要求。
func (f *fakeRawConn) Control(func(uintptr)) error { return nil }

// Read 是 syscall.RawConn 接口的占位实现；护栏不使用它，仅为满足类型要求。
func (f *fakeRawConn) Read(func(uintptr) bool) error { return nil }

// Write 是 syscall.RawConn 接口的占位实现；护栏不使用它，仅为满足类型要求。
func (f *fakeRawConn) Write(func(uintptr) bool) error { return nil }

// TestProxyClientKeepsPrivateTargetBlocked 验证走代理时目标侧的字面量兜底检查：
// 目标直接写成内网地址时必须被拦，即使代理本身可信。
func TestProxyClientKeepsPrivateTargetBlocked(t *testing.T) {
	fetcher := NewFetcher(Config{ProxyURL: "http://127.0.0.1:7890", Timeout: time.Second})
	if fetcher.proxy == nil {
		t.Fatal("合法代理应被保留")
	}
	for _, target := range []string{"http://10.0.0.1/x", "http://localhost:8080/x", "http://169.254.169.254/"} {
		t.Run(target, func(t *testing.T) {
			request := mustParseRequest(t, map[string]any{"url": target})
			_, err := fetcher.Fetch(context.Background(), request)
			if err == nil {
				t.Fatalf("%s 应被拦截", target)
			}
			if !strings.Contains(err.Error(), "已拦截") {
				t.Fatalf("错误应说明被护栏拦截，实际: %v", err)
			}
		})
	}
	// AllowPrivate 打开后字面量检查被跳过（护栏整体关闭），此时失败原因转为连接失败
	openFetcher := NewFetcher(Config{ProxyURL: "http://127.0.0.1:9", AllowPrivate: true, Timeout: time.Second})
	request := mustParseRequest(t, map[string]any{"url": "http://127.0.0.1:9/x"})
	if _, err := openFetcher.Fetch(context.Background(), request); err == nil ||
		strings.Contains(err.Error(), "已拦截") {
		t.Fatalf("AllowPrivate 打开后不应再报护栏拦截: %v", err)
	}
}

// TestHostLooksPrivate 覆盖内网主机名的字面量判定。
func TestHostLooksPrivate(t *testing.T) {
	private := []string{"localhost", "LOCALHOST", "localhost.", "api.local", "svc.internal", "x.home.arpa", "foo.localhost"}
	for _, host := range private {
		if !hostLooksPrivate(host) {
			t.Fatalf("%s 应被判定为内网主机名", host)
		}
	}
	for _, host := range []string{"example.com", "localhost.example.com", "internal.example.com"} {
		if hostLooksPrivate(host) {
			t.Fatalf("%s 不应被判定为内网主机名", host)
		}
	}
}

// TestParseProxyURL 覆盖代理地址解析的合法与非法形态。
func TestParseProxyURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:7890", "https://proxy.example.com:8443", "socks5://user:pass@127.0.0.1:1080", "socks5h://127.0.0.1:1080"} {
		t.Run("合法 "+raw, func(t *testing.T) {
			parsed, err := parseProxyURL(raw)
			if err != nil || parsed == nil {
				t.Fatalf("应解析成功: %v", err)
			}
		})
	}
	for _, raw := range []string{"ftp://example.com:21", "http://", "not a url", "://x"} {
		t.Run("非法 "+raw, func(t *testing.T) {
			if _, err := parseProxyURL(raw); err == nil {
				t.Fatalf("%q 应被拒绝", raw)
			}
		})
	}
	if parsed, err := parseProxyURL("   "); err != nil || parsed != nil {
		t.Fatalf("空代理应返回 (nil, nil)，实际 (%v, %v)", parsed, err)
	}
	// 超长地址被拒，避免被反复当作地址解析
	if _, err := parseProxyURL("http://example.com/" + strings.Repeat("a", maximumProxyLength)); err == nil {
		t.Fatal("超长代理地址应被拒绝")
	}
}

// TestResolveProxyIPs 验证代理主机名解析结果，失败时返回空名单（宁可失败也不放开）。
func TestResolveProxyIPs(t *testing.T) {
	literal, _ := url.Parse("http://127.0.0.1:7890")
	if ips := resolveProxyIPs(literal); !ips["127.0.0.1"] || len(ips) != 1 {
		t.Fatalf("字面量 IP 应直接进名单: %v", ips)
	}
	// 不存在的域名：解析失败返回空集合而非 panic
	missing, _ := url.Parse("http://does-not-exist.invalid:1")
	if ips := resolveProxyIPs(missing); len(ips) != 0 {
		t.Fatalf("解析失败应返回空名单: %v", ips)
	}
}

// TestSocksAuth 验证代理认证信息提取。
func TestSocksAuth(t *testing.T) {
	withAuth, _ := url.Parse("socks5://user:secret@127.0.0.1:1080")
	auth := socksAuth(withAuth)
	if auth == nil || auth.User != "user" || auth.Password != "secret" {
		t.Fatalf("认证信息提取不正确: %+v", auth)
	}
	withoutAuth, _ := url.Parse("socks5://127.0.0.1:1080")
	if socksAuth(withoutAuth) != nil {
		t.Fatal("无认证信息时应返回 nil")
	}
}
