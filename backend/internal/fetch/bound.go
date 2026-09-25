package fetch

import (
	"fmt"
	"net/http"
)

// noMoreContent 是 start_index 已超出内容长度时的提示文案。
// 与源工具保持逐字一致：模型可能已按该文案形成「已读完」的判断。
const noMoreContent = "No more content available."

// boundOutput 是截断后的输出与其元信息。
type boundOutput struct {
	// Content 是截断后的内容，被截断时末尾附有续读提示
	Content string
	// Truncated 表示内容是否因超出 MaxLength 而被截断
	Truncated bool
	// NextIndex 是续读起点；未截断时等于 Total
	NextIndex int
	// Total 是完整内容的字符数（以 rune 计）
	Total int
}

// boundText 按 req.MaxLength 截断文本，并在仍有剩余时追加续读提示。
//
// 索引以 rune 计，因此不会把多字节字符切成两半 —— 按字节截断会产生无效 UTF-8，
// 模型读到的是乱码，且续读时无法还原。
//
// 提示文本区分两种情况：GET 可带 start_index 续读；POST 不能续读，
// 只能提示提高 max_length，因为重放请求可能造成副作用。
//
// 边界条件：req.StartIndex 大于等于内容长度时返回 noMoreContent 且 Truncated 为 false
// （已无内容可读，不算截断）。本函数不修改入参。
func boundText(text string, req Request) boundOutput {
	runes := []rune(text)
	total := len(runes)
	if req.StartIndex >= total {
		return boundOutput{Content: noMoreContent, NextIndex: total, Total: total}
	}
	start := req.StartIndex
	end := start + req.MaxLength
	if end > total {
		end = total
	}
	content := string(runes[start:end])
	if end >= total {
		return boundOutput{Content: content, NextIndex: total, Total: total}
	}
	return boundOutput{
		Content:   content + "\n\n" + truncationNotice(req, start, end, total),
		Truncated: true,
		NextIndex: end,
		Total:     total,
	}
}

// truncationNotice 生成续读提示。
//
// 单独拆出是为了让截断逻辑与文案构造各自保持单一职责。
// 提示中的起始位置用 0 基、结束位置用 end-1，与源工具一致，
// 便于模型按「已展示字符范围」判断还剩多少内容。
func truncationNotice(req Request, start, end, total int) string {
	shown := fmt.Sprintf("[Content truncated: showing characters %d-%d of %d.",
		start, end-1, total)
	if req.Method != http.MethodGet {
		return fmt.Sprintf("%s Raise max_length to see more; a %s cannot be continued with start_index.]",
			shown, req.Method)
	}
	return fmt.Sprintf("%s Call %s with start_index=%d to continue.]",
		shown, ToolName, end)
}
