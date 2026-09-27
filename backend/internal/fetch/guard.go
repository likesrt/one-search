package fetch

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

// maximumProxyLength 限制代理地址长度，防止把超长串当作代理地址反复尝试解析。
const maximumProxyLength = 512

// ErrPrivateTarget 是「目标被 SSRF 护栏拦下」的哨兵错误。
//
// 单独定义它的唯一目的是让上层能用 errors.Is 判定拦截原因：回退闸门 1 必须据此
// 拒绝把内网 URL 外发给第三方，否则内网地址会被 Tavily 拿到（护栏形同虚设）。
// 用错误文案匹配字符串太脆弱，文案一改判定就静默失效。
//
// 三处拦截点（拨号层 Control、checkTargetLiteral 的地址与主机名分支）都用 %w 包装它；
// net.OpError 与 url.Error 都实现了 Unwrap，因此拨号层的错误能穿透到调用方。
var ErrPrivateTarget = errors.New("已拦截内网地址")

// newGuardDialer 返回一个在建立 TCP 连接前拦截内网地址的拨号器。
//
// 校验放在拨号层而非仅校验 URL，是为了同时覆盖两种绕过手法：一是重定向到内网地址，
// 二是 DNS rebinding（域名本身合法但解析到内网）。这两种情况下只有实际连接的 IP
// 才暴露真实目标，因此必须在连接点判定。
//
// 参数：
//   - allowPrivate：为 true 时不做任何拦截（整层防护关闭）。
//   - trustedProxyIPs：管理员配置的代理主机 IP 集合，命中即放行。
//     代理地址由管理员配置、视为可信，因此放行；这样「用本地代理」就不再需要关掉整个
//     SSRF 防护（源工具把两者绑在同一个开关上，是有意改进的点）。
//     只比对 IP 不比对端口：同 IP 不同端口仍属管理员自己的代理主机，端口还可能因默认值
//     变化，比对端口会造成误拦。
//
// 边界条件：地址串无法拆出 host:port 时返回错误而非放行（宁可失败也不放开不确定的地址）。
// 已知限制：Windows 上 Dialer.Control 的运行时行为未经验证。
func newGuardDialer(allowPrivate bool, trustedProxyIPs map[string]bool) *net.Dialer {
	return &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("无法解析目标地址 %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil || !isPrivateIP(ip) {
				return nil
			}
			if trustedProxyIPs[ip.String()] {
				return nil
			}
			return fmt.Errorf("%w %s", ErrPrivateTarget, host)
		},
	}
}

// isPrivateIP 判断 IP 是否属于不应被外部调用方触达的地址段。
//
// 参数 ip 为已解析的地址，返回 true 表示应当拦截。覆盖范围除标准库的环回、
// 私有网段、链路本地与多播之外，还额外拦截 CGNAT(100.64/10) 与 192.0.0.0/24 ——
// 这两段不在标准库判定内，但同样可能指向运营商或本机内部服务。
//
// 边界条件：IPv4 与 IPv6 均可；无法解析的 nil 返回 false。
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		return ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0
	}
	// IPv6 唯一本地地址 fc00::/7
	return len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc
}

// hostLooksPrivate 按主机名字面量判断是否为内网写法。
//
// 覆盖 localhost 及 .local/.internal 等常见内网后缀。这里是廉价的前置过滤，
// 不替代 DNS 解析后的 IP 校验 —— 域名可能解析到内网，那种情况由拨号层拦截。
// 走代理时拨号层看不到真实目标，该函数是仅剩的一道字面量防护。
func hostLooksPrivate(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// parseProxyURL 解析并校验管理员配置的代理地址。
//
// 空串返回 (nil, nil) 表示直连。仅接受 http、https、socks5、socks5h 四种协议 ——
// 限制协议种类可以缩小攻击面。地址长度受限是为了防止超长字符串被反复当作地址解析。
//
// 返回值：解析成功的 *url.URL；地址为空时为 nil；格式或协议非法时返回错误
// （调用方应记日志并按直连处理，不留存一个可疑的代理）。
func parseProxyURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	if len(value) > maximumProxyLength {
		return nil, fmt.Errorf("代理地址长度不能超过 %d 个字符", maximumProxyLength)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("无法解析代理地址 %q", value)
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q；仅支持 http、https、socks5、socks5h", parsed.Scheme)
	}
	return parsed, nil
}

