-- 新增 Context7 文档渠道（GET /v3/search，鉴权头 Authorization: Bearer，key 可选）。
-- 它检索 GitHub 仓库的结构化文档与代码片段，用于「查库/框架用法」场景。
-- 刻意不改 settings.default_providers：存量用户的默认搜索行为保持不变，
-- context7 只在显式 providers:["context7"] 时参与（MCP enum 已由 model.DefaultProviders 列出）。
-- 注意：本文件不得在注释中出现分号，也不得包含美元引用块，语句必须永久幂等。
INSERT INTO providers (name, display_name, base_url, priority, weight, timeout_ms, default_cache_enabled, cache_ttl_seconds, settings)
VALUES ('context7', 'Context7', 'https://context7.com/api', 90, 1, 30000, FALSE, 3600, '{"key_retry_count":3,"max_concurrency":0}'::jsonb)
ON CONFLICT (name) DO NOTHING;
