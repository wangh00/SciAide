package researchworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type workflowKnowledgeFixture struct{ chunk knowledge.EvidenceChunk }

func (f workflowKnowledgeFixture) SynchronizeAttachments(context.Context, string, []string) ([]knowledge.Document, error) {
	return nil, nil
}
func (f workflowKnowledgeFixture) ReadEvidenceChunk(_ context.Context, projectID, indexID, documentID, attachmentID, chunkID string) (knowledge.EvidenceChunk, error) {
	if projectID != "project" || indexID != f.chunk.IndexVersionID || documentID != f.chunk.DocumentID || attachmentID != f.chunk.AttachmentID || chunkID != f.chunk.ChunkID {
		return knowledge.EvidenceChunk{}, context.Canceled
	}
	return f.chunk, nil
}

type workflowBibliographyFixture struct{ snapshot research.CitationSnapshot }

func (f workflowBibliographyFixture) CitationSnapshotForAttachment(context.Context, string, string) (research.CitationSnapshot, error) {
	return f.snapshot, nil
}

type taskScopedKnowledgeFixture struct{ chunk knowledge.EvidenceChunk }

func (f taskScopedKnowledgeFixture) SynchronizeAttachments(context.Context, string, []string) ([]knowledge.Document, error) {
	return nil, nil
}
func (f taskScopedKnowledgeFixture) ReadEvidenceChunk(context.Context, string, string, string, string, string) (knowledge.EvidenceChunk, error) {
	return f.chunk, nil
}
func (f taskScopedKnowledgeFixture) ReadEvidenceChunkForTask(_ context.Context, projectID, taskID, indexID, documentID, attachmentID, chunkID string) (knowledge.EvidenceChunk, error) {
	if projectID != "project" || taskID != "task-a" || indexID != f.chunk.IndexVersionID || documentID != f.chunk.DocumentID || attachmentID != f.chunk.AttachmentID || chunkID != f.chunk.ChunkID {
		return knowledge.EvidenceChunk{}, fmt.Errorf("reference is outside task scope")
	}
	return f.chunk, nil
}

type reportUserMaterialLoader struct {
	value       attachment.Attachment
	allowedTask string
}

func (f reportUserMaterialLoader) ReferenceMaterials(_ context.Context, projectID, taskID string, ids []string) ([]attachment.Attachment, error) {
	if projectID != "project" || taskID != f.allowedTask || len(ids) != 1 || ids[0] != f.value.ID {
		return nil, fmt.Errorf("reference material is outside task scope")
	}
	return []attachment.Attachment{f.value}, nil
}

type workflowCallsFixture struct{ calls []tool.Call }

func (f workflowCallsFixture) ListBySubject(context.Context, tool.SubjectKind, string) ([]tool.Call, error) {
	return f.calls, nil
}

type workflowArtifactsFixture struct {
	command artifact.WorkflowReportCommand
}

func (f *workflowArtifactsFixture) PublishWorkflowReport(_ context.Context, command artifact.WorkflowReportCommand) (artifact.WorkflowReportResult, error) {
	f.command = command
	return artifact.WorkflowReportResult{Artifact: artifact.Artifact{ID: "artifact"}, Version: artifact.Version{ID: "version"}, DOCX: artifact.Export{ID: "docx"}, PDF: artifact.Export{ID: "pdf"}, Created: true}, nil
}

type unusedDiscovery struct{}

func (unusedDiscovery) Search(context.Context, research.DiscoverySearchCommand) (research.DiscoverySearchResult, error) {
	return research.DiscoverySearchResult{}, nil
}
func (unusedDiscovery) GetCandidate(context.Context, string, string) (research.Candidate, error) {
	return research.Candidate{}, nil
}
func (unusedDiscovery) UpdateReview(context.Context, research.ReviewCommand) (research.Candidate, error) {
	return research.Candidate{}, nil
}
func (unusedDiscovery) ImportCandidate(context.Context, research.ImportCandidateCommand) (research.ImportCandidateResult, error) {
	return research.ImportCandidateResult{}, nil
}

type unusedEnvironment struct{}

func (unusedEnvironment) Get(context.Context, string) (pythonenv.Environment, error) {
	return pythonenv.Environment{}, nil
}
func (unusedEnvironment) Create(context.Context, string, string, bool) (pythonenv.Environment, error) {
	return pythonenv.Environment{}, nil
}
func (unusedEnvironment) Verify(context.Context, string) (pythonenv.Environment, error) {
	return pythonenv.Environment{}, nil
}

type environmentActionFixture struct {
	current     pythonenv.Environment
	createCalls int
	verifyCalls int
}

