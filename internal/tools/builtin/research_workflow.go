package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchworkflow"
	"github.com/wangh00/SciAide/internal/app/reviewcontract"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/localexec"
	"github.com/wangh00/SciAide/internal/researchsources"
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
		Review  json.RawMessage `json:"review"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	receipt, err := reviewcontract.Evaluate(args.Subject, args.Review)
	if err != nil {
		var rejected *reviewcontract.Rejected
		if errors.As(err, &rejected) {
			return tool.Result{}, tool.BusinessDecisionError{Code: "RESEARCH_REVIEW_NOT_APPROVED", Message: rejected.Error()}
		}
		return tool.Result{}, reportPublicationError{err, "交付核验的审查输入绑定失败。这是技术错误，不代表科研内容被否决；报告和审查已保留，请检查诊断后重试交付核验。"}
	}
	structured, _ := json.Marshal(receipt)
	return tool.Result{Status: tool.ResultSuccess, Text: "Independent review passed the deterministic delivery gate for the exact frozen result.", Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var researchWorkflowNetworkPermissions = tool.ResearchSearchNetworkPermissions()

var researchFullTextNetworkPermissions = tool.ResearchMaterialNetworkPermissions()

func (*ResearchWorkflowSearch) Definition(context.Context) (tool.Definition, error) {
	d := tool.Definition{QualifiedName: ResearchWorkflowSearchName, Version: "9", Risk: tool.RiskModerate, Idempotent: true,
		Description:  "Search public scholarly databases and persist the normalized, deduplicated candidates in the current project. Results remain untrusted discovery data until selected, imported, indexed, and searched locally. CORE requires explicit sourceIds selection and only returns metadata; it is never added to default searches.",
		Permissions:  append([]tool.PermissionRequirement(nil), researchWorkflowNetworkPermissions...),
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":500},"queries":{"type":"array","maxItems":4,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":500}},"sourceIds":{"type":"array","maxItems":12,"uniqueItems":true,"items":{"type":"string","enum":` + researchSourceIDsJSON + `}},"offset":{"type":"integer","minimum":0,"maximum":150},"publicationYears":{"type":"object","additionalProperties":false,"required":["from","to"],"properties":{"from":{"type":"integer","minimum":0,"maximum":3000},"to":{"type":"integer","minimum":0,"maximum":3000}}},"referencesOnly":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":50}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["queryId","queryIds","queryText","queries","candidates","sources","partial","searchErrors"],"properties":{"queryId":{"type":"string"},"queryIds":{"type":"array","items":{"type":"string"}},"queryText":{"type":"string"},"queries":{"type":"array","items":{"type":"string"}},"candidates":{"type":"array","items":{"type":"object"}},"candidateIds":{"type":"array","items":{"type":"string"}},"candidateCount":{"type":"integer"},"sources":{"type":"array","items":{"type":"object"}},"partial":{"type":"boolean"},"searchErrors":{"type":"array","items":{"type":"string"}}}}`),
	}
	var schema map[string]any
	_ = json.Unmarshal(d.InputSchema, &schema)
	p := schema["properties"].(map[string]any)
	p["queries"].(map[string]any)["maxItems"] = 1
	p["offset"] = map[string]any{"type": "integer", "const": 0}
	p["limit"].(map[string]any)["maximum"] = 20
	providerProps := map[string]any{}
	for _, source := range researchsources.PlanQueryIDs(false) {
		providerProps[source] = map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}}
	}
	p["providerQueries"] = map[string]any{"type": "object", "additionalProperties": false, "properties": providerProps}
	p["retrievalProfile"] = map[string]any{"type": "string", "enum": research.RetrievalProfiles(), "description": "Explicit discipline strategy. When present, search has a shared 14 HTTP-attempt ceiling including retries/redirects, no per-record enrichment, and a persisted source/slot plan. Explicit sourceIds override profile selection, not the ceiling. Omit for legacy source behavior."}
	p["sourceRationale"] = map[string]any{"type": "string", "maxLength": 800}
	out := map[string]any{}
	_ = json.Unmarshal(d.OutputSchema, &out)
	out["properties"].(map[string]any)["searchPlan"] = map[string]any{"type": "object"}
	d.OutputSchema, _ = json.Marshal(out)
	d.InputSchema, _ = json.Marshal(schema)
	return d, nil
}

