package researchworkflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
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
	calls := []tool.Call{{ID: "search-call", RunID: "workflow-run", SubjectKind: tool.SubjectWorkflowRun, ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &result}}
	bibliography, _ := json.Marshal(map[string]any{"schemaVersion": 1, "bibliographyId": "bibliography", "revision": 1, "capturedAt": time.Now().UTC()})
	artifacts := &workflowArtifactsFixture{}
	service, err := New(unusedDiscovery{}, workflowKnowledgeFixture{chunk}, workflowBibliographyFixture{research.CitationSnapshot{BibliographyID: "bibliography", Bibliography: bibliography, EvidenceLevel: research.EvidenceFullText}}, unusedEnvironment{}, workflowCallsFixture{calls}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	request := ReportRequest{ProjectID: "project", WorkflowRunID: "workflow-run", ToolCallID: "report-call", IdempotencyKey: "stable", Name: "Report", Markdown: "# Result\n\nVerified. " + ref.Reference, Citations: []tool.CitationRef{ref}, SourceArtifacts: result.Artifacts}
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
	request.Markdown += " [K-FFFFFFFFFFFF]"
	if _, err := service.PublishReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unverified marker") {
		t.Fatalf("unverified marker error=%v", err)
	}
	request.Markdown = "# Missing marker"
	if _, err := service.PublishReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not cite") {
		t.Fatalf("missing marker error=%v", err)
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