func (f *environmentActionFixture) Get(context.Context, string) (pythonenv.Environment, error) {
	return f.current, nil
}
func (f *environmentActionFixture) Create(context.Context, string, string, bool) (pythonenv.Environment, error) {
	f.createCalls++
	return f.current, nil
}
func (f *environmentActionFixture) Verify(context.Context, string) (pythonenv.Environment, error) {
	f.verifyCalls++
	return f.current, nil
}

func TestEnsureEnvironmentNeverRebuildsBrokenExternalEnvironment(t *testing.T) {
	environments := &environmentActionFixture{current: pythonenv.Environment{ProjectID: "project", State: pythonenv.StateBroken, Kind: pythonenv.KindExternal}}
	service, err := New(unusedDiscovery{}, workflowKnowledgeFixture{}, workflowBibliographyFixture{}, environments, workflowCallsFixture{}, &workflowArtifactsFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := service.EnsureEnvironment(context.Background(), "project"); err != nil || created || environments.verifyCalls != 1 || environments.createCalls != 0 {
		t.Fatalf("external environment ensure created=%v createCalls=%d verifyCalls=%d err=%v", created, environments.createCalls, environments.verifyCalls, err)
	}
}

func TestPublishReportRevalidatesKnowledgeAndFreezesBibliography(t *testing.T) {
	quote := "...The verified result is reproducible...."
	chunk := knowledge.EvidenceChunk{IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.md", MIMEType: "text/markdown", Locator: "section:results", Title: "Results", Content: "Introduction. The verified result is reproducible. Limitations.", ContentSHA256: strings.Repeat("a", 64), SourceStart: 10, SourceEnd: 70}
	ref := tool.CitationRef{ID: "attachment", Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: chunk.IndexVersionID, DocumentID: chunk.DocumentID, AttachmentID: chunk.AttachmentID, ChunkID: chunk.ChunkID, SourceName: chunk.SourceName, MIMEType: chunk.MIMEType, Locator: chunk.Locator, Title: chunk.Title, Quote: quote, QuoteSHA256: citation.QuoteSHA256(quote), SourceStart: chunk.SourceStart, SourceEnd: chunk.SourceEnd}
	ref.Reference = citation.KnowledgeReference("workflow-run", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	result := tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}, Artifacts: []tool.ArtifactRef{{Name: "analysis.csv", WorkspacePath: "analysis-output/analysis.csv"}}}
	analysis := json.RawMessage(`{"summary":"verified"}`)
	reviewGate := json.RawMessage(`{"approved":true,"reviewedInputSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","reviewSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","confidence":"high","verifiedClaimCount":1,"limitations":[]}`)
	gateArguments, _ := json.Marshal(map[string]any{"subject": json.RawMessage(analysis), "review": map[string]any{}})
	gateResult := tool.Result{Status: tool.ResultSuccess, Structured: reviewGate, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}
	calls := []tool.Call{
		{ID: "search-call", RunID: "workflow-run", SubjectKind: tool.SubjectWorkflowRun, ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &result},
		{ID: "review-gate-call", RunID: "workflow-run", SubjectKind: tool.SubjectWorkflowRun, ToolName: ReviewGateToolName, Arguments: gateArguments, Status: tool.CallCompleted, Result: &gateResult},
	}
	bibliography, _ := json.Marshal(map[string]any{"schemaVersion": 1, "bibliographyId": "bibliography", "revision": 1, "capturedAt": time.Now().UTC()})
	artifacts := &workflowArtifactsFixture{}
	service, err := New(unusedDiscovery{}, workflowKnowledgeFixture{chunk}, workflowBibliographyFixture{research.CitationSnapshot{BibliographyID: "bibliography", Bibliography: bibliography, EvidenceLevel: research.EvidenceFullText}}, unusedEnvironment{}, workflowCallsFixture{calls}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	request := ReportRequest{ProjectID: "project", WorkflowRunID: "workflow-run", ToolCallID: "report-call", IdempotencyKey: "stable", Name: "Report", Markdown: "# Result\n\nVerified. " + ref.Reference, Citations: []tool.CitationRef{ref}, Analysis: analysis, SourceArtifacts: result.Artifacts, ReviewGate: reviewGate}
	value, err := service.PublishReport(context.Background(), request)
	if err != nil || value.Artifact.ID != "artifact" {
		t.Fatalf("PublishReport()=%#v, %v", value, err)
	}
	if len(artifacts.command.Citations) != 1 || artifacts.command.Citations[0].BibliographyIDSnapshot != "bibliography" || artifacts.command.Citations[0].SourceToolCallIDSnapshot != "search-call" || artifacts.command.OperationKey != "stable" {
		t.Fatalf("frozen report=%#v", artifacts.command)
	}
	if len(artifacts.command.SourceWorkspaceFiles) != 1 || artifacts.command.SourceWorkspaceFiles[0] != "analysis-output/analysis.csv" {
		t.Fatalf("source files=%#v", artifacts.command.SourceWorkspaceFiles)
	}

	tampered := ref
	tampered.Quote = "altered"
	request.Citations = []tool.CitationRef{tampered}
	if _, err := service.PublishReport(context.Background(), request); err == nil {
		t.Fatal("tampered citation was accepted")
	}
	request.Citations = []tool.CitationRef{ref}
	request.Analysis = json.RawMessage(`{"summary":"different result"}`)
	if _, err := service.PublishReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("mismatched review subject error=%v", err)
	}
	request.Analysis = analysis
	request.Markdown += " [K-FFFFFFFFFFFF]"
	if _, err := service.PublishReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unverified marker") {
		t.Fatalf("unverified marker error=%v", err)
	}
	request.Markdown = "# Missing marker"
	if _, err := service.PublishReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not cite") {
		t.Fatalf("missing marker error=%v", err)
	}
	request.Markdown = "# Report\n\nThe verified result is reproducible. [1]"
	if _, err := service.PublishReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not cite") {
		t.Fatalf("manual numbering bypassed the stored citation contract: %v", err)
	}
}

