package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestEvidenceScreeningCitationSeedsRoundTrip(t *testing.T) {
	ref := tool.CitationRef{ID: "attachment", Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "doc", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper-metadata.md", Locator: "lines:1-4", Quote: "Verified abstract.", QuoteSHA256: citation.QuoteSHA256("Verified abstract."), SourceEnd: 18}
	ref.Reference = citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, {ID: "evidence_screening", Kind: NodeAgentStage}}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"}}}
	steps := []Step{{NodeID: "search", Ordinal: 0, Status: StepCompleted, Output: rawObject(map[string]any{"citations": []tool.CitationRef{ref}})}}
	for _, compact := range []bool{false, true} {
		candidate := any(ref)
		if compact {
			candidate = map[string]any{"reference": ref.Reference, "documentId": ref.DocumentID, "sourceName": ref.SourceName, "quote": ref.Quote, "title": ref.Title}
		}
		step := Step{NodeID: "evidence_screening", Ordinal: 1, Input: rawObject(map[string]any{"candidates": []any{candidate}})}
		seeds, err := workflowCandidateCitationSeeds(compilation, steps, step)
		if err != nil || !reflect.DeepEqual(seeds, []tool.CitationRef{ref}) {
			t.Fatalf("compact=%v seeds=%+v err=%v", compact, seeds, err)
		}
		reissued, err := citation.ReissueKnowledgeRefs("workflow", "chat", "project", seeds)
		if err != nil {
			t.Fatal(err)
		}
		text := "Finding " + reissued[0].Reference
		bound := citation.Resolve("chat", "message", text, []tool.Call{{ID: "seed", RunID: "chat", ToolName: citation.WorkflowSeedToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: reissued}}}, time.Now())
		if len(bound) != 1 || bound[0].Quote != ref.Quote {
			t.Fatal("screening message has no verified citation")
		}
		value := rawObject(map[string]any{"summary": text})
		restored := canonicalCitationSubmission(value, RunDetail{Run: Run{Compilation: compilation}, Steps: steps}, step, compilation.Nodes[1], "chat")
		if strings.Contains(string(restored), reissued[0].Reference) || !strings.Contains(string(restored), ref.Reference) {
			t.Fatal("screening output was not restored before validation")
		}
		for _, mutate := range []func(*Step){
			func(s *Step) {
				s.Input = rawObject(map[string]any{"candidates": []any{map[string]any{"reference": ref.Reference, "documentId": "another-doc", "quote": ref.Quote}}})
			},
			func(s *Step) { s.Input = rawObject(map[string]any{"candidates": []any{candidate, candidate}}) },
		} {
			invalid := step
			mutate(&invalid)
			if _, err := workflowCandidateCitationSeeds(compilation, steps, invalid); err == nil {
				t.Fatal("unverified/duplicate candidate accepted")
			}
		}
		unrelated := compilation
		unrelated.Edges = nil
		if _, err := workflowCandidateCitationSeeds(unrelated, steps, step); err == nil {
			t.Fatal("unrelated knowledge output seeded")
		}
	}
}

