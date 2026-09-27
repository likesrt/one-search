package fetch

import (
	"context"
)

// defaultMaxConcurrency 是 max_concurrency 缺省（非正）时的上游并发上限。
//
// 取 32 是保守值：单请求最坏占用「超时上限 × 10MiB 响应体」，32 路同时在飞时
// 内存峰值仍在容器可承受范围内；再高则需要同时调大 transport 的 MaxConnsPerHost。
const defaultMaxConcurrency = 32

// gate 是「打向上游的抓取」的全局并发闸门，用带缓冲 channel 当信号量。
//
// 只护住上游 IO 而不护住整个 Fetch 是有意的：缓存命中与单飞等待者不产生上游流量，
// 让它们占名额等于把并发限制变成对请求数的限制，与「别把目标站点打成限流」的初衷不符。
//
// gate 可被多个 goroutine 并发使用；零值不可用，必须经 newGate 构造。
type gate struct {
	slots chan struct{}
}

// newGate 构造容量为 limit 的并发闸门。
//
// 参数 limit <= 0 时取 defaultMaxConcurrency（配置层允许把 0 解释为「用默认值」）。
// 返回值恒非 nil，可安全并发使用。
func newGate(limit int) *gate {
	if limit <= 0 {
		limit = defaultMaxConcurrency
	}
	return &gate{slots: make(chan struct{}, limit)}
}

// acquire 占用一个并发名额，名额耗尽时排队等待。
//
// 参数 ctx 必须带超时或可被取消：本函数不为排队设置上限，只依赖 ctx 退出，
// 否则上游变慢时请求会无限堆积。返回值非 nil 表示未拿到名额（ctx 已结束），
// 此时调用方不得调用 release。
func (g *gate) acquire(ctx context.Context) error {
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release 归还一个并发名额，必须与成功的 acquire 一一配对。
// 无参数、无返回值；多调用会阻塞，因此只应放在 defer 中。
func (g *gate) release() {
	<-g.slots
}

// detachContext 派生一个只保留原 deadline、切断取消传播的 context。
//
// 单飞组里的上游抓取由第一个调用方触发的 goroutine 执行。若直接沿用它的 ctx，
// 该调用方断开（客户端取消）会连带让所有等待者一起拿到 context.Canceled。
// 保留 deadline 使总耗时仍有上界，因此不会出现「无人取消的请求永远挂着」。
//
// 参数 ctx 无 deadline 时退化为可取消的 Background，由调用方负责 cancel。
// 返回值：新 context 与其 cancel 函数，调用方必须 defer cancel 以免泄漏。
func detachContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.WithCancel(context.Background())
}