func TestPublishReportRejectsCitationFromAnotherWorkflowRun(t *testing.T) {
	ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.md", MIMEType: "text/markdown", Locator: "section:1", Quote: "verified", QuoteSHA256: citation.QuoteSHA256("verified")}
	ref.Reference = citation.KnowledgeReference("other-run", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	service, _ := New(unusedDiscovery{}, workflowKnowledgeFixture{}, workflowBibliographyFixture{}, unusedEnvironment{}, workflowCallsFixture{[]tool.Call{{ID: "call", RunID: "other-run", SubjectKind: tool.SubjectWorkflowRun, ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}}}}, &workflowArtifactsFixture{})
	_, err := service.PublishReport(context.Background(), ReportRequest{ProjectID: "project", WorkflowRunID: "workflow-run", ToolCallID: "report", Name: "Report", Markdown: ref.Reference, Citations: []tool.CitationRef{ref}})
	if err == nil {
		t.Fatal("cross-Workflow citation was accepted")
	}
}

func TestAbstractCitationTitleRecoveryRetainsEvidenceChecks(t *testing.T) {
	chunk := knowledge.EvidenceChunk{IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "research-paper-metadata.md", MIMEType: "text/markdown", Locator: "lines:1-24", Content: "## Abstract\nVerified result.\n## Source records", SourceEnd: 51}
	ref := tool.CitationRef{ID: "attachment", Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: chunk.IndexVersionID, DocumentID: chunk.DocumentID, AttachmentID: chunk.AttachmentID, ChunkID: chunk.ChunkID, SourceName: chunk.SourceName, MIMEType: chunk.MIMEType, Locator: chunk.Locator, Title: "Abstract", Quote: "Verified result.", QuoteSHA256: citation.QuoteSHA256("Verified result."), SourceEnd: chunk.SourceEnd}
	ref.Reference = citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	call := tool.Call{ID: "search", ToolName: citation.KnowledgeToolName, ToolVersion: "6", Arguments: json.RawMessage(`{"perDocument":true,"documentIds":["document"]}`)}
	s := &Service{knowledge: workflowKnowledgeFixture{chunk}, bibliography: workflowBibliographyFixture{research.CitationSnapshot{EvidenceLevel: research.EvidenceMetadataAbstract}}}
	if _, err := s.verifyCitation(context.Background(), "project", "workflow", "", call, ref); err != nil {
		t.Fatal(err)
	}
	call.RunID, call.SubjectKind, call.Status = "workflow", tool.SubjectWorkflowRun, tool.CallCompleted
	call.Result = &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}
	gate := tool.Call{ID: "gate", RunID: "workflow", SubjectKind: tool.SubjectWorkflowRun, ToolName: ReviewGateToolName, Status: tool.CallCompleted, Arguments: json.RawMessage(`{"subject":{"summary":"verified"}}`), Result: &tool.Result{Status: tool.ResultSuccess, Structured: json.RawMessage(`{"approved":true}`)}}
	s.toolCalls = workflowCallsFixture{[]tool.Call{call, gate}}
	s.artifacts = &workflowArtifactsFixture{}
	request := ReportRequest{ProjectID: "project", WorkflowRunID: "workflow", ToolCallID: "publish", Name: "Recovered report", Markdown: "Finding " + ref.Reference, Citations: []tool.CitationRef{ref}, Analysis: json.RawMessage(`{"summary":"verified"}`), ReviewGate: gate.Result.Structured}
	if _, err := s.PublishReport(context.Background(), request); err != nil {
		t.Fatal("legacy snapshot publication failed", err)
	}
	request.Citations = append([]tool.CitationRef(nil), request.Citations...)
	request.Citations[0].Title = ""
	if _, err := s.PublishReport(context.Background(), request); err == nil {
		t.Fatal("publication accepted a rewritten historical snapshot")
	}
	for _, mutate := range []func(*tool.CitationRef, *tool.Call){
		func(r *tool.CitationRef, _ *tool.Call) { r.Title = "Invented" },
		func(r *tool.CitationRef, _ *tool.Call) { r.SourceStart++ },
		func(r *tool.CitationRef, _ *tool.Call) { r.Locator = "changed" },
		func(r *tool.CitationRef, _ *tool.Call) { r.Quote = "Invented" },
		func(r *tool.CitationRef, _ *tool.Call) { r.Reference = "[K-AAAAAAAAAAAA]" },
		func(_ *tool.CitationRef, c *tool.Call) { c.ToolVersion = "7" },
		func(_ *tool.CitationRef, c *tool.Call) { c.Arguments = json.RawMessage(`{"perDocument":false}`) },
		func(_ *tool.CitationRef, c *tool.Call) {
			c.Arguments = json.RawMessage(`{"perDocument":true,"documentIds":["other"]}`)
		},
	} {
		r, c := ref, call
		mutate(&r, &c)
		if _, err := s.verifyCitation(context.Background(), "project", "workflow", "", c, r); err == nil {
			t.Fatal("mismatched evidence accepted")
		}
	}
	ref.Title = chunk.Title
	if _, err := s.verifyCitation(context.Background(), "project", "workflow", "", call, ref); err != nil {
		t.Fatal("canonical new citation rejected", err)
	}
}

