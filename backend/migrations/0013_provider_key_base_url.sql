-- Key 级基础 URL 覆盖：留空表示回退到渠道（provider）的 base_url。
-- 注意：migrations.go 的 splitSQLStatements 按分号朴素切分，且没有 schema_migrations 表（每次启动重跑），
-- 因此本文件不得在注释中出现分号，也不得包含 $$ 块，语句必须永久幂等。
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS base_url TEXT NOT NULL DEFAULT '';
