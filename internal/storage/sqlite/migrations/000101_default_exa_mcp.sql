-- Applied once: deletion/disable/edit remains user-owned on later startups.
-- Do not overwrite a user namespace or duplicate a hosted Exa connection.
INSERT INTO mcp_servers (
    id,name,namespace,transport,url,enabled,auto_start,trust,timeout_seconds,status,created_at,updated_at
)
SELECT 'builtin-exa-search','Exa 全网搜索（免 Key）','exa','streamable_http',
       'https://mcp.exa.ai/mcp?tools=web_search_exa',1,1,'user_trusted',30,'disconnected',
       strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE NOT EXISTS (
    SELECT 1 FROM mcp_servers WHERE id='builtin-exa-search' OR namespace='exa'
    OR lower(rtrim(url,'/'))='https://mcp.exa.ai/mcp'
    OR lower(url) LIKE 'https://mcp.exa.ai/mcp?%'
);
