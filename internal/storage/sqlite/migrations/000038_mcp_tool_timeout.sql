-- Codex applies a distinct 30 second startup boundary and a 300 second
-- default outer boundary to tools/call. Existing SciAide imports used the
-- startup-sized value for both, so upgrade only that historical default.
UPDATE mcp_servers
SET timeout_seconds = 300
WHERE timeout_seconds = 30;
