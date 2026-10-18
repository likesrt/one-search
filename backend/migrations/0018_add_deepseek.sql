-- 新增 DeepSeek 官方网页搜索渠道（POST /v1/tools/search，Authorization: Bearer）。
-- 只接入 Search 接口，不接入 Fetch 接口（/v1/tools/fetch 未使用）。
-- 注意：本文件不得在注释中出现 ASCII 分号，也不得包含美元引用块，语句必须永久幂等。
INSERT INTO providers (name, display_name, base_url, priority, weight, timeout_ms, default_cache_enabled, cache_ttl_seconds, settings)
VALUES ('deepseek', 'DeepSeek', 'https://api.deepseek.com', 100, 1, 15000, FALSE, 3600, '{"key_retry_count":3,"max_concurrency":0}'::jsonb)
ON CONFLICT (name) DO NOTHING;

-- 只在用户从未自定义默认列表时追加 deepseek。
-- 守卫快照是上一版 8 元数组（0016 写入），自定义过的实例保持原样。
UPDATE settings
SET value = jsonb_set(value, '{default_providers}', '["exa","you","jina","tavily","firecrawl","serper","brave","keenable","deepseek"]'::jsonb, true),
    updated_at = now()
WHERE key = 'runtime'
  AND COALESCE(value->'default_providers', '[]'::jsonb) = '["exa","you","jina","tavily","firecrawl","serper","brave","keenable"]'::jsonb;