func TestUserMaterialCitationRequiresImportedHashAndCurrentTaskScope(t *testing.T) {
	chunk := knowledge.EvidenceChunk{IndexVersionID: "index", DocumentID: "document", AttachmentID: "user-attachment", ChunkID: "chunk", SourceName: "notes.pdf", MIMEType: "application/pdf", Locator: "page:1", Title: "", Content: "The locally supplied result is limited.", SourceStart: 0, SourceEnd: 37}
	ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: chunk.IndexVersionID, DocumentID: chunk.DocumentID, AttachmentID: chunk.AttachmentID, ChunkID: chunk.ChunkID, SourceName: chunk.SourceName, MIMEType: chunk.MIMEType, Locator: chunk.Locator, Quote: chunk.Content, QuoteSHA256: citation.QuoteSHA256(chunk.Content), SourceStart: chunk.SourceStart, SourceEnd: chunk.SourceEnd}
	ref.Reference = citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	material := ImportedMaterial{AttachmentID: ref.AttachmentID, AttachmentSHA256: "expected-sha", Title: "notes.pdf", MaterialOrigin: "user_selected"}
	service := &Service{
		knowledge: taskScopedKnowledgeFixture{chunk: chunk},
		materials: reportUserMaterialLoader{value: attachment.Attachment{ID: ref.AttachmentID, SHA256: "expected-sha"}, allowedTask: "task-a"},
	}
	result, err := service.verifyCitation(context.Background(), "project", "workflow", "task-a", tool.Call{}, ref, map[string]ImportedMaterial{ref.AttachmentID: material})
	if err != nil || result.EvidenceLevel != "" || !strings.Contains(string(result.BibliographySnapshot), `"materialOrigin":"user_selected"`) || !strings.Contains(string(result.BibliographySnapshot), `"workType":"user_material"`) {
		t.Fatalf("user material citation=%#v err=%v", result, err)
	}
	for _, mutate := range []func(*ImportedMaterial, *Service, *tool.CitationRef, *string){
		func(v *ImportedMaterial, _ *Service, _ *tool.CitationRef, _ *string) { v.AttachmentSHA256 = "forged" },
		func(_ *ImportedMaterial, _ *Service, _ *tool.CitationRef, task *string) { *task = "task-b" },
		func(_ *ImportedMaterial, _ *Service, ref *tool.CitationRef, _ *string) {
			ref.AttachmentID = "another-attachment"
		},
	} {
		candidate, citationRef, task := material, ref, "task-a"
		mutate(&candidate, service, &citationRef, &task)
		if _, err := service.verifyCitation(context.Background(), "project", "workflow", task, tool.Call{}, citationRef, map[string]ImportedMaterial{citationRef.AttachmentID: candidate}); err == nil {
			t.Fatal("forged hash or task scope bypass was accepted")
		}
	}
}

