-- 新增 DeepSeek 兼容接口开关（POST /v1/tools/search 与 /v1/tools/fetch）。
-- 只在键缺失时补 true，不覆盖用户已保存的值，升级后行为与其它兼容接口一致。
-- 注意：本文件不得在注释中出现 ASCII 分号，也不得包含美元引用块，语句必须永久幂等。
UPDATE settings
SET value = jsonb_set(value, '{compat_deepseek_enabled}', 'true'::jsonb, true),
    updated_at = now()
WHERE key = 'runtime'
  AND NOT (value ? 'compat_deepseek_enabled');
