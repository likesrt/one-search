-- 为软删除新增 deleted 状态：删除密钥时保留密钥行（供历史调用日志继续引用），仅清除其用量记录。
-- 注意：本文件不得在注释中出现分号，也不得包含 $$ 块，语句必须永久幂等。
ALTER TABLE provider_keys DROP CONSTRAINT IF EXISTS provider_keys_status_check;
ALTER TABLE provider_keys ADD CONSTRAINT provider_keys_status_check CHECK (status IN ('enabled','disabled','cooling','exhausted','deleted'));

-- 别名唯一性改为只约束未删除的密钥：软删除后别名仍留在表里，
-- 若沿用原 UNIQUE(provider_id, alias)，用户删掉"大号"后无法再建同名密钥。
-- 用部分唯一索引替代表级 UNIQUE 约束来表达这个语义。
ALTER TABLE provider_keys DROP CONSTRAINT IF EXISTS provider_keys_provider_id_alias_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_keys_provider_alias_active ON provider_keys (provider_id, alias) WHERE status <> 'deleted';