func TestPublishReportAcceptsOnlySameRunImportedUserMaterial(t *testing.T) {
	chunk := knowledge.EvidenceChunk{IndexVersionID: "index", DocumentID: "document", AttachmentID: "user-attachment", ChunkID: "chunk", SourceName: "notes.pdf", MIMEType: "application/pdf", Locator: "page:1", Content: "The locally supplied result is limited.", SourceStart: 0, SourceEnd: 37}
	ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: chunk.IndexVersionID, DocumentID: chunk.DocumentID, AttachmentID: chunk.AttachmentID, ChunkID: chunk.ChunkID, SourceName: chunk.SourceName, MIMEType: chunk.MIMEType, Locator: chunk.Locator, Quote: chunk.Content, QuoteSHA256: citation.QuoteSHA256(chunk.Content), SourceStart: chunk.SourceStart, SourceEnd: chunk.SourceEnd}
	ref.Reference = citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	analysis := json.RawMessage(`{"summary":"limited local evidence"}`)
	gate := json.RawMessage(`{"approved":true}`)
	imported, _ := json.Marshal(map[string]any{"materials": []ImportedMaterial{{AttachmentID: ref.AttachmentID, AttachmentSHA256: "expected-sha", Title: "notes.pdf", MaterialOrigin: "user_selected"}}})
	calls := []tool.Call{
		{ID: "import", RunID: "workflow", SubjectKind: tool.SubjectWorkflowRun, ToolName: "builtin.research.workflow.import", Status: tool.CallCompleted, Arguments: json.RawMessage(`{"selectedAttachmentIds":["user-attachment"]}`), Result: &tool.Result{Status: tool.ResultSuccess, Structured: imported}},
		{ID: "search", RunID: "workflow", SubjectKind: tool.SubjectWorkflowRun, ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}},
		{ID: "gate", RunID: "workflow", SubjectKind: tool.SubjectWorkflowRun, ToolName: ReviewGateToolName, Status: tool.CallCompleted, Arguments: json.RawMessage(`{"subject":{"summary":"limited local evidence"}}`), Result: &tool.Result{Status: tool.ResultSuccess, Structured: gate}},
	}
	artifacts := &workflowArtifactsFixture{}
	service := &Service{
		knowledge:    taskScopedKnowledgeFixture{chunk: chunk},
		bibliography: workflowBibliographyFixture{}, // Must not be used for a user-selected material.
		materials:    reportUserMaterialLoader{value: attachment.Attachment{ID: ref.AttachmentID, SHA256: "expected-sha"}, allowedTask: "task-a"},
		toolCalls:    workflowCallsFixture{calls},
		artifacts:    artifacts,
	}
	request := ReportRequest{ProjectID: "project", WorkflowRunID: "workflow", ResearchTaskID: "task-a", ToolCallID: "publish", Name: "local material report", Markdown: "Finding " + ref.Reference, Citations: []tool.CitationRef{ref}, Analysis: analysis, ReviewGate: gate}
	if _, err := service.PublishReport(context.Background(), request); err != nil {
		t.Fatal("same-run imported user material was rejected", err)
	}
	if len(artifacts.command.Citations) != 1 || artifacts.command.Citations[0].EvidenceLevel != "" || !strings.Contains(string(artifacts.command.Citations[0].BibliographySnapshot), `"attachmentSha256":"expected-sha"`) {
		t.Fatalf("user material snapshot was not frozen: %#v", artifacts.command.Citations)
	}
	// The same knowledge citation cannot be promoted merely by a material record
	// from a different Workflow Run.
	calls[0].RunID = "other-run"
	service.toolCalls = workflowCallsFixture{calls}
	if _, err := service.PublishReport(context.Background(), request); err == nil {
		t.Fatal("different-run import authorized a user material citation")
	}
	// A forged tool result cannot authorize an attachment that was not an
	// explicit import input, even when it was returned by this same run.
	calls[0].RunID = "workflow"
	calls[0].Arguments = json.RawMessage(`{"selectedAttachmentIds":[]}`)
	service.toolCalls = workflowCallsFixture{calls}
	if _, err := service.PublishReport(context.Background(), request); err == nil {
		t.Fatal("unselected attachment in import result authorized a user material citation")
	}
}
