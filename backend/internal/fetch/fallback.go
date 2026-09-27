package fetch

import (
	"context"
	"errors"
	"net/http"
)

// fallbackGateReason 描述回退被哪道闸门拦下，用于日志与排错。
//
// 四道闸门的处置在代码上收敛为同一段逻辑（有内置结果就返回内置结果，没有就返回内置错误），
// 但原因必须分开记录：闸门 4 的语义是「raw 契约无法用 Markdown 替代」，
// 与闸门 1/2/3 的「能力与安全边界」不同，混在一起会让日志失去诊断价值。
type fallbackGateReason string

const (
	// gateNone 表示未被任何闸门拦下
	gateNone fallbackGateReason = ""
	// gatePrivateTarget 拦截内网目标：外发给第三方等于把 SSRF 护栏放在一个不生效的位置
	gatePrivateTarget fallbackGateReason = "private_target"
	// gateNotGet 拦截非 GET 请求：extract 只能 GET，重放 POST 可能产生副作用
	gateNotGet fallbackGateReason = "not_get"
	// gateCustomHeaders 拦截带自定义请求头的请求：可能是 Authorization / Cookie，不得外发
	gateCustomHeaders fallbackGateReason = "custom_headers"
	// gateRaw 拦截 raw 请求：raw 的契约是「原样返回源文本」，Markdown 无法替代
	gateRaw fallbackGateReason = "raw"
)

// 触发回退的上游状态码集合。刻意不含 404 与任何 5xx：
// 它们是对端服务端问题（或明确的「资源不存在」），重试无意义，也不该消耗第三方额度。
var fallbackStatuses = map[int]bool{
	http.StatusUnauthorized:    true,
	http.StatusForbidden:       true,
	http.StatusTooManyRequests: true,
}

// fallbackBlocked 判定四道闸门，返回被拦原因（gateNone 表示放行）。
//
// 闸门 1 只看 ErrPrivateTarget 哨兵，**不重复做目标字面量检查**：AllowPrivate 为 false 时
// 拨号层与 doRequest 的字面量检查都已把内网目标包装成该哨兵错误，重复判断没有额外收益；
// 而 AllowPrivate 为 true 属于管理员显式声明「本部署允许抓内网」，此时若仍拒绝回退，
// 会让整套回退在测试与内网部署中完全不可用。
// 已知代价：打开 AllowPrivate 后内网地址可能被送到第三方通道，见文档中的安全提示。
//
// 参数 fetchErr 是内置抓取的传输层错误（成功时为 nil）。本函数为纯函数，无副作用。
func fallbackBlocked(req Request, fetchErr error) fallbackGateReason {
	if errors.Is(fetchErr, ErrPrivateTarget) {
		return gatePrivateTarget
	}
	if req.Method != http.MethodGet || req.HasBody {
		return gateNotGet
	}
	if len(req.Headers) > 0 {
		return gateCustomHeaders
	}
	if req.Raw {
		return gateRaw
	}
	return gateNone
}

// shouldFallback 判定内置抓取结果是否落入触发条件。
//
// 三条触发条件是「或」的关系：传输层失败、状态码属于 fallbackStatuses、
// 可见文本长度低于阈值。任一命中即触发（是否真正回退还要过 fallbackBlocked 的闸门）。
//
// 判定用「可见文本」而不是原始长度：内联 base64 图片会让质询页看起来很长，见 visibleTextLength。
// 参数 minChars 为阈值（调用方已收敛为正数）。本函数为纯函数，无副作用。
func shouldFallback(entry CacheEntry, fetchErr error, minChars int) bool {
	if fetchErr != nil {
		return true
	}
	if fallbackStatuses[entry.StatusCode] {
		return true
	}
	return visibleTextLength(entry.Content) < minChars
}

// applyFallback 在内置抓取之后按需调用回退通道，返回最终应采用的结果。
//
// 参数：
//   - entry / fetchErr：内置抓取的结果与传输层错误，二者互斥（fetchErr 非 nil 时 entry 无意义）。
//   - req：已规范化的请求，用于判定触发条件与闸门。
//
// 返回值：最终结果与错误。三种可能：
//   - 回退成功：返回 channel=tavily、status=200 的结果，错误为 nil；
//   - 回退被闸门拦下或未触发：原样返回内置结果与内置错误；
//   - 回退失败（无 key、超时、上游报错）：**仍返回内置结果与内置错误**，绝不因为
//     回退失败把一次原本成功的抓取变成失败，也不改变原本的 502。
//
// 副作用：调用 Fallback.Fetch（可能发起真实网络请求并消耗第三方额度）。
func (f *Fetcher) applyFallback(ctx context.Context, req Request, entry CacheEntry, fetchErr error) (CacheEntry, error) {
	if f.fallback == nil {
		return entry, fetchErr
	}
	if reason := fallbackBlocked(req, fetchErr); reason != gateNone {
		return entry, fetchErr
	}
	if fetchErr == nil && !shouldFallback(entry, nil, f.fallbackMinChars) {
		return entry, fetchErr
	}
	content, _, err := f.fallback.Fetch(ctx, req.URL.String())
	if err != nil {
		// 回退是加分项而非必要条件：失败时按内置结果返回，让调用方看到真实的上游行为。
		return entry, fetchErr
	}
	return fallbackEntry(req, entry, fetchErr, content), nil
}

// fallbackEntry 把回退通道的正文组装为最终缓存条目。
//
// 关键约定：回退成功时 StatusCode 记 200 而不是内置抓取的状态码。
// 因为 Result.Text() 对非 2xx 会在正文前补一行 HTTP <状态码>，若回退成功仍报 403，
// 模型会在真内容最前面看到一行假的 403。原始状态码不丢：它留在缓存条目的
// StatusCode 上（决定该条用哪种 TTL），而「这次走了回退」由 Channel 标明。
//
// 参数 direct 是内置抓取的条目或错误（有错误时条目为空）；content 为回退正文。
// 返回值可直接写入缓存并据此切片。本函数为纯函数，无副作用。
func fallbackEntry(req Request, direct CacheEntry, directErr error, content string) CacheEntry {
	entry := CacheEntry{
		URL:         req.URL.String(),
		Method:      req.Method,
		ContentType: "text/markdown",
		Channel:     ChannelTavily,
		Content:     content,
	}
	if directErr == nil {
		// 保留内置抓取的状态码：缓存 TTL 按「内容型门禁结果」判定，与通道无关。
		entry.StatusCode = direct.StatusCode
	}
	return entry
}