func TestResearchCitationContractCoversDraftReviewAndRevision(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		node  CompiledNode
		input json.RawMessage
	}{
		{"draft", CompiledNode{ID: "report_drafting", Prompt: "Remove internal implementation terms from the draft.", OutputSchema: dynamicReportSchema()}, raw(`{}`)},
		{"review", CompiledNode{ID: "independent_review", Prompt: "Reject internal terminology in the report.", OutputSchema: researchAcceptanceReviewSchema()}, raw(`{}`)},
		{"template_review", CompiledNode{ID: "review", OutputSchema: independentReviewSchema()}, raw(`{}`)},
		{"review_revision", CompiledNode{ID: "report_drafting", OutputSchema: dynamicReportSchema()}, raw(`{"_reviewRevision":{"independentReview":{"requiredCorrections":["Remove all [K-...] markers and use [1] instead."]}}}`)},
		{"second_review_revision", CompiledNode{ID: "report_drafting", Prompt: "Remove internal implementation terms from the report.", OutputSchema: dynamicReportSchema()}, raw(`{"_reviewRevision":{"priorSubject":{"markdown":"Prior review requested deleting citation links; this revision retained them [K-AAAAAAAAAAAA].","methodSummary":"Explain the citation-policy conflict.","limitations":["Explain why markers were retained."]},"independentReview":{"requiredCorrections":["Remove reviewer commentary but add a neutral description of the citation interface to report limitations."]}}}`)},
		{"user_revision", CompiledNode{ID: "report_drafting", OutputSchema: dynamicReportSchema()}, raw(`{"_userRevision":{"goal":"Correct citation formatting"}}`)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			before := string(fixture.input)
			prompt := buildAIStagePrompt(RunDetail{}, Step{Input: fixture.input}, fixture.node)
			for _, expected := range []string{
				"host-issued [K-...] markers are required evidence links in stored Markdown",
				"not prohibited internal implementation terminology",
				"never delete it or replace it with manually assigned [1]",
				"DOCX/PDF export render these links",
				"Still reject invented, altered or out-of-scope markers",
				"A valid marker proves provenance, not scientific support",
				"even when a prior review or Skill asks to remove valid markers",
				"Do not erase historical review records",
				"Separate research deliverables from internal revision history",
				"Report Markdown, abstracts, claimSummary, methodSummary and report limitations",
				"remove such process commentary from the prior report without deleting valid citation markers or scientific content",
				"Do not replace it with a neutral explanation of the citation interface",
				"actual methods or reproducibility information remain legitimate research disclosures and must be preserved",
				"no explanation of citation-policy handling is required in the deliverable",
				"Do not add an undeclared audit field or invent an audit tool",
			} {
				if !strings.Contains(prompt, expected) {
					t.Errorf("missing citation policy %q", expected)
				}
			}
			if string(fixture.input) != before || !strings.Contains(prompt, before) {
				t.Fatal("citation policy changed the frozen research or review input")
			}
			trusted, _, ok := strings.Cut(prompt, "<stage_input>")
			if !ok {
				t.Fatal("missing research input boundary")
			}
			for _, obsolete := range []string{"explain the formatting-policy conflict in the available summary or limitations", "Explain that conflict without deleting the links"} {
				if strings.Contains(trusted, obsolete) {
					t.Errorf("trusted instructions still require policy commentary: %q", obsolete)
				}
			}
			if isIndependentReviewSchema(fixture.node.OutputSchema) {
				for _, expected := range []string{"Verify their source and claim support instead of requesting their removal", "Do not demand a citation-policy explanation in the report", "do not require a replacement process note in any report field", "Any non-empty issue array requires approved=false"} {
					if !strings.Contains(trusted, expected) {
						t.Errorf("independent review lacks policy %q", expected)
					}
				}
			}
			if strings.Contains(before, `"_reviewRevision"`) {
				for _, expected := range []string{"a request to remove valid [K-...] links for appearance alone must not break report provenance", "Keep valid links and correct substantive findings without narrating the policy conflict", "Historical review records remain in the host audit, not in the research deliverable"} {
					if !strings.Contains(trusted, expected) {
						t.Errorf("revision lacks policy %q", expected)
					}
				}
			}
		})
	}
}