// resolveProxyIPs 解析代理主机名，返回其全部 IP 的字符串集合。
//
// 域名形式的代理（如容器内的 host.docker.internal）只在这里解析一次，
// 结果作为拨号层的放行名单。
//
// 边界条件：解析失败时返回空集合而非报错 —— 调用方按「裸名单」继续运行，
// 结果是所有私网地址（含该代理）都被拦截，表现为抓取失败而不是静默绕过。
// 这是有意的：宁可抓取失败，也不能放开一个不确定的放行名单。
//
// 副作用：可能触发一次 DNS 查询；失败时写一行日志。
func resolveProxyIPs(proxyURL *url.URL) map[string]bool {
	allowed := map[string]bool{}
	host := proxyURL.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		allowed[ip.String()] = true
		return allowed
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		log.Printf("fetch: 代理主机 %q 解析失败，拨号放行名单为空: %v", host, err)
		return allowed
	}
	for _, ip := range ips {
		allowed[ip.String()] = true
	}
	return allowed
}

// applyProxyTransport 为 transport 装配代理，并在其拨号层保留内网护栏。
//
// 两种代理协议的处理不同：
//   - socks5/socks5h 经 x/net/proxy 适配为拨号器，由它负责与代理握手；
//   - http/https 交给标准库的 Transport.Proxy 处理。
//
// 两种情况下底层拨号器都替换为带护栏的实现：HTTP 代理下护栏会放行代理主机自身
// （在放行名单里），但仍拦截非代理目标；SOCKS5 下护栏实际只作用于「连到代理」这一步，
// 目标侧的字面量兜底检查由 checkTargetLiteral 承担。
//
// 返回值 error 表示该代理不可用（缺少主机名或 SOCKS5 适配失败），
// 调用方应退回直连并记日志。
//
// 副作用：修改传入的 transport。
func applyProxyTransport(transport *http.Transport, proxyURL *url.URL, allowPrivate bool, trustedProxyIPs map[string]bool) error {
	if proxyURL.Hostname() == "" {
		return errors.New("代理地址缺少主机名")
	}
	dialer := newGuardDialer(allowPrivate, trustedProxyIPs)
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		socksDialer, err := proxy.SOCKS5("tcp", proxyURL.Host, socksAuth(proxyURL), dialer)
		if err != nil {
			return fmt.Errorf("代理配置无效: %w", err)
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return errors.New("代理配置无效: 该 SOCKS5 实现不支持上下文取消")
		}
		transport.DialContext = contextDialer.DialContext
	default:
		transport.Proxy = http.ProxyURL(proxyURL)
		transport.DialContext = dialer.DialContext
	}
	return nil
}

// socksAuth 提取代理 URL 中的认证信息，未提供时返回 nil。
func socksAuth(proxyURL *url.URL) *proxy.Auth {
	if proxyURL.User == nil {
		return nil
	}
	password, _ := proxyURL.User.Password()
	return &proxy.Auth{User: proxyURL.User.Username(), Password: password}
}

// checkTargetLiteral 对目标地址做字面量检查，仅走代理时需要。
//
// 走代理时请求由代理代为发出，本机拨号层看到的对端始终是代理地址，无法判断最终目标
// 是否为内网。因此「目标直接写成内网 IP 或 localhost」只能在这一层拦下。
//
// 参数：
//   - target：已解析的目标 URL。
//   - allowPrivate：为 true 时整层防护关闭，直接放行。
//
// 返回值为可读错误（已拦截内网地址/主机名），可直接回传给调用方。
//
// 已知限制：挡不住「域名解析到内网」、「代理自身跟随重定向到内网」，
// 以及「代理被配置为可访问内网」—— 这些取决于代理自身的可信程度。
func checkTargetLiteral(target *url.URL, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	host := target.Hostname()
	if ip := net.ParseIP(host); ip != nil && isPrivateIP(ip) {
		return fmt.Errorf("%w %s", ErrPrivateTarget, host)
	}
	if hostLooksPrivate(host) {
		return fmt.Errorf("%w：内网主机 %s", ErrPrivateTarget, host)
	}
	return nil
}
