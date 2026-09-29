package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchworkflow"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

const (
	ResearchWorkflowSearchName        = "builtin.research.workflow.search"
	ResearchWorkflowImportName        = "builtin.research.workflow.import"
	ResearchWorkflowSyncName          = "builtin.research.workflow.sync"
	ResearchWorkflowPythonName        = "builtin.research.workflow.python.ensure"
	ResearchWorkflowPreparePythonName = "builtin.research.workflow.python.prepare"
	ResearchWorkflowReportName        = researchworkflow.ReportToolName
	ResearchWorkflowReviewGateName    = researchworkflow.ReviewGateToolName
)

type ResearchWorkflowSearch struct{ service *researchworkflow.Service }
type ResearchWorkflowImport struct{ service *researchworkflow.Service }
type ResearchWorkflowSync struct{ service *researchworkflow.Service }
type ResearchWorkflowPython struct{ service *researchworkflow.Service }
type ResearchWorkflowPreparePython struct{ service *researchworkflow.Service }
type ResearchWorkflowReport struct{ service *researchworkflow.Service }
type ResearchWorkflowReviewGate struct{}

func NewResearchWorkflowSearch(service *researchworkflow.Service) *ResearchWorkflowSearch {
	return &ResearchWorkflowSearch{service: service}
}
func NewResearchWorkflowImport(service *researchworkflow.Service) *ResearchWorkflowImport {
	return &ResearchWorkflowImport{service: service}
}
func NewResearchWorkflowSync(service *researchworkflow.Service) *ResearchWorkflowSync {
	return &ResearchWorkflowSync{service: service}
}
func NewResearchWorkflowPython(service *researchworkflow.Service) *ResearchWorkflowPython {
	return &ResearchWorkflowPython{service: service}
}
func NewResearchWorkflowPreparePython(service *researchworkflow.Service) *ResearchWorkflowPreparePython {
	return &ResearchWorkflowPreparePython{service: service}
}
func NewResearchWorkflowReport(service *researchworkflow.Service) *ResearchWorkflowReport {
	return &ResearchWorkflowReport{service: service}
}
func NewResearchWorkflowReviewGate() *ResearchWorkflowReviewGate {
	return &ResearchWorkflowReviewGate{}
}

