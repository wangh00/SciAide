package tool

import "encoding/json"

// ResearchMaterialDefinition is the exact host-owned, scoped persistence
// capability permitted in AgentStage. It cannot choose paths or foreign tasks.
func ResearchMaterialDefinition() Definition {
	return Definition{QualifiedName: "builtin.research.full_text.read", Version: "4", Risk: RiskModerate, Idempotent: true, Description: "Verify a concrete missing result in selected task literature. Skip materials with fullTextAvailability=unavailable. status=unavailable means verification did not complete: preserve evidence limitations and do not retry. Successful downloads are saved as task-owned materials while old attachments remain intact. The host refreshes changed evidence before accepting synthesis. Results contain bounded PDF units, not new citation markers or proof of complete reading.", Permissions: append([]PermissionRequirement{{Kind: PermissionWorkspaceRead, Resource: "."}, {Kind: PermissionWorkspaceWrite, Resource: "research:selected-task-material"}}, researchFullTextNetworkPermissions...), InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["candidateId","query"],"properties":{"candidateId":{"type":"string","minLength":1,"maxLength":128},"query":{"type":"string","minLength":3,"maxLength":300}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
}

func IsResearchMaterialDefinition(value Definition) bool {
	return DefinitionFingerprint(value) == DefinitionFingerprint(ResearchMaterialDefinition())
}

var researchFullTextNetworkPermissions = []PermissionRequirement{
	{Kind: PermissionNetworkDomain, Resource: "idp.nature.com:443"},
	{Kind: PermissionNetworkDomain, Resource: "arxiv.org:443"},
	{Kind: PermissionNetworkDomain, Resource: "export.arxiv.org:443"},
	{Kind: PermissionNetworkDomain, Resource: "europepmc.org:443"},
	{Kind: PermissionNetworkDomain, Resource: "www.ebi.ac.uk:443"},
	{Kind: PermissionNetworkDomain, Resource: "pmc.ncbi.nlm.nih.gov:443"},
	{Kind: PermissionNetworkDomain, Resource: "www.ncbi.nlm.nih.gov:443"},
	{Kind: PermissionNetworkDomain, Resource: "www.semanticscholar.org:443"},
	{Kind: PermissionNetworkDomain, Resource: "semanticscholar.org:443"},
	{Kind: PermissionNetworkDomain, Resource: "www.nature.com:443"},
	{Kind: PermissionNetworkDomain, Resource: "jamanetwork.com:443"},
}

func ResearchMaterialNetworkPermissions() []PermissionRequirement {
	return append([]PermissionRequirement(nil), researchFullTextNetworkPermissions...)
}
