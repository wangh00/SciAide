package mcpserver

const DefaultExaID = "builtin-exa-search"
const DefaultExaURL = "https://mcp.exa.ai/mcp?tools=web_search_exa"
const DefaultExaTool = "mcp.exa.web_search_exa"

// Host-reviewed search-only configuration. Never trust readOnlyHint from an
// arbitrary MCP server to lower tool permissions or admit it to research stages.
func IsDefaultExaSearch(s Server) bool {
	return s.ID == DefaultExaID && s.Namespace == "exa" && s.Transport == TransportStreamableHTTP && s.URL == DefaultExaURL && len(s.Headers) == 0
}