func (*ResearchWorkflowReviewGate) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ResearchWorkflowReviewGateName, Version: researchworkflow.ReviewGateToolVersion, Risk: tool.RiskLow, Idempotent: true,
		Description:  "Deterministically block delivery unless an independent AI review approves the exact frozen stage input and reports no unresolved claim, citation, numeric, or method issues.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["subject","review"],"properties":{"subject":{},"review":{"type":"object","additionalProperties":false,"required":["approved","reviewedInputSha256","verifiedClaims","unsupportedClaims","citationIssues","numericIssues","methodIssues","requiredCorrections","confidence","limitations"],"properties":{"approved":{"type":"boolean"},"reviewedInputSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"verifiedClaims":{"type":"array","maxItems":100,"items":{"type":"string"}},"unsupportedClaims":{"type":"array","maxItems":100,"items":{"type":"string"}},"citationIssues":{"type":"array","maxItems":100,"items":{"type":"string"}},"numericIssues":{"type":"array","maxItems":100,"items":{"type":"string"}},"methodIssues":{"type":"array","maxItems":100,"items":{"type":"string"}},"requiredCorrections":{"type":"array","maxItems":100,"items":{"type":"string"}},"confidence":{"type":"string","enum":["low","medium","high"]},"limitations":{"type":"array","maxItems":100,"items":{"type":"string"}}}}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["approved","reviewedInputSha256","reviewSha256","confidence","verifiedClaimCount","limitations"],"properties":{"approved":{"type":"boolean","const":true},"reviewedInputSha256":{"type":"string"},"reviewSha256":{"type":"string"},"confidence":{"type":"string","enum":["low","medium","high"]},"verifiedClaimCount":{"type":"integer","minimum":0},"limitations":{"type":"array","items":{"type":"string"}}}}`),
	}, nil
}

func (*ResearchWorkflowReviewGate) Invoke(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		Subject json.RawMessage `json:"subject"`
		Review  struct {
			Approved            bool     `json:"approved"`
			ReviewedInputSHA256 string   `json:"reviewedInputSha256"`
			VerifiedClaims      []string `json:"verifiedClaims"`
			UnsupportedClaims   []string `json:"unsupportedClaims"`
			CitationIssues      []string `json:"citationIssues"`
			NumericIssues       []string `json:"numericIssues"`
			MethodIssues        []string `json:"methodIssues"`
			RequiredCorrections []string `json:"requiredCorrections"`
			Confidence          string   `json:"confidence"`
			Limitations         []string `json:"limitations"`
		} `json:"review"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if len(args.Subject) == 0 || !json.Valid(args.Subject) {
		return tool.Result{}, tool.NewUserFacingError("独立审查的冻结输入无效；请重新执行审查阶段")
	}
	// New Workflow runs provide the complete stage input that the independent
	// review actually saw. Hash that exact frozen JSON. The envelope fallback
	// preserves direct callers created before this field existed.
	snapshot := args.Subject
	var subjectObject map[string]json.RawMessage
	if json.Unmarshal(args.Subject, &subjectObject) != nil || subjectObject == nil {
		return tool.Result{}, tool.NewUserFacingError("independent review snapshot is invalid")
	}
	if _, hasContext := subjectObject["context"]; !hasContext {
		snapshot, _ = json.Marshal(map[string]json.RawMessage{"context": args.Subject})
	}
	if len(snapshot) > 0 && subjectObject["context"] == nil {
		var object map[string]json.RawMessage
		if !json.Valid(snapshot) || json.Unmarshal(snapshot, &object) != nil || object == nil {
			return tool.Result{}, tool.NewUserFacingError("独立审查的冻结输入快照无效，已阻止交付；请重新执行审查阶段")
		}
		contextSnapshot, ok := object["context"]
		if !ok || !jsonSnapshotsEqual(contextSnapshot, args.Subject) {
			return tool.Result{}, tool.NewUserFacingError("独立审查的冻结结果与当前交付结果不一致，已阻止交付；请重新执行审查阶段")
		}
	} else if subjectObject["context"] == nil {
		snapshot, _ = json.Marshal(map[string]json.RawMessage{"context": args.Subject})
	}
	digest := sha256.Sum256(snapshot)
	expected := hex.EncodeToString(digest[:])
	if args.Review.ReviewedInputSHA256 != expected {
		return tool.Result{}, tool.NewUserFacingError("独立审查没有覆盖当前冻结结果，已阻止交付；请重新执行审查阶段")
	}
	issues := len(args.Review.UnsupportedClaims) + len(args.Review.CitationIssues) + len(args.Review.NumericIssues) + len(args.Review.MethodIssues)
	corrections := len(args.Review.RequiredCorrections)
	if !args.Review.Approved || issues > 0 || corrections > 0 {
		return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("独立审查未通过：审查记录列出 %d 条分类意见，并归纳为 %d 条必须修正项；当前结果不会交付", issues, corrections))
	}
	reviewBytes, _ := json.Marshal(args.Review)
	reviewDigest := sha256.Sum256(reviewBytes)
	structured, _ := json.Marshal(map[string]any{
		"approved": true, "reviewedInputSha256": expected, "reviewSha256": hex.EncodeToString(reviewDigest[:]),
		"confidence": args.Review.Confidence, "verifiedClaimCount": len(args.Review.VerifiedClaims), "limitations": nonNilStrings(args.Review.Limitations),
	})
	return tool.Result{Status: tool.ResultSuccess, Text: "Independent review passed the deterministic delivery gate for the exact frozen result.", Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func jsonSnapshotsEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var researchWorkflowNetworkPermissions = []tool.PermissionRequirement{
	{Kind: tool.PermissionNetworkDomain, Resource: "api.openalex.org:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "api.crossref.org:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "export.arxiv.org:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "eutils.ncbi.nlm.nih.gov:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "www.ebi.ac.uk:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "api.semanticscholar.org:443"},
}

var researchFullTextNetworkPermissions = tool.ResearchMaterialNetworkPermissions()

func (*ResearchWorkflowSearch) Definition(context.Context) (tool.Definition, error) {
	d := tool.Definition{QualifiedName: ResearchWorkflowSearchName, Version: "7", Risk: tool.RiskModerate, Idempotent: true,
		Description:  "Search public scholarly databases and persist the normalized, deduplicated candidates in the current project. Results remain untrusted discovery data until selected, imported, indexed, and searched locally.",
		Permissions:  append([]tool.PermissionRequirement(nil), researchWorkflowNetworkPermissions...),
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":500},"queries":{"type":"array","maxItems":4,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":500}},"sourceIds":{"type":"array","maxItems":12,"uniqueItems":true,"items":{"type":"string","enum":["openalex","crossref","arxiv","pubmed","europepmc","semantic-scholar"]}},"offset":{"type":"integer","minimum":0,"maximum":150},"publicationYears":{"type":"object","additionalProperties":false,"required":["from","to"],"properties":{"from":{"type":"integer","minimum":0,"maximum":3000},"to":{"type":"integer","minimum":0,"maximum":3000}}},"referencesOnly":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":50}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["queryId","queryIds","queryText","queries","candidates","sources","partial","searchErrors"],"properties":{"queryId":{"type":"string"},"queryIds":{"type":"array","items":{"type":"string"}},"queryText":{"type":"string"},"queries":{"type":"array","items":{"type":"string"}},"candidates":{"type":"array","items":{"type":"object"}},"candidateIds":{"type":"array","items":{"type":"string"}},"candidateCount":{"type":"integer"},"sources":{"type":"array","items":{"type":"object"}},"partial":{"type":"boolean"},"searchErrors":{"type":"array","items":{"type":"string"}}}}`),
	}
	var schema map[string]any
	_ = json.Unmarshal(d.InputSchema, &schema)
	p := schema["properties"].(map[string]any)
	p["queries"].(map[string]any)["maxItems"] = 1
	p["offset"] = map[string]any{"type": "integer", "const": 0}
	p["limit"].(map[string]any)["maximum"] = 20
	providerProps := map[string]any{}
	for _, source := range []string{"pubmed", "europepmc", "crossref", "openalex"} {
		providerProps[source] = map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}}
	}
	p["providerQueries"] = map[string]any{"type": "object", "additionalProperties": false, "properties": providerProps}
	d.InputSchema, _ = json.Marshal(schema)
	return d, nil
}

