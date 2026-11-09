
CREATE TABLE IF NOT EXISTS meta (
  k TEXT PRIMARY KEY, v TEXT
);
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL DEFAULT '',
  family TEXT NOT NULL,
  customer_id TEXT,
  group_id TEXT,
  status TEXT NOT NULL DEFAULT 'unverified',
  enc_tokens TEXT NOT NULL, -- 加密令牌 JSON
  device_mid TEXT NOT NULL,
  app_version TEXT NOT NULL,
  card_started_at INTEGER,
  card_expires_at INTEGER,
  web_cookie_expires_at INTEGER,
  business_token_expires_at INTEGER,
  zcode_jwt_last_ok_at INTEGER,
  fail_streak INTEGER NOT NULL DEFAULT 0,
  cooldown_until INTEGER,
  capabilities TEXT NOT NULL DEFAULT '[]', -- JSON 模型列表
  last_balance TEXT, -- 上游余额 JSON
  last_error TEXT,
  proxy_url TEXT, -- NULL 跟随全局代理
  source TEXT NOT NULL DEFAULT 'local',
  created_at INTEGER NOT NULL,
  last_ok_at INTEGER,
  last_used_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_accounts_group_created ON accounts(group_id,created_at);
CREATE TABLE IF NOT EXISTS quota_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  bucket_id TEXT,
  entitlement_id TEXT,
  meter TEXT,
  total_units INTEGER,
  used_units INTEGER,
  remaining_units INTEGER,
  expires_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_snap_acc_ts ON quota_snapshots(account_id, ts);
CREATE INDEX IF NOT EXISTS idx_snap_ts ON quota_snapshots(ts);
CREATE TABLE IF NOT EXISTS usage_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  key_id TEXT,
  client_ip TEXT,
  account_id TEXT,
  family TEXT,
  model TEXT,
  reasoning_level TEXT,
  stream INTEGER,
  attempt_count INTEGER DEFAULT 1,
  rotated_accounts TEXT,
  status TEXT NOT NULL,
  error_code TEXT,
  error_message TEXT,
  ttft_ms INTEGER,
  total_ms INTEGER,
  tokens_in INTEGER, tokens_out INTEGER, tokens_reasoning INTEGER,
  cache_read INTEGER, cache_write INTEGER,
  prompt_hash TEXT
);
CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage_log(ts);
CREATE INDEX IF NOT EXISTS idx_usage_key ON usage_log(key_id, ts);
CREATE INDEX IF NOT EXISTS idx_usage_status_ts ON usage_log(status, ts);
CREATE INDEX IF NOT EXISTS idx_usage_error_ts ON usage_log(error_code, ts);
CREATE INDEX IF NOT EXISTS idx_usage_account_ts ON usage_log(account_id, ts);
CREATE INDEX IF NOT EXISTS idx_usage_model_ts ON usage_log(model, ts);
CREATE TABLE IF NOT EXISTS schedule_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  request_id TEXT,
  chain TEXT, -- JSON 分组 ID 列表
  candidates TEXT, -- JSON 调度候选
  policy TEXT,
  picked TEXT
);
CREATE INDEX IF NOT EXISTS idx_schedule_ts ON schedule_log(ts);
CREATE INDEX IF NOT EXISTS idx_schedule_picked_ts ON schedule_log(picked, ts);
CREATE TABLE IF NOT EXISTS claim_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  plan_id TEXT,
  ok INTEGER NOT NULL,
  code TEXT,
  message TEXT,
  user_plan_id TEXT,
  ends_at INTEGER,
  trigger TEXT DEFAULT 'manual'
);
CREATE TABLE IF NOT EXISTS keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  key_tail TEXT NOT NULL,
  quota_limit INTEGER, -- Token 上限，NULL 不限
  used_units INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER,
  ip_whitelist TEXT NOT NULL DEFAULT '[]',
  model_whitelist TEXT NOT NULL DEFAULT '[]',
  group_order TEXT, -- JSON 分组链，NULL 使用默认链
  sticky_minutes INTEGER NOT NULL DEFAULT 0,
  default_level TEXT, -- NULL 跟随全局思考档位
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER
);
CREATE TABLE IF NOT EXISTS groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'primary',
  family_filter TEXT,
  policy TEXT NOT NULL DEFAULT 'balanced',
  urgent_window_min INTEGER NOT NULL DEFAULT 180,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY, v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  actor TEXT, action TEXT, detail TEXT, ip TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
CREATE INDEX IF NOT EXISTS idx_audit_action_ts ON audit_log(action, ts);
CREATE TABLE IF NOT EXISTS oauth_flows (
  flow_id TEXT PRIMARY KEY,
  account_rowid TEXT,
  provider TEXT NOT NULL,
  poll_token_enc TEXT NOT NULL,
  authorize_url TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  error TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER,
  account_id TEXT,
  updated_at INTEGER, -- 状态变更时间
  noticed_at INTEGER -- 结果确认时间，NULL 待提醒
);
CREATE TABLE IF NOT EXISTS proxy_state (
  identity TEXT PRIMARY KEY,
  blocked_until INTEGER, -- WAF 拦截冷却截止（epoch ms）
  retry_at INTEGER -- 连接失败冷却截止（epoch ms）
);
