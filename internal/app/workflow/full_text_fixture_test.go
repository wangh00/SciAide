package workflow

import (
	"encoding/json"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func fullTextReadFixtureDefinition() tool.Definition {
	return tool.Definition{QualifiedName: "builtin.research.full_text.read", Version: "1", Risk: tool.RiskModerate, Idempotent: true,
		Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}, {Kind: tool.PermissionNetworkDomain, Resource: "europepmc.org"}},
		InputSchema: json.RawMessage(`{"type":"object","required":["candidateId","query"],"properties":{"candidateId":{"type":"string"},"query":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
}