func (t *ResearchWorkflowSearch) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		RetrievalProfile string                    `json:"retrievalProfile"`
		SourceRationale  string                    `json:"sourceRationale"`
		ProviderQueries  map[string][]string       `json:"providerQueries"`
		Query            string                    `json:"query"`
		Years            research.PublicationYears `json:"publicationYears"`
		Queries          []string                  `json:"queries"`
		SourceIDs        []string                  `json:"sourceIds"`
		Limit            int                       `json:"limit"`
		ReferencesOnly   bool                      `json:"referencesOnly"`
		Offset           int                       `json:"offset"`
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
	var plan *research.RetrievalPlan
	var budget *research.SourceHTTPBudget
	if args.RetrievalProfile != "" {
		value, err := research.BuildRetrievalPlan(args.RetrievalProfile, args.SourceIDs, len(queries))
		if err != nil {
			return tool.Result{}, err
		}
		ctx, budget, err = research.WithSourceHTTPBudget(ctx, value.MaxHTTP)
		if err != nil {
			return tool.Result{}, err
		}
		plan = &value
	}
	queryIDs := []string{}
	allSources := []research.SourceSearch{}
	searchErrors := []string{}
	partial := false
	search := func(callCtx context.Context, index int, query string, sourceIDs []string) error {
		projections := map[string]string{}
		for source, values := range args.ProviderQueries {
			projections[source] = values[index]
		}
		if err := callCtx.Err(); err != nil {
			return err
		}
		value, err := t.service.SearchScopedPage(callCtx, research.DiscoverySearchCommand{ProjectID: invocation.ProjectID, Query: query, ProviderQueries: projections, SourceIDs: sourceIDs, Limit: args.Limit, Offset: args.Offset, SnapshotKey: invocation.CallID, ResearchTaskID: invocation.ResearchTaskID, Years: args.Years})
		if err != nil {
			if callCtx.Err() != nil {
				return callCtx.Err()
			}
			partial = true
			searchErrors = append(searchErrors, fmt.Sprintf("%s: %v", query, err))
			return nil
		}
		queryIDs = append(queryIDs, value.Query.ID)
		allSources = append(allSources, value.Query.Sources...)
		partial = partial || value.Query.Partial
		return nil
	}
	if plan == nil {
		for index, query := range queries {
			if err := search(ctx, index, query, args.SourceIDs); err != nil {
				return tool.Result{}, err
			}
		}
	} else {
		for index, query := range queries {
			sources := plan.SourcesForQuery(index)
			if len(sources) == 0 {
				continue
			} // Never expand an empty schedule to defaults.
			callCtx, err := research.WithRetrievalSchedule(ctx, plan, index)
			if err != nil {
				return tool.Result{}, err
			}
			if err := search(callCtx, index, query, sources); err != nil {
				return tool.Result{}, err
			}
		}
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
	output := map[string]any{"queryId": queryIDs[0], "queryIds": queryIDs, "queryText": queries[0], "queries": queries, "candidates": display, "candidateIds": ids, "candidateCount": len(ids), "sources": allSources, "partial": partial, "searchErrors": searchErrors}
	text := fmt.Sprintf("Persisted %d deduplicated candidates for paged AI screening. No candidates were discarded by a model batch limit.", len(ids))
	if plan != nil {
		plan.UsedHTTP, plan.BudgetDenied = budget.Usage()
		output["searchPlan"] = map[string]any{"allocation": plan, "rationale": args.SourceRationale,
			"limits": "Fixed per-invocation HTTP ceiling, including retries/redirects. Deferred or failed slots are not empty evidence. No per-record enrichment or exhaustive-coverage claim."}
		output["partial"] = partial || plan.BudgetDenied > 0
		text += fmt.Sprintf(" 检索策略 %s / %s，实际来源 HTTP %d/%d，未安排槽位 %d；逐来源理由与执行槽位见 searchPlan。不是独立研究计数或穷尽检索。", plan.Profile, plan.SelectionMode, plan.UsedHTTP, plan.MaxHTTP, len(plan.Deferred))
	}
	structured, _ := json.Marshal(output)
	return tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowImport) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowImportName, Version: "6", Risk: tool.RiskHigh, Idempotent: true,
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
		ReportDraft     json.RawMessage    `json:"reportDraft"`
		SourceArtifacts []tool.ArtifactRef `json:"sourceArtifacts"`
		ReviewGate      json.RawMessage    `json:"reviewGate"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	var report struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(args.ReportDraft, &report); err != nil {
		return tool.Result{}, err
	}
	markdownBody, analysisSnapshot := report.Markdown, args.ReportDraft
	markdown := composeWorkflowReport(markdownBody, args.Citations, args.SourceArtifacts)
	value, err := t.service.PublishReport(ctx, researchworkflow.ReportRequest{ProjectID: invocation.ProjectID, WorkflowRunID: invocation.RunID, ResearchTaskID: invocation.ResearchTaskID, ToolCallID: invocation.CallID, IdempotencyKey: stableWorkflowOperationKey(invocation), Name: args.Name, Markdown: markdown, Citations: args.Citations, Analysis: analysisSnapshot, SourceArtifacts: args.SourceArtifacts, ReviewGate: args.ReviewGate})
	if err != nil {
		return tool.Result{}, workflowReportError(err)
	}
	structured, _ := json.Marshal(value)
	text := fmt.Sprintf("Published trusted report %s with immutable Markdown, DOCX, and PDF exports.", strings.TrimSpace(args.Name))
	return tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured, Artifacts: []tool.ArtifactRef{{ID: value.Artifact.ID, Name: value.Artifact.Name}, {ID: value.DOCX.ID, Name: value.DOCX.FileName, MIMEType: value.DOCX.MIMEType}, {ID: value.PDF.ID, Name: value.PDF.FileName, MIMEType: value.PDF.MIMEType}}, Citations: []tool.CitationRef{}}, nil
}

// Keep the diagnostic cause in host logs while exposing only a bounded,
// actionable explanation to the researcher. Do not turn binding failures into
// scientific rejections or erase the actual assembly failure.
type reportPublicationError struct {
	cause   error
	message string
}

func (e reportPublicationError) Error() string             { return e.cause.Error() }
func (e reportPublicationError) Unwrap() error             { return e.cause }
func (e reportPublicationError) UserFacingMessage() string { return e.message }

func workflowReportError(err error) error {
	message := err.Error()
	switch {
	case errors.Is(err, researchworkflow.ErrPublicationIdentityUnavailable):
		return reportPublicationError{err, "报告发布恢复校验失败：无法核对旧发布回执与本次冻结报告、引用及审查快照。已完成的新稿和审查保留；请检查发布来源记录，不要重新生成整篇报告。"}
	case errors.Is(err, researchworkflow.ErrReviewReportMismatch):
		return reportPublicationError{err, "报告发布校验失败：待发布报告与已审查报告的内容快照不一致。这不是独立审查否决；已完成的审查和报告保留，请检查发布绑定，不要重复返修。"}
	case errors.Is(err, researchworkflow.ErrReviewReportInvalid):
		return reportPublicationError{err, "报告发布校验失败：无法完整还原审查中的报告正文与记录附录。这是快照绑定错误，不是独立审查否决；已完成的内容保留，请检查程序诊断。"}
	case errors.Is(err, researchworkflow.ErrReviewGateUnavailable):
		return reportPublicationError{err, "报告发布校验失败：找不到与本次发布匹配的已完成交付核验记录。已完成的报告与审查保留；请检查交付核验和发布绑定，不要直接返修报告。"}
	case strings.Contains(message, "report Markdown contains unverified marker"), strings.Contains(message, "report Markdown does not cite selected marker"):
		return tool.NewUserFacingError("报告正文中的引用标记与本次科研任务人工选定的证据不一致；请重试报告阶段，系统会使用当前任务冻结的引用重新生成")
	case strings.Contains(message, "report citation was not returned"), strings.Contains(message, "report citation marker or quote hash was altered"), strings.Contains(message, "report citation no longer matches"):
		return tool.NewUserFacingError("报告引用未通过当前科研任务的证据快照校验；请返回证据选择阶段重新核验引用")
	case strings.Contains(message, "report source Artifact"):
		return tool.NewUserFacingError("报告引用的分析产物不属于当前科研任务或内容已变化；请重新执行分析阶段")
	case strings.Contains(message, "independent review gate"):
		return reportPublicationError{err, "报告发布所需的交付核验记录无效或缺失。这是发布校验错误，不代表报告科学内容未通过独立审查；请检查交付核验记录。"}
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