func (t *ResearchWorkflowSearch) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		ProviderQueries map[string][]string       `json:"providerQueries"`
		Query           string                    `json:"query"`
		Years           research.PublicationYears `json:"publicationYears"`
		Queries         []string                  `json:"queries"`
		SourceIDs       []string                  `json:"sourceIds"`
		Limit           int                       `json:"limit"`
		ReferencesOnly  bool                      `json:"referencesOnly"`
		Offset          int                       `json:"offset"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	queries := make([]string, 0, 1+len(args.Queries))
	seenQueries := map[string]struct{}{}
	for _, query := range append([]string{args.Query}, args.Queries...) {
		query = strings.TrimSpace(query)
		key := strings.ToLower(query)
		if query == "" {
			continue
		}
		if _, duplicate := seenQueries[key]; duplicate {
			continue
		}
		seenQueries[key] = struct{}{}
		queries = append(queries, query)
	}
	if len(queries) == 0 || len(queries) > 2 || args.Offset != 0 || args.Limit > 20 || args.Limit < 0 {
		return tool.Result{}, fmt.Errorf("research literature search requires 1-2 unique queries, first page only, limit at most 20")
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	for _, values := range args.ProviderQueries {
		if len(values) != len(queries) {
			return tool.Result{}, fmt.Errorf("provider query count must match planned queries")
		}
	}
	queryIDs := []string{}
	allSources := []research.SourceSearch{}
	searchErrors := []string{}
	partial := false
	for index, query := range queries {
		projections := map[string]string{}
		for source, values := range args.ProviderQueries {
			projections[source] = values[index]
		}
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		value, err := t.service.SearchScopedPage(ctx, research.DiscoverySearchCommand{ProjectID: invocation.ProjectID, Query: query, ProviderQueries: projections, SourceIDs: args.SourceIDs, Limit: args.Limit, Offset: args.Offset, SnapshotKey: invocation.CallID, ResearchTaskID: invocation.ResearchTaskID, Years: args.Years})
		if err != nil {
			if ctx.Err() != nil {
				return tool.Result{}, ctx.Err()
			}
			partial = true
			searchErrors = append(searchErrors, fmt.Sprintf("%s: %v", query, err))
			continue
		}
		queryIDs = append(queryIDs, value.Query.ID)
		allSources = append(allSources, value.Query.Sources...)
		partial = partial || value.Query.Partial
	}
	if len(queryIDs) == 0 {
		return tool.Result{}, fmt.Errorf("all literature searches failed: %s", strings.Join(searchErrors, "; "))
	}
	candidates, err := t.service.LiteratureCandidates(ctx, invocation.ProjectID, queryIDs)
	if err != nil {
		return tool.Result{}, err
	}
	ids := make([]string, 0, len(candidates))
	display := []map[string]any{}
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
		if !args.ReferencesOnly {
			w := candidate.Preferred
			display = append(display, map[string]any{"id": candidate.ID, "title": w.Title, "year": w.Year, "doi": w.Identifiers.DOI, "abstract": w.Abstract})
		}
	}
	structured, _ := json.Marshal(map[string]any{"queryId": queryIDs[0], "queryIds": queryIDs, "queryText": queries[0], "queries": queries, "candidates": display, "candidateIds": ids, "candidateCount": len(ids), "sources": allSources, "partial": partial, "searchErrors": searchErrors})
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Persisted %d deduplicated candidates for paged AI screening. No candidates were discarded by a model batch limit.", len(ids)), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowImport) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowImportName, Version: "5", Risk: tool.RiskHigh, Idempotent: true,
		Description:  "Mark the exact human-selected research candidates as included, materialize open full text or disclosed metadata/abstract files, and import them as project attachments. Completed candidates are reused on retry.",
		Permissions:  append(append([]tool.PermissionRequirement{}, researchFullTextNetworkPermissions...), tool.PermissionRequirement{Kind: tool.PermissionWorkspaceWrite, Resource: "."}),
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["selectedCandidateIds"],"properties":{"selectedCandidateIds":{"type":"array","maxItems":100,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}},"selectedAttachmentIds":{"type":"array","maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}},"mode":{"type":"string","enum":["auto","full_text","metadata_abstract"]}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["materials","attachmentIds"],"properties":{"materials":{"type":"array","items":{"type":"object"}},"attachmentIds":{"type":"array","items":{"type":"string"}}}}`),
	}, nil
}