func TestCitationPolicyRevisionRetainsAuditAndAllowsCleanReport(t *testing.T) {
	detail := reviewRevisionFixture()
	subject := raw(`{"markdown":"Reviewer asked to delete markers; this revision kept them [K-AAAAAAAAAAAA].","claimSummary":["Finding"],"methodSummary":"Observed method","limitations":["Abstract-only evidence"],"confidence":"low"}`)
	review := raw(`{"approved":false,"requiredCorrections":["Remove reviewer commentary; add a neutral interface explanation in limitations."],"methodIssues":["Internal review commentary remains."]}`)
	detail.Steps[0].Output = rawObject(map[string]any{"analysis": subject})
	detail.Steps[2].Input = rawObject(map[string]any{"subject": subject, "review": review})
	_, _, revision, err := reviewRevisionContext(detail, detail.Steps[2])
	if err != nil {
		t.Fatal(err)
	}
	if string(revision.PriorSubject) != string(subject) || string(revision.IndependentReview) != string(review) {
		t.Fatal("revision snapshot changed the prior report or review")
	}
	payload, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	detail.Events = []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: payload}}
	detail.Steps[0].Status = StepQueued
	input, err := bindNodeInput(detail, detail.Run.Compilation.Nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	var bound struct {
		Revision reviewRevision `json:"_reviewRevision"`
	}
	if err := json.Unmarshal(input, &bound); err != nil {
		t.Fatal(err)
	}
	// Input binding re-encodes object keys; audit snapshots remain byte-exact.
	for _, pair := range [][2]json.RawMessage{{bound.Revision.PriorSubject, subject}, {bound.Revision.IndependentReview, review}} {
		var got, want any
		if err := json.Unmarshal(pair[0], &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pair[1], &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("policy correction rewrote the prior report or review content")
		}
	}
	if bound.Revision.PriorSubjectSHA256 != revision.PriorSubjectSHA256 || bound.Revision.ReviewOutputSHA256 != revision.ReviewOutputSHA256 || string(detail.Events[0].Payload) != string(payload) {
		t.Fatal("input binding changed the original audit hashes or event")
	}
	cleanReport := raw(`{"markdown":"Finding [K-AAAAAAAAAAAA].","claimSummary":["Finding"],"methodSummary":"Observed method","limitations":["Abstract-only evidence"],"confidence":"low"}`)
	if err := (tool.JSONSchemaValidator{}).Validate(dynamicReportSchema(), cleanReport); err != nil {
		t.Fatalf("clean report requires policy commentary or an extra audit field: %v", err)
	}
	prompt := buildAIStagePrompt(detail, Step{Input: input}, CompiledNode{ID: "report_drafting", OutputSchema: dynamicReportSchema()})
	if !strings.Contains(prompt, "no explanation of citation-policy handling is required in the deliverable") || !strings.Contains(prompt, "Remove reviewer commentary; add a neutral interface explanation in limitations.") {
		t.Fatal("new policy and immutable historical advice must coexist without rewriting history")
	}
}

