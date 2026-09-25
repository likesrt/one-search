-- Key 级代理（三态）：inherit 回退渠道级、direct 强制直连、custom 用该 key 自己的地址。
-- proxy_mode 用 TEXT + CHECK 而非布尔开关，是为了区分「未配置」与「显式直连」。
-- 注意：migrations.go 的 splitSQLStatements 按分号朴素切分，且没有 schema_migrations 表（每次启动重跑），
-- 因此本文件不得在注释中出现分号，也不得包含 $$ 块，语句必须永久幂等。
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS proxy_mode TEXT NOT NULL DEFAULT 'inherit';
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS proxy_url TEXT NOT NULL DEFAULT '';
-- 先删后建，保证重复执行也不会因约束已存在而报错。
ALTER TABLE provider_keys DROP CONSTRAINT IF EXISTS provider_keys_proxy_mode_check;
ALTER TABLE provider_keys ADD CONSTRAINT provider_keys_proxy_mode_check CHECK (proxy_mode IN ('inherit','direct','custom'));