func (t *ResearchWorkflowImport) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		IDs         []string                 `json:"selectedCandidateIds"`
		Attachments []string                 `json:"selectedAttachmentIds"`
		Mode        research.MaterializeMode `json:"mode"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if len(args.IDs)+len(args.Attachments) == 0 || len(args.IDs)+len(args.Attachments) > 100 {
		return tool.Result{}, fmt.Errorf("select 1-100 materials")
	}
	values := []researchworkflow.ImportedMaterial{}
	if len(args.Attachments) > 0 {
		imported, err := t.service.ImportUserMaterials(ctx, invocation.ProjectID, invocation.ResearchTaskID, args.Attachments)
		if err != nil {
			return tool.Result{}, err
		}
		values = append(values, imported...)
	}
	if len(args.IDs) > 0 {
		imported, err := t.service.ImportSelectedForTask(ctx, invocation.ProjectID, args.IDs, args.Mode, invocation.ResearchTaskID)
		if err != nil {
			return tool.Result{}, err
		}
		values = append(values, imported...)
	}
	unique := values[:0]
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v.AttachmentID] {
			seen[v.AttachmentID] = true
			unique = append(unique, v)
		}
	}
	values = unique
	ids := make([]string, 0, len(values))
	warnings := 0
	for _, value := range values {
		ids = append(ids, value.AttachmentID)
		if value.Warning != "" {
			warnings++
		}
	}
	structured, _ := json.Marshal(map[string]any{"materials": values, "attachmentIds": ids})
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("已导入 %d 份研究材料，%d 份包含来源或证据限制说明；详见各材料 warning 和 importKind。", len(values), warnings), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowSync) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowSyncName, Version: "2", Risk: tool.RiskModerate, Idempotent: true, Description: "Drain knowledge indexing for the selected imported attachments and prove that every material is ready in the current active project index before continuing.", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}, {Kind: tool.PermissionWorkspaceWrite, Resource: "."}}, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachmentIds"],"properties":{"attachmentIds":{"type":"array","minItems":1,"maxItems":100,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}}}}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["documents","documentIds","attachmentIds"],"properties":{"documents":{"type":"array","items":{"type":"object"}},"documentIds":{"type":"array","items":{"type":"string"}},"attachmentIds":{"type":"array","items":{"type":"string"}}}}`)}, nil
}

