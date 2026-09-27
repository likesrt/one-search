package security

import "testing"

// TestMaskSecret 覆盖密钥脱敏展示的三种取值：
// 空值（含纯空白）返回空串 —— 这是「空密钥即匿名调用」在展示层的判据，返回 "****" 会让
// 匿名条目与「已配置但看不清」无法区分；短值（去空白后 <= 8）返回 "****"；
// 正常值保留首尾各 4 个字符。所有分支都不修改入参、不产生副作用。
func TestMaskSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "空串返回空串", secret: "", want: ""},
		{name: "纯空白返回空串", secret: "   ", want: ""},
		{name: "制表符与换行也视为空", secret: "\t\n ", want: ""},
		{name: "短值返回固定掩码", secret: "abc", want: "****"},
		{name: "长度 8 返回固定掩码", secret: "12345678", want: "****"},
		{name: "长度 9 保留首尾各 4 位", secret: "123456789", want: "1234****6789"},
		{name: "正常值保留首尾各 4 位", secret: "ctx7sk-abcdefgh-1234", want: "ctx7****1234"},
		{name: "两侧空白先被裁掉", secret: "  ctx7sk-abcdefgh-1234  ", want: "ctx7****1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskSecret(tc.secret); got != tc.want {
				t.Fatalf("MaskSecret(%q) = %q, want %q", tc.secret, got, tc.want)
			}
		})
	}
}
