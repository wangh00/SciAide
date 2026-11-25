package tool

import "strings"

// Presentation only, never an execution allowlist. These auxiliary families
// can be discovered through the existing host search when their full schemas
// would prevent first admission. Unknown/core/resource tools stay direct.
func OptionalBuiltinDefinition(name string) bool {
	for _, prefix := range []string{"builtin.attachment.", "builtin.document.", "builtin.knowledge.", "builtin.python.", "builtin.skill.", "builtin.workspace.", "builtin.research.workflow."} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "builtin.shell.execute", "builtin.research.catalog", "builtin.research.search", "builtin.research.fetch", "builtin.research.full_text.read":
		return true
	}
	return false
}