func (t *ResearchWorkflowSync) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		IDs []string `json:"attachmentIds"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	values, err := t.service.SynchronizeForTask(ctx, invocation.ProjectID, invocation.ResearchTaskID, args.IDs)
	if err != nil {
		return tool.Result{}, err
	}
	documentIDs := make([]string, 0, len(values))
	attachmentIDs := make([]string, 0, len(values))
	for _, value := range values {
		documentIDs = append(documentIDs, value.ID)
		attachmentIDs = append(attachmentIDs, value.AttachmentID)
	}
	structured, _ := json.Marshal(map[string]any{"documents": values, "documentIds": documentIDs, "attachmentIds": attachmentIDs})
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Verified %d imported materials in the active project knowledge index.", len(values)), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowPython) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowPythonName, Version: "2", Risk: tool.RiskHigh, Idempotent: true, Description: "Verify the current project's Python environment. If it is absent or a SciAide-managed environment is broken, create/rebuild the Workspace environment. A ready user-owned external virtual environment is only verified and is never modified. This step installs no third-party package; pip installation remains a separate approval.", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute, Resource: "python-venv"}}, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"context":{}}}`), OutputSchema: pythonEnvironmentOutputSchema()}, nil
}

func (t *ResearchWorkflowPython) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	value, created, err := t.service.EnsureEnvironment(localexec.WithCallID(ctx, invocation.CallID), invocation.ProjectID)
	if err != nil {
		return tool.Result{}, err
	}
	structured, _ := json.Marshal(value)
	verb := "verified"
	if created {
		verb = "created"
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Project Python environment %s with fingerprint %s.", verb, value.EnvironmentFingerprint), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowPreparePython) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ResearchWorkflowPreparePythonName, Version: "1", Risk: tool.RiskHigh, Idempotent: false,
		Description:  "Verify the project Python environment and install only the method packages explicitly frozen by the preceding AI Schema. Empty packages only verify the environment. Package URLs, paths, indexes and arbitrary pip flags are rejected.",
		Permissions:  []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute, Resource: "python-method-environment"}},
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["packages"],"properties":{"packages":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256,"pattern":"^[A-Za-z0-9][A-Za-z0-9._-]*(?:\\[[A-Za-z0-9._,-]+\\])?(?:(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9][A-Za-z0-9._+!-]*)?$"}}}}`),
		OutputSchema: pythonEnvironmentOutputSchema(),
	}, nil
}

func (t *ResearchWorkflowPreparePython) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, changed, err := t.service.PrepareEnvironment(localexec.WithCallID(ctx, invocation.CallID), invocation.ProjectID, args.Packages)
	if err != nil {
		return tool.Result{}, err
	}
	structured, _ := json.Marshal(value)
	verb := "verified"
	if changed {
		verb = "prepared"
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Project Python method environment %s with %d locked packages; fingerprint %s.", verb, len(value.Lock), value.EnvironmentFingerprint), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowReport) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowReportName, Version: researchworkflow.ReportToolVersion, Risk: tool.RiskHigh, Idempotent: true, Description: "Publish an independently reviewed immutable Markdown report, revalidate every selected local knowledge citation, freeze upstream ToolCalls and declared analysis files, and generate GB/T 7714 DOCX and PDF exports.", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}, {Kind: tool.PermissionWorkspaceWrite, Resource: "."}}, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","citations","reportDraft","reviewGate"],"properties":{"name":{"type":"string","minLength":1,"maxLength":120},"citations":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"object"}},"reportDraft":{"type":"object","required":["markdown"],"properties":{"markdown":{"type":"string","minLength":1,"maxLength":1900000}}},"sourceArtifacts":{"type":"array","maxItems":64,"items":{"type":"object"}},"reviewGate":{"type":"object"}}}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["artifact","version","docx","pdf","created"],"properties":{"artifact":{"type":"object"},"version":{"type":"object"},"docx":{"type":"object"},"pdf":{"type":"object"},"created":{"type":"boolean"}}}`)}, nil
}

