package agent

import "github.com/wangh00/SciAide/internal/app/tool"

func isWebBrowsingTool(name string) bool {
	return name == "builtin.web.search" || name == "builtin.web.open" || name == "builtin.browser.open"
}

func filterWebTools(definitions []tool.Definition, disabled bool) []tool.Definition {
	if !disabled {
		return definitions
	}
	filtered := make([]tool.Definition, 0, len(definitions))
	for _, d := range definitions {
		if !isWebBrowsingTool(d.QualifiedName) {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

const optionalWebGuidance = `Tools are optional means, not mandatory steps. Answer ordinary explanations and familiar coding examples directly when your knowledge suffices. Use web search when current information, a requested source, a version-sensitive detail, or a material uncertainty makes it useful. Search being enabled is permission, not an instruction to search. Keep lookups proportional to the question. Do not search the public web for the user's local configuration or installed tools. Do not browse Skill catalogs for ordinary questions unless a specialized procedure actually needs them. Skills are instruction documents, not MCP servers or an inventory of installed tools. For MCP configuration and connection status use builtin.mcp.list; for available tool discovery use builtin.tools.search. Never substitute Skill categories or directory scans for MCP discovery. These read-only tools do not connect servers or grant execution permission. If browsing fails, answer the parts you can reliably answer and disclose what could not be verified; do not repeatedly switch websites for the same network failure.`
