package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchworkflow"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const (
	ResearchWorkflowSearchName = "builtin.research.workflow.search"
	ResearchWorkflowImportName = "builtin.research.workflow.import"
	ResearchWorkflowSyncName   = "builtin.research.workflow.sync"
	ResearchWorkflowPythonName = "builtin.research.workflow.python.ensure"
	ResearchWorkflowReportName = researchworkflow.ReportToolName
)

type ResearchWorkflowSearch struct{ service *researchworkflow.Service }
type ResearchWorkflowImport struct{ service *researchworkflow.Service }
type ResearchWorkflowSync struct{ service *researchworkflow.Service }
type ResearchWorkflowPython struct{ service *researchworkflow.Service }
type ResearchWorkflowReport struct{ service *researchworkflow.Service }

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
func NewResearchWorkflowReport(service *researchworkflow.Service) *ResearchWorkflowReport {
	return &ResearchWorkflowReport{service: service}
}

var researchWorkflowNetworkPermissions = []tool.PermissionRequirement{
	{Kind: tool.PermissionNetworkDomain, Resource: "api.openalex.org:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "api.crossref.org:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "export.arxiv.org:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "eutils.ncbi.nlm.nih.gov:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "www.ebi.ac.uk:443"},
	{Kind: tool.PermissionNetworkDomain, Resource: "api.semanticscholar.org:443"},
}

func (*ResearchWorkflowSearch) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowSearchName, Version: "1", Risk: tool.RiskModerate, Idempotent: true,
		Description:  "Search public scholarly databases and persist the normalized, deduplicated candidates in the current project. Results remain untrusted discovery data until selected, imported, indexed, and searched locally.",
		Permissions:  append([]tool.PermissionRequirement(nil), researchWorkflowNetworkPermissions...),
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":500},"sourceIds":{"type":"array","maxItems":12,"uniqueItems":true,"items":{"type":"string","enum":["openalex","crossref","arxiv","pubmed","europepmc","semantic-scholar"]}},"limit":{"type":"integer","minimum":1,"maximum":20}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["queryId","queryText","candidates","sources","partial"],"properties":{"queryId":{"type":"string"},"queryText":{"type":"string"},"candidates":{"type":"array","items":{"type":"object"}},"sources":{"type":"array","items":{"type":"object"}},"partial":{"type":"boolean"}}}`),
	}, nil
}

func (t *ResearchWorkflowSearch) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		Query     string   `json:"query"`
		SourceIDs []string `json:"sourceIds"`
		Limit     int      `json:"limit"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, err := t.service.Search(ctx, invocation.ProjectID, args.Query, args.SourceIDs, args.Limit)
	if err != nil {
		return tool.Result{}, err
	}
	type candidate struct {
		ID         string            `json:"id"`
		Title      string            `json:"title"`
		Authors    []research.Author `json:"authors"`
		Year       int               `json:"year,omitempty"`
		Venue      string            `json:"venue,omitempty"`
		DOI        string            `json:"doi,omitempty"`
		SourceIDs  []string          `json:"sourceIds"`
		OpenAccess bool              `json:"openAccess"`
		Abstract   string            `json:"abstract,omitempty"`
	}
	candidates := make([]candidate, 0, len(value.Page.Items))
	for _, item := range value.Page.Items {
		sources := make([]string, 0, len(item.Records))
		for _, record := range item.Records {
			sources = append(sources, record.Work.SourceID)
		}
		abstract := item.Preferred.Abstract
		if len([]rune(abstract)) > 3000 {
			abstract = string([]rune(abstract)[:3000])
		}
		authors := item.Preferred.Authors
		if len(authors) > 20 {
			authors = authors[:20]
		}
		candidates = append(candidates, candidate{ID: item.ID, Title: item.Preferred.Title, Authors: authors, Year: item.Preferred.Year, Venue: item.Preferred.Venue, DOI: item.Preferred.Identifiers.DOI, SourceIDs: sources, OpenAccess: item.Preferred.OpenAccess, Abstract: abstract})
	}
	structured, _ := json.Marshal(map[string]any{"queryId": value.Query.ID, "queryText": value.Query.Text, "candidates": candidates, "sources": value.Query.Sources, "partial": value.Query.Partial})
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Persisted %d untrusted discovery candidates. Select candidate IDs before local import.", len(candidates)), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowImport) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowImportName, Version: "1", Risk: tool.RiskHigh, Idempotent: true,
		Description:  "Mark the exact human-selected research candidates as included, materialize open full text or disclosed metadata/abstract files, and import them as project attachments. Completed candidates are reused on retry.",
		Permissions:  append(append([]tool.PermissionRequirement{}, researchWorkflowNetworkPermissions...), tool.PermissionRequirement{Kind: tool.PermissionWorkspaceWrite, Resource: "."}),
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["selectedCandidateIds"],"properties":{"selectedCandidateIds":{"type":"array","minItems":1,"maxItems":20,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}},"mode":{"type":"string","enum":["auto","full_text","metadata_abstract"]}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["materials","attachmentIds"],"properties":{"materials":{"type":"array","items":{"type":"object"}},"attachmentIds":{"type":"array","items":{"type":"string"}}}}`),
	}, nil
}