func (t *ResearchWorkflowReport) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if tool.NormalizeSubjectKind(invocation.SubjectKind) != tool.SubjectWorkflowRun {
		return tool.Result{}, fmt.Errorf("trusted research report can only run inside a Workflow Run")
	}
	var args struct {
		Name            string             `json:"name"`
		Citations       []tool.CitationRef `json:"citations"`
		ReportDraft     map[string]any     `json:"reportDraft"`
		SourceArtifacts []tool.ArtifactRef `json:"sourceArtifacts"`
		ReviewGate      json.RawMessage    `json:"reviewGate"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	markdownBody, _ := args.ReportDraft["markdown"].(string)
	analysisSnapshot, _ := json.Marshal(args.ReportDraft)
	markdown := composeWorkflowReport(markdownBody, args.Citations, args.SourceArtifacts)
	value, err := t.service.PublishReport(ctx, researchworkflow.ReportRequest{ProjectID: invocation.ProjectID, WorkflowRunID: invocation.RunID, ResearchTaskID: invocation.ResearchTaskID, ToolCallID: invocation.CallID, IdempotencyKey: stableWorkflowOperationKey(invocation), Name: args.Name, Markdown: markdown, Citations: args.Citations, Analysis: analysisSnapshot, SourceArtifacts: args.SourceArtifacts, ReviewGate: args.ReviewGate})
	if err != nil {
		return tool.Result{}, workflowReportError(err)
	}
	structured, _ := json.Marshal(value)
	text := fmt.Sprintf("Published trusted report %s with immutable Markdown, DOCX, and PDF exports.", strings.TrimSpace(args.Name))
	return tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured, Artifacts: []tool.ArtifactRef{{ID: value.Artifact.ID, Name: value.Artifact.Name}, {ID: value.DOCX.ID, Name: value.DOCX.FileName, MIMEType: value.DOCX.MIMEType}, {ID: value.PDF.ID, Name: value.PDF.FileName, MIMEType: value.PDF.MIMEType}}, Citations: []tool.CitationRef{}}, nil
}

func workflowReportError(err error) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "report Markdown contains unverified marker"), strings.Contains(message, "report Markdown does not cite selected marker"):
		return tool.NewUserFacingError("报告正文中的引用标记与本次科研任务人工选定的证据不一致；请重试报告阶段，系统会使用当前任务冻结的引用重新生成")
	case strings.Contains(message, "report citation was not returned"), strings.Contains(message, "report citation marker or quote hash was altered"), strings.Contains(message, "report citation no longer matches"):
		return tool.NewUserFacingError("报告引用未通过当前科研任务的证据快照校验；请返回证据选择阶段重新核验引用")
	case strings.Contains(message, "report source Artifact"):
		return tool.NewUserFacingError("报告引用的分析产物不属于当前科研任务或内容已变化；请重新执行分析阶段")
	case strings.Contains(message, "independent review gate"):
		return tool.NewUserFacingError("报告未通过当前科研任务的独立二次审查门禁；请重新执行审查阶段")
	default:
		return err
	}
}

func composeWorkflowReport(body string, citations []tool.CitationRef, artifacts []tool.ArtifactRef) string {
	var result strings.Builder
	result.WriteString(strings.TrimSpace(body))
	// The full Analysis snapshot stays in PublishReport's immutable audit.
	// It is not user-facing prose and must not be appended as internal JSON.
	if len(artifacts) > 0 {
		result.WriteString("\n\n## 分析产物\n")
		for _, value := range artifacts {
			name := strings.TrimSpace(value.Name)
			if name == "" {
				name = filepath.Base(value.WorkspacePath)
			}
			fmt.Fprintf(&result, "\n- `%s` (`%s`)", name, value.WorkspacePath)
		}
	}
	result.WriteString("\n\n## 已核验证据\n")
	for _, value := range citations {
		fmt.Fprintf(&result, "\n- %s %s，%s：%s", value.Reference, value.SourceName, value.Locator, strings.TrimSpace(value.Quote))
	}
	return strings.TrimSpace(result.String())
}

func stableWorkflowOperationKey(invocation tool.Invocation) string {
	provider := strings.TrimSpace(invocation.ProviderCallID)
	if index := strings.LastIndex(provider, ":"); index > 0 {
		provider = provider[:index]
	}
	return invocation.RunID + ":" + provider
}