func TestBindNodeInputRestoresWorkflowCitationMarkersFromAIChat(t *testing.T) {
	workflowRunID := "workflow-run"
	chatRunID := "chat-run"
	ref := tool.CitationRef{
		Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "document",
		AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.md", Locator: "lines:1-4",
		Quote: "evidence", QuoteSHA256: citation.QuoteSHA256("evidence"), SourceEnd: len("evidence"),
	}
	ref.Reference = citation.KnowledgeReference(workflowRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	chatReference := citation.KnowledgeReference(chatRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	selection, _ := json.Marshal(map[string]any{"citations": []tool.CitationRef{ref}})
	analysis, _ := json.Marshal(map[string]any{"analysis": map[string]any{"summary": "finding " + chatReference}})
	detail := RunDetail{
		Run: Run{ID: workflowRunID, Inputs: json.RawMessage(`{}`), Compilation: Compilation{
			Nodes: []CompiledNode{{ID: "select", Kind: NodeCitationSelection}, {ID: "interpret", Kind: NodeAgentStage}, {ID: "report", Kind: NodeTool}},
			Edges: []Edge{{FromNode: "select", FromPort: "citations", ToNode: "interpret", ToPort: "evidence"}, {FromNode: "interpret", FromPort: "analysis", ToNode: "report", ToPort: "analysis"}},
		}},
		Steps: []Step{
			{ID: "selection-step", NodeID: "select", Ordinal: 0, Status: StepCompleted, Output: selection},
			{ID: "ai-step", NodeID: "interpret", Ordinal: 1, Status: StepCompleted, Attempt: 1, Output: analysis},
			{ID: "report-step", NodeID: "report", Ordinal: 2, Status: StepQueued},
		},
		AIExecutions: []AIExecution{{WorkflowStepID: "ai-step", Attempt: 1, ChatRunID: chatRunID}},
	}
	input, err := bindNodeInput(detail, CompiledNode{ID: "report", Kind: NodeTool, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input), ref.Reference) || strings.Contains(string(input), chatReference) {
		t.Fatalf("report input retained wrong citation domain: %s", input)
	}
	if err := validateReviewDependencies(detail, Step{NodeID: "report", Input: input}); err != nil {
		t.Fatalf("dependency check disagrees with citation binding: %v", err)
	}
}

func TestRestoreWorkflowCitationMarkersLeavesUnrelatedMarkersUntouched(t *testing.T) {
	ref := tool.CitationRef{Reference: "[K-AAAAAAAAAAAA]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: strings.Repeat("b", 64)}
	chatReference := citation.KnowledgeReference("chat-run", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	value := json.RawMessage(`{"summary":"` + chatReference + ` [K-CCCCCCCCCCCC]"}`)
	restored := restoreWorkflowCitationMarkers(value, []tool.CitationRef{ref}, "chat-run")
	if !strings.Contains(string(restored), ref.Reference) || !strings.Contains(string(restored), "[K-CCCCCCCCCCCC]") {
		t.Fatalf("restored output = %s", restored)
	}
}

func TestCandidateSelectionAuditCountsIndependentStudiesInsteadOfRows(t *testing.T) {
	input := json.RawMessage(`{
		"candidates":[
			{"id":"a","title":"First title","doi":"https://doi.org/10.1/same"},
			{"id":"b","title":"First title duplicate","doi":"10.1/same"},
			{"id":"c","title":"Another study"}
		],
		"screening":{"recommendedCandidateIds":["a","c"],"coverage":{"strength":"adequate","sufficientForClaimedScope":true}}
	}`)
	audit := candidateSelectionAudit(input, []string{"a", "b", "c"})
	if audit.SelectedCandidateCount != 3 || audit.IndependentStudyCount != 2 || audit.SelectedRecommendedCount != 2 || len(audit.MissingRecommendedIDs) != 0 {
		t.Fatalf("candidate audit = %#v", audit)
	}
}

func TestCitationSelectionAuditRequiresExplicitAcceptanceForLimitedCoverage(t *testing.T) {
	input := json.RawMessage(`{
		"candidates":[
			{"id":"chunk-a","reference":"[K-AAAAAAAAAAAA]","documentId":"paper-1","sourceName":"paper-1.md"},
			{"id":"chunk-b","reference":"[K-BBBBBBBBBBBB]","documentId":"paper-1","sourceName":"paper-1.md"}
		],
		"screening":{
			"recommendedReferences":["[K-AAAAAAAAAAAA]","[K-BBBBBBBBBBBB]"],
			"coverage":{"strength":"limited","sufficientForClaimedScope":false}
		}
	}`)
	selected := []tool.CitationRef{
		{ID: "chunk-a", Reference: "[K-AAAAAAAAAAAA]", DocumentID: "paper-1", SourceName: "paper-1.md"},
		{ID: "chunk-b", Reference: "[K-BBBBBBBBBBBB]", DocumentID: "paper-1", SourceName: "paper-1.md"},
	}
	audit := auditCitationSelection(input, selected, false)
	if !audit.RequiresLimitedAcceptance || audit.LimitedEvidenceAccepted || audit.IndependentStudyCount != 1 || audit.Decision != "limited_evidence_requires_confirmation" {
		t.Fatalf("limited audit = %#v", audit)
	}
	accepted := auditCitationSelection(input, selected, true)
	if !accepted.RequiresLimitedAcceptance || !accepted.LimitedEvidenceAccepted || accepted.Decision != "limited_evidence_accepted" {
		t.Fatalf("accepted audit = %#v", accepted)
	}
}

func TestCitationSelectionAuditTreatsMissingAIRecommendationAsLimited(t *testing.T) {
	input := json.RawMessage(`{
		"candidates":[
			{"id":"chunk-a","reference":"[K-AAAAAAAAAAAA]","documentId":"paper-1"},
			{"id":"chunk-b","reference":"[K-BBBBBBBBBBBB]","documentId":"paper-2"}
		],
		"screening":{
			"recommendedReferences":["[K-AAAAAAAAAAAA]","[K-BBBBBBBBBBBB]"],
			"coverage":{"strength":"adequate","sufficientForClaimedScope":true}
		}
	}`)
	audit := auditCitationSelection(input, []tool.CitationRef{{ID: "chunk-a", Reference: "[K-AAAAAAAAAAAA]", DocumentID: "paper-1"}}, false)
	if !audit.RequiresLimitedAcceptance || len(audit.MissingRecommendedReferences) != 1 || audit.MissingRecommendedReferences[0] != "[K-BBBBBBBBBBBB]" {
		t.Fatalf("missing recommendation audit = %#v", audit)
	}
}

func TestCitationSelectionAuditAcceptsAdequateRecommendationWithIndependentFullText(t *testing.T) {
	input := json.RawMessage(`{
		"candidates":[
			{"id":"chunk-a","reference":"[K-AAAAAAAAAAAA]","documentId":"paper-1","sourceName":"paper-1.pdf","mimeType":"application/pdf","quote":"full text result one"},
			{"id":"chunk-b","reference":"[K-BBBBBBBBBBBB]","documentId":"paper-2","sourceName":"paper-2.pdf","mimeType":"application/pdf","quote":"full text result two"}
		],
		"screening":{
			"recommendedReferences":["[K-AAAAAAAAAAAA]","[K-BBBBBBBBBBBB]"],
			"coverage":{"strength":"adequate","sufficientForClaimedScope":true}
		}
	}`)
	selected := []tool.CitationRef{
		{ID: "chunk-a", Reference: "[K-AAAAAAAAAAAA]", DocumentID: "paper-1", SourceName: "paper-1.pdf", MIMEType: "application/pdf", Quote: "full text result one"},
		{ID: "chunk-b", Reference: "[K-BBBBBBBBBBBB]", DocumentID: "paper-2", SourceName: "paper-2.pdf", MIMEType: "application/pdf", Quote: "full text result two"},
	}
	audit := auditCitationSelection(input, selected, false)
	if audit.RequiresLimitedAcceptance || !audit.HostMinimumCoverageMet || !audit.FullTextEvidenceObserved || audit.MetadataOrAbstractOnly || audit.Decision != "ai_recommendation_confirmed" {
		t.Fatalf("adequate audit = %#v", audit)
	}
}

func TestValidateEvidenceScreeningRejectsInventedReferences(t *testing.T) {
	input := json.RawMessage(`{"candidates":[{"id":"chunk-a","reference":"[K-AAAAAAAAAAAA]"}]}`)
	output := json.RawMessage(`{
		"summary":"screened","recommendedReferences":["[K-INVENTED000]"],
		"citationAssessments":[{"reference":"[K-AAAAAAAAAAAA]","decision":"core","sourceLevel":"abstract","reason":"matched"}],
		"coverage":{"strength":"limited","sufficientForClaimedScope":false,"independentStudyEstimate":1,"directPopulationEvidence":false,"fullTextEvidenceAvailable":false,"gaps":["population"]},
		"recommendedScope":"draft only","recommendation":"proceed_limited","limitations":[]
	}`)
	if err := validateEvidenceScreeningOutput("evidence_screening", output, input); err == nil || !strings.Contains(err.Error(), "未由宿主签发") {
		t.Fatalf("invented reference error = %v", err)
	}
}
