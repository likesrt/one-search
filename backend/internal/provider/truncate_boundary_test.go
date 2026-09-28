package provider

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsUTF8Boundary(t *testing.T) {
	cn := strings.Repeat("中", 400) // 1200 字节
	for _, max := range []int{1, 2, 3, 4, 999, 1000, 1001, 1199} {
		got := truncate(cn, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d 产出非法 UTF-8: %q", max, got)
		}
		if len(got) > max {
			t.Fatalf("max=%d 超长: %d", max, len(got))
		}
		if len(got) < max-3 {
			t.Fatalf("max=%d 回退过多: %d", max, len(got))
		}
		if strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("max=%d 含替换字符: %q", max, got)
		}
	}
	if got := truncate("short", 100); got != "short" {
		t.Fatalf("未超限不应改动: %q", got)
	}
	if got := truncate("任意", 0); got != "任意" {
		t.Fatalf("max<=0 应原样返回: %q", got)
	}
}
