CREATE TABLE mcp_servers (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 100),
    namespace TEXT NOT NULL UNIQUE CHECK (length(namespace) BETWEEN 1 AND 32),
    transport TEXT NOT NULL CHECK (transport IN ('stdio', 'streamable_http')),
    command TEXT NOT NULL DEFAULT '',
    args_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(args_json) AND json_type(args_json) = 'array'),
    working_dir TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    headers_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(headers_json) AND json_type(headers_json) = 'object'),
    env_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(env_json) AND json_type(env_json) = 'object'),
    secret_env_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(secret_env_json) AND json_type(secret_env_json) = 'object'),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    auto_start INTEGER NOT NULL DEFAULT 0 CHECK (auto_start IN (0, 1)),
    trust TEXT NOT NULL DEFAULT 'untrusted' CHECK (trust IN ('untrusted', 'user_trusted')),
    timeout_seconds INTEGER NOT NULL DEFAULT 30 CHECK (timeout_seconds BETWEEN 5 AND 300),
    status TEXT NOT NULL DEFAULT 'disconnected' CHECK (status IN ('disabled','disconnected','starting','initializing','ready','degraded','failed','stopping')),
    protocol_version TEXT NOT NULL DEFAULT '',
    server_version TEXT NOT NULL DEFAULT '',
    tool_count INTEGER NOT NULL DEFAULT 0 CHECK (tool_count >= 0),
    resource_count INTEGER NOT NULL DEFAULT 0 CHECK (resource_count >= 0),
    prompt_count INTEGER NOT NULL DEFAULT 0 CHECK (prompt_count >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    last_connected_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (transport = 'stdio' AND length(trim(command)) > 0 AND url = '')
        OR (transport = 'streamable_http' AND command = '' AND working_dir = '' AND url <> '')
    )
);

CREATE INDEX idx_mcp_servers_status ON mcp_servers(enabled, status, name);