func (t *ResearchWorkflowImport) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		IDs  []string                 `json:"selectedCandidateIds"`
		Mode research.MaterializeMode `json:"mode"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	values, err := t.service.ImportSelected(ctx, invocation.ProjectID, args.IDs, args.Mode)
	if err != nil {
		return tool.Result{}, err
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.AttachmentID)
	}
	structured, _ := json.Marshal(map[string]any{"materials": values, "attachmentIds": ids})
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Imported %d selected research materials as local project attachments.", len(values)), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func (*ResearchWorkflowSync) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowSyncName, Version: "1", Risk: tool.RiskModerate, Idempotent: true, Description: "Drain knowledge indexing for the selected imported attachments and prove that every material is ready in the current active project index before continuing.", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}, {Kind: tool.PermissionWorkspaceWrite, Resource: "."}}, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachmentIds"],"properties":{"attachmentIds":{"type":"array","minItems":1,"maxItems":20,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}}}}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["documents","documentIds","attachmentIds"],"properties":{"documents":{"type":"array","items":{"type":"object"}},"documentIds":{"type":"array","items":{"type":"string"}},"attachmentIds":{"type":"array","items":{"type":"string"}}}}`)}, nil
}

func (t *ResearchWorkflowSync) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		IDs []string `json:"attachmentIds"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	values, err := t.service.Synchronize(ctx, invocation.ProjectID, args.IDs)
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
	value, created, err := t.service.EnsureEnvironment(ctx, invocation.ProjectID)
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

func (*ResearchWorkflowReport) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: ResearchWorkflowReportName, Version: "1", Risk: tool.RiskHigh, Idempotent: true, Description: "Publish a trusted immutable Markdown report from a Workflow, revalidate every selected local knowledge citation, freeze upstream ToolCalls and declared analysis files, and generate GB/T 7714 DOCX and PDF exports.", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}, {Kind: tool.PermissionWorkspaceWrite, Resource: "."}}, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","markdown","citations"],"properties":{"name":{"type":"string","minLength":1,"maxLength":120},"markdown":{"type":"string","minLength":1,"maxLength":1900000},"citations":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"object"}},"analysis":{"type":"object"},"sourceArtifacts":{"type":"array","maxItems":64,"items":{"type":"object"}}}}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["artifact","version","docx","pdf","created"],"properties":{"artifact":{"type":"object"},"version":{"type":"object"},"docx":{"type":"object"},"pdf":{"type":"object"},"created":{"type":"boolean"}}}`)}, nil
}

func (t *ResearchWorkflowReport) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if tool.NormalizeSubjectKind(invocation.SubjectKind) != tool.SubjectWorkflowRun {
		return tool.Result{}, fmt.Errorf("trusted research report can only run inside a Workflow Run")
	}
	var args struct {
		Name            string             `json:"name"`
		Markdown        string             `json:"markdown"`
		Citations       []tool.CitationRef `json:"citations"`
		Analysis        map[string]any     `json:"analysis"`
		SourceArtifacts []tool.ArtifactRef `json:"sourceArtifacts"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	markdown := composeWorkflowReport(args.Markdown, args.Citations, args.Analysis, args.SourceArtifacts)
	value, err := t.service.PublishReport(ctx, researchworkflow.ReportRequest{ProjectID: invocation.ProjectID, WorkflowRunID: invocation.RunID, ToolCallID: invocation.CallID, IdempotencyKey: stableWorkflowOperationKey(invocation), Name: args.Name, Markdown: markdown, Citations: args.Citations, SourceArtifacts: args.SourceArtifacts})
	if err != nil {
		return tool.Result{}, err
	}
	structured, _ := json.Marshal(value)
	text := fmt.Sprintf("Published trusted report %s with immutable Markdown, DOCX, and PDF exports.", strings.TrimSpace(args.Name))
	return tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured, Artifacts: []tool.ArtifactRef{{ID: value.Artifact.ID, Name: value.Artifact.Name}, {ID: value.DOCX.ID, Name: value.DOCX.FileName, MIMEType: value.DOCX.MIMEType}, {ID: value.PDF.ID, Name: value.PDF.FileName, MIMEType: value.PDF.MIMEType}}, Citations: []tool.CitationRef{}}, nil
}

func composeWorkflowReport(body string, citations []tool.CitationRef, analysis map[string]any, artifacts []tool.ArtifactRef) string {
	var result strings.Builder
	result.WriteString(strings.TrimSpace(body))
	if len(analysis) > 0 {
		encoded, _ := json.MarshalIndent(analysis, "", "  ")
		if len(encoded) > 24*1024 {
			encoded = encoded[:24*1024]
		}
		result.WriteString("\n\n## 分析执行摘要\n\n```json\n")
		result.Write(encoded)
		result.WriteString("\n```")
	}
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
