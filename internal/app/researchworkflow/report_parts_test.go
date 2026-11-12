package researchworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/reportview"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestPublishReportSeparatedPartsAndTampering(t *testing.T) {
	for _, change := range []string{"none", "gate-key-order", "unrelated-old-gate", "report-body", "report-appendix", "report-summary", "gate-other-run", "gate-failed", "part-hash", "part-order", "part-missing", "version", "trailing-text"} {
		t.Run(change, func(t *testing.T) {
			quote := "Verified fixture evidence."
			chunk := knowledge.EvidenceChunk{IndexVersionID: "index", DocumentID: "doc", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.md", MIMEType: "text/markdown", Locator: "abstract", Title: "Paper", Content: quote, SourceEnd: len(quote)}
			ref := tool.CitationRef{ID: "attachment", Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "doc", AttachmentID: "attachment", ChunkID: "chunk", SourceName: chunk.SourceName, MIMEType: chunk.MIMEType, Locator: chunk.Locator, Title: chunk.Title, Quote: quote, QuoteSHA256: citation.QuoteSHA256(quote), SourceEnd: chunk.SourceEnd}
			ref.Reference = citation.KnowledgeReference("run", "index", "chunk", ref.QuoteSHA256)
			body, appendix := "# 中文报告 🧪\n"+ref.Reference+"\n\n", "## 程序附录\n原始保存记录"
			start := len([]rune(body))
			composition := reportview.Composition{ViewVersion: reportview.PartsVersion, Pointer: "/context/markdown", Sections: []reportview.ManagedSection{{Start: start, End: start + len([]rune(appendix)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(appendix)))}}}
			subject := map[string]any{"context": map[string]any{"markdown": body, "confidence": "low"}, "reportComposition": composition, "reportRecordAppendices": []string{appendix + "\r\n"}}
			draft := map[string]any{"markdown": body + appendix + "\r\n", "confidence": "low"}
			gate := json.RawMessage(`{"approved":true,"confidence":"low"}`)
			switch change {
			case "report-body":
				draft["markdown"] = "Changed. " + draft["markdown"].(string)
			case "report-appendix":
				draft["markdown"] = body + "changed appendix"
			case "report-summary":
				draft["confidence"] = "high"
			case "part-hash":
				composition.Sections[0].SHA256 = "invalid"
			case "part-order":
				composition.Sections[0].Start++
			case "part-missing":
				subject["reportRecordAppendices"] = []string{}
			case "version":
				composition.ViewVersion = "future"
				subject["reportComposition"] = composition
			case "trailing-text":
				subject["reportRecordAppendices"] = []string{appendix + "unreviewed extra text"}
			}
			args, _ := json.Marshal(map[string]any{"subject": subject})
			calls := []tool.Call{
				{ID: "search", RunID: "run", SubjectKind: tool.SubjectWorkflowRun, ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}},
				{ID: "gate", RunID: "run", SubjectKind: tool.SubjectWorkflowRun, ToolName: ReviewGateToolName, Status: tool.CallCompleted, Arguments: args, Result: &tool.Result{Status: tool.ResultSuccess, Structured: gate}},
			}
			if change == "gate-other-run" {
				calls[1].RunID = "other"
			}
			if change == "gate-failed" {
				calls[1].Status = tool.CallFailed
			}
			if change == "gate-key-order" {
				calls[1].Result.Structured = json.RawMessage(`{"confidence":"low","approved":true}`)
			}
			if change == "unrelated-old-gate" {
				calls = append(calls, tool.Call{ID: "old-gate", RunID: "run", SubjectKind: tool.SubjectWorkflowRun, ToolName: ReviewGateToolName, Status: tool.CallCompleted, Arguments: json.RawMessage(`{"subject":{"reportComposition":{"viewVersion":"unknown"}}}`), Result: &tool.Result{Status: tool.ResultSuccess, Structured: json.RawMessage(`{"approved":true,"reviewSha256":"other-review"}`)}})
			}
			writer := &workflowArtifactsFixture{}
			svc, err := New(unusedDiscovery{}, workflowKnowledgeFixture{chunk}, workflowBibliographyFixture{research.CitationSnapshot{BibliographyID: "bib", Bibliography: json.RawMessage(`{"bibliographyId":"bib","revision":1}`), EvidenceLevel: research.EvidenceFullText}}, unusedEnvironment{}, workflowCallsFixture{calls}, writer)
			if err != nil {
				t.Fatal(err)
			}
			analysis, _ := json.Marshal(draft)
			_, err = svc.PublishReport(context.Background(), ReportRequest{ProjectID: "project", WorkflowRunID: "run", ToolCallID: "publication", Name: "Report", Markdown: draft["markdown"].(string), Citations: []tool.CitationRef{ref}, Analysis: analysis, ReviewGate: gate})
			if change == "none" || change == "unrelated-old-gate" || change == "gate-key-order" {
				if err != nil || writer.command.Markdown != strings.TrimSpace(draft["markdown"].(string)) || len(writer.command.Citations) != 1 {
					t.Fatal("exact separated report not published", err)
				}
			} else if err == nil || writer.command.Markdown != "" {
				t.Fatal("altered report reached publisher", err)
			}
		})
	}
}

// Optional replay of the actual incident, with no formal DB writes or model calls.
func TestCapturedPublicationPartsBinding(t *testing.T) {
	dir := os.Getenv("SCIAIDE_GATE_AUDIT_DIR")
	if dir == "" {
		t.Skip("no isolated capture supplied")
	}
	read := func(name string, v any) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	var input map[string]json.RawMessage
	var result tool.Result
	var pub struct {
		Name        string
		Citations   []tool.CitationRef
		ReportDraft json.RawMessage
		ReviewGate  json.RawMessage
	}
	read("delivery_gate-input_json", &input)
	read("delivery_gate-output_json", &result)
	read("report_publication-input_json", &pub)
	args, _ := json.Marshal(input)
	writer := &workflowArtifactsFixture{}
	svc, _ := New(unusedDiscovery{}, workflowKnowledgeFixture{}, workflowBibliographyFixture{}, unusedEnvironment{}, workflowCallsFixture{[]tool.Call{{ID: "gate", RunID: "run", SubjectKind: tool.SubjectWorkflowRun, ToolName: ReviewGateToolName, Status: tool.CallCompleted, Arguments: args, Result: &result}}}, writer)
	request := ReportRequest{ProjectID: "project", WorkflowRunID: "run", ToolCallID: "publication", Name: pub.Name, Markdown: "fixture", Citations: pub.Citations, Analysis: pub.ReportDraft, ReviewGate: pub.ReviewGate}
	_, err := svc.PublishReport(context.Background(), request)
	// Deliberately omit source fixtures: reaching citation verification proves
	// the real gate/draft now match without pretending to export this capture.
	if err == nil || !strings.Contains(err.Error(), "report citation was not returned") {
		t.Fatal("capture still fails gate binding", err)
	}
	var draft map[string]any
	_ = json.Unmarshal(pub.ReportDraft, &draft)
	draft["markdown"] = "changed"
	request.Analysis, _ = json.Marshal(draft)
	_, err = svc.PublishReport(context.Background(), request)
	if !errors.Is(err, ErrReviewReportMismatch) {
		t.Fatal("capture tampering not rejected", err)
	}
	t.Log("captured full report accepted by binding; altered body rejected; no production data or model changed")
}
