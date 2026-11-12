package tool

import (
	"encoding/json"
	"github.com/wangh00/SciAide/internal/researchsources"
)

// ResearchMaterialDefinition is the exact host-owned, scoped persistence
// capability permitted in AgentStage. It cannot choose paths or foreign tasks.
func ResearchMaterialDefinition() Definition {
	return Definition{QualifiedName: "builtin.research.full_text.read", Version: "5", Risk: RiskModerate, Idempotent: true, Description: "Verify a concrete missing result in selected task literature. Skip materials with fullTextAvailability=unavailable. status=unavailable means verification did not complete: preserve evidence limitations and do not retry. Successful downloads are saved as task-owned materials while old attachments remain intact. The host refreshes changed evidence before accepting synthesis. Results contain bounded PDF units, not new citation markers or proof of complete reading.", Permissions: append([]PermissionRequirement{{Kind: PermissionWorkspaceRead, Resource: "."}, {Kind: PermissionWorkspaceWrite, Resource: "research:selected-task-material"}}, researchFullTextNetworkPermissions...), InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["candidateId","query"],"properties":{"candidateId":{"type":"string","minLength":1,"maxLength":128},"query":{"type":"string","minLength":3,"maxLength":300}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
}

func IsResearchMaterialDefinition(value Definition) bool {
	return DefinitionFingerprint(value) == DefinitionFingerprint(ResearchMaterialDefinition())
}

var researchFullTextNetworkPermissions = func() []PermissionRequirement {
	// Preserve the existing publisher authentication redirect authority.
	result := []PermissionRequirement{{Kind: PermissionNetworkDomain, Resource: "idp.nature.com:443"}}
	seen := map[string]bool{"idp.nature.com": true}
	for _, source := range researchsources.Definitions() {
		for _, host := range source.FullTextHosts {
			if !seen[host] {
				result = append(result, PermissionRequirement{Kind: PermissionNetworkDomain, Resource: host + ":443"})
				seen[host] = true
			}
		}
	}
	return result
}()

func ResearchMaterialNetworkPermissions() []PermissionRequirement {
	return append([]PermissionRequirement(nil), researchFullTextNetworkPermissions...)
}
