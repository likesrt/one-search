-- 新增 Keenable 搜索渠道（带 key 的 POST /v1/search，鉴权头 X-API-Key）。
-- 未公开按次单价，因此不写入按次费率，仅在渠道 settings 里保留常规键。
-- 注意：本文件不得在注释中出现分号，也不得包含 $$ 块，语句必须永久幂等。
INSERT INTO providers (name, display_name, base_url, priority, weight, timeout_ms, default_cache_enabled, cache_ttl_seconds, settings)
VALUES ('keenable', 'Keenable', 'https://api.keenable.ai', 80, 1, 15000, FALSE, 3600, '{"key_retry_count":3,"max_concurrency":0}'::jsonb)
ON CONFLICT (name) DO NOTHING;

-- 把 keenable 并入默认渠道列表，但只在用户尚未改动过默认列表时覆写：
-- 守卫快照串是上一轮的 7 元数组，一旦用户自定义过 default_providers 就不再动它。
UPDATE settings
SET value = jsonb_set(value, '{default_providers}', '["exa","you","jina","tavily","firecrawl","serper","brave","keenable"]'::jsonb, true),
    updated_at = now()
WHERE key = 'runtime'
  AND COALESCE(value->'default_providers', '[]'::jsonb) = '["exa","you","jina","tavily","firecrawl","serper","brave"]'::jsonb;
