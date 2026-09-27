package db

import (
	"testing"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
)

// TestNextKeyStatus 覆盖 RecordKeyResult 的状态决策（已抽成纯函数，无需数据库）。
//
// 两组断言：
//  1. 非空 key 的既有状态机语义必须逐字不变 —— success 回 enabled，
//     auth → disabled、quota_exhausted → exhausted、rate_limited → cooling（+15 分钟），
//     其余错误保持 enabled 且不设冷却。
//  2. 匿名 key（Value 为空串）**不做任何状态流转**：status 原样保留、cooldown 恒为 nil，
//     否则 exa 这类不支持匿名的渠道一旦创建空 key 就会被 auth 错误自动停用。
//     注意 success 分支不特判匿名：成功回 enabled 是既有的自愈语义，对匿名 key 同样成立。
func TestNextKeyStatus(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		key          model.APIKey
		success      bool
		errorType    string
		wantStatus   string
		wantCooldown bool
	}{
		{name: "非空 key 成功回到 enabled", key: model.APIKey{Value: "real-key", Status: "cooling"}, success: true, wantStatus: "enabled"},
		{name: "非空 key auth 停用", key: model.APIKey{Value: "real-key", Status: "enabled"}, errorType: "auth", wantStatus: "disabled"},
		{name: "非空 key 额度耗尽", key: model.APIKey{Value: "real-key", Status: "enabled"}, errorType: "quota_exhausted", wantStatus: "exhausted"},
		{name: "非空 key 限流进入冷却", key: model.APIKey{Value: "real-key", Status: "enabled"}, errorType: "rate_limited", wantStatus: "cooling", wantCooldown: true},
		{name: "非空 key 其它错误保持 enabled", key: model.APIKey{Value: "real-key", Status: "enabled"}, errorType: "timeout", wantStatus: "enabled"},
		{name: "非空 key 其它错误不改动原状态", key: model.APIKey{Value: "real-key", Status: "exhausted"}, errorType: "upstream", wantStatus: "enabled"},
		{name: "匿名 key 遇 auth 不改状态", key: model.APIKey{Status: "enabled"}, errorType: "auth", wantStatus: "enabled"},
		{name: "匿名 key 遇额度耗尽不改状态", key: model.APIKey{Status: "enabled"}, errorType: "quota_exhausted", wantStatus: "enabled"},
		{name: "匿名 key 遇限流不改状态也不设冷却", key: model.APIKey{Status: "enabled"}, errorType: "rate_limited", wantStatus: "enabled"},
		{name: "匿名 key 保留非 enabled 的原状态", key: model.APIKey{Status: "cooling"}, errorType: "rate_limited", wantStatus: "cooling"},
		{name: "纯空白密钥同样视为匿名", key: model.APIKey{Value: "   ", Status: "enabled"}, errorType: "auth", wantStatus: "enabled"},
		{name: "匿名 key 成功仍回 enabled", key: model.APIKey{Status: "cooling"}, success: true, wantStatus: "enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, cooldown := nextKeyStatus(tc.key, tc.success, tc.errorType, now)
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if tc.wantCooldown {
				if cooldown == nil {
					t.Fatalf("cooldown = nil, want %v", now.Add(15*time.Minute))
				}
				if !cooldown.Equal(now.Add(15 * time.Minute)) {
					t.Fatalf("cooldown = %v, want %v", *cooldown, now.Add(15*time.Minute))
				}
				return
			}
			if cooldown != nil {
				t.Fatalf("cooldown = %v, want nil", *cooldown)
			}
		})
	}
}
