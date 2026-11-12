package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func evidenceNavigationFixture() (RunDetail, Step) {
	input := raw(`{"evidenceContext":[{"documentId":"doc-a","attachmentId":"att-a","quote":"Lin; DOI 1418. No energy result."},{"documentId":"doc-a","attachmentId":"att-a","quote":"Lin trial results"}],"researchSourceContext":{"bibliography":[{"id":"similar","title":"Similar trial","doi":"547"},{"id":"lin","title":"Actual Lin trial","doi":"1418"}],"indexedMaterials":{"documents":[{"id":"doc-a","attachmentId":"att-a","title":"research-similar-metadata.md"}]}}}`)
	steps := []Step{
		{ID: "screen", NodeID: "candidate_screening", Ordinal: 1, Attempt: 2, Status: StepCompleted, Output: raw(`{"analysis":{"candidateAssessments":[{"candidateId":"lin","decision":"core"}]},"evidenceManifest":[{"candidateId":"lin","decision":"core","reason":"abstract"},{"candidateId":"similar","decision":"exclude","reason":"wrong population"},{"candidateId":"other","decision":"support","reason":"background"}]}`)},
		{ID: "choice", NodeID: "candidate_review", Ordinal: 2, Attempt: 1, Status: StepCompleted, Output: raw(`{"selectedCandidateIds":["lin"]}`)},
		{ID: "import", NodeID: "evidence_import", Ordinal: 3, Attempt: 2, Status: StepCompleted, Output: raw(`{"structured":{"materials":[{"candidateId":"lin","attachmentId":"att-a"}]}}`)},
	}
	return RunDetail{Steps: steps}, Step{NodeID: "independent_review", Ordinal: 8, Input: input, InputSHA256: hashJSON(input)}
}

func TestEvidenceNavigationBindsExactImportNotFilenameOrSimilarTitle(t *testing.T) {
	detail, step := evidenceNavigationFixture()
	before, frozen := string(mustJSON(detail)), string(mustJSON(step))
	index := buildResearchEvidenceNavigation(detail, step)
	if len(index.Citations) != 2 {
		t.Fatal(index)
	}
	for i, citation := range index.Citations {
		if citation.BindingStatus != "matched" || citation.CandidateID != "lin" || citation.BibliographyPointer != "/researchSourceContext/bibliography/1" || citation.ImportPointer != "/structured/materials/0" || index.MaterialImports.OutputSHA256 != hashJSON(detail.Steps[2].Output) || citation.QuotePointer == "" {
			t.Fatal(i, citation)
		}
	}
	if index.Citations[0].QuotePointer == index.Citations[1].QuotePointer {
		t.Fatal("multiple excerpts were collapsed into one")
	}
	if index.ScreeningRecords.RecordCount != 3 || index.ScreeningRecords.DecisionCounts["exclude"] != 1 || index.ScreeningRecords.RecordLocation.Pointer != "/evidenceManifest" || index.ScreeningRecords.RecordLocation.Attempt != 2 || index.CandidateDecision.NodeID != "candidate_review" {
		t.Fatal("retained candidate display lost excluded operations", index)
	}
	if before != string(mustJSON(detail)) || frozen != string(mustJSON(step)) {
		t.Fatal("navigation mutated frozen records")
	}
	compact := mustJSON(compactResearchEvidenceNavigation(index))
	if strings.Contains(string(compact), `"pointer"`) || !strings.Contains(string(compact), `"section":"stage_output"`) {
		t.Fatal("executing navigation exposed a cross-stage pointer", string(compact))
	}
	var navigation struct {
		Roots    map[string]string `json:"roots"`
		Bindings []struct {
			Evidence     int    `json:"evidence"`
			Document     *int   `json:"document"`
			Bibliography *int   `json:"bibliography"`
			Material     *int   `json:"material"`
			Status       string `json:"status"`
		} `json:"bindings"`
	}
	if json.Unmarshal(compact, &navigation) != nil || len(navigation.Bindings) != 2 || navigation.Roots["bibliography"] != "/researchSourceContext/bibliography" {
		t.Fatal("compact index lost record roots", string(compact))
	}
	for i, binding := range navigation.Bindings {
		if binding.Evidence != i || binding.Document == nil || *binding.Document != 0 || binding.Bibliography == nil || *binding.Bibliography != 1 || binding.Material == nil || *binding.Material != 0 || binding.Status != "matched" {
			t.Fatal("zero-based compact binding changed original pointers", binding)
		}
	}
	if !strings.Contains(string(compactResearchEvidenceNavigationJSONEmpty()), "\"document\":null") {
		t.Fatal("missing identity incorrectly assigned index zero")
	}
}

func compactResearchEvidenceNavigationJSONEmpty() json.RawMessage {
	return mustJSON(compactResearchEvidenceNavigation(researchEvidenceNavigation{Citations: []researchCitationNavigation{{EvidencePointer: "/evidenceContext/0", BindingStatus: "missing_import"}}}))
}

func TestEvidenceNavigationMissingConflictingAndAmbiguousBindingsStayExplicit(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		change       func(*RunDetail, *Step)
	}{
		{"missing_import", "missing_import", func(d *RunDetail, _ *Step) { d.Steps[2].Status = StepQueued }},
		{"future_import", "missing_import", func(d *RunDetail, s *Step) { d.Steps[2].Ordinal = s.Ordinal + 1 }},
		{"ambiguous_import", "ambiguous_import", func(d *RunDetail, _ *Step) {
			d.Steps[2].Output = raw(`{"structured":{"materials":[{"candidateId":"lin","attachmentId":"att-a"},{"candidateId":"similar","attachmentId":"att-a"}]}}`)
		}},
		{"conflicting_identity", "conflicting_identity", func(_ *RunDetail, s *Step) {
			s.Input = raw(`{"evidenceContext":[{"documentId":"doc-a","attachmentId":"att-other"}],"researchSourceContext":{"indexedMaterials":{"documents":[{"id":"doc-a","attachmentId":"att-a"}]}}}`)
		}},
		{"missing_document", "missing_indexed_document", func(_ *RunDetail, s *Step) {
			s.Input = raw(`{"evidenceContext":[{"documentId":"missing","attachmentId":"att-a"}],"researchSourceContext":{"bibliography":[{"id":"lin"}]}}`)
		}},
		{"missing_bibliography", "missing_bibliography", func(_ *RunDetail, s *Step) {
			s.Input = raw(`{"evidenceContext":[{"attachmentId":"att-a"}]}`)
		}},
		{"ambiguous_bibliography", "ambiguous_bibliography", func(_ *RunDetail, s *Step) {
			s.Input = raw(`{"evidenceContext":[{"attachmentId":"att-a"}],"researchSourceContext":{"bibliography":[{"id":"lin"},{"id":"lin"}]}}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, s := evidenceNavigationFixture()
			tc.change(&d, &s)
			entry := buildResearchEvidenceNavigation(d, s).Citations[0]
			if entry.BindingStatus != tc.status || entry.BibliographyPointer != "" {
				t.Fatal("invented metadata or binding", entry)
			}
		})
	}
	// An assessment display is not the complete manifest. Never invent counts
	// for unrecorded exclusions, deduplicated trials or a complete search.
	d, s := evidenceNavigationFixture()
	d.Steps[0].Output = raw(`{"analysis":{"candidateAssessments":[{"candidateId":"lin","decision":"core"}]}}`)
	if buildResearchEvidenceNavigation(d, s).ScreeningRecords != nil {
		t.Fatal("inferred full screening from a partial display")
	}
}

func TestEvidenceNavigationOffsetsReadExactRecordedFieldThroughExistingPages(t *testing.T) {
	for _, value := range []json.RawMessage{
		raw(`{"evidenceManifest":[{"decision":"exclude","reason":"中文排除: ⚗️"}],"text":"other"}`),
		raw("{\n  \"text\": \"中文 ⚗️ " + strings.Repeat("研", 25000) + "\",\n  \"evidenceManifest\": [{\"decision\": \"exclude\", \"reason\": \"真实记录\"}],\n  \"other\": []\n}"),
	} {
		location := recordLocation(Step{Output: value}, "/evidenceManifest")
		runes := []rune(string(value))
		if location.Offset <= 0 || location.EndOffset <= location.Offset || location.EndOffset > len(runes) {
			t.Fatal("invalid character offsets", location)
		}
		var manifest json.RawMessage
		if err := json.Unmarshal([]byte(string(runes[location.Offset:location.EndOffset])), &manifest); err != nil {
			t.Fatal("offsets split JSON/Unicode", err)
		}
		var root map[string]json.RawMessage
		_ = json.Unmarshal(value, &root)
		if !bytes.Equal(manifest, root["evidenceManifest"]) {
			t.Fatal("offsets point to a remarshal instead of exact original field")
		}
		// Exercise the actual existing paginated tool representation, including
		// its character offsets and unchanged whole-record digest.
		var collected strings.Builder
		for offset := location.Offset; offset < location.EndOffset; {
			var page struct {
				Content    string `json:"content"`
				NextOffset int    `json:"nextOffset"`
				SHA256     string `json:"sha256"`
			}
			_ = json.Unmarshal(researchDiscussionPage(value, ResearchTaskReadCommand{Section: "stage_output", NodeID: "candidate_screening", Offset: offset}), &page)
			if page.SHA256 != location.OutputSHA256 || page.NextOffset <= offset {
				t.Fatal("page doesn't match navigated original", page)
			}
			part := []rune(page.Content)
			collected.WriteString(string(part[:min(len(part), location.EndOffset-offset)]))
			offset = page.NextOffset
		}
		if collected.String() != string(manifest) {
			t.Fatal("existing task reader could not retrieve exact manifest")
		}
	}
	for _, value := range []json.RawMessage{raw(`{}`), raw(`[]`), raw(`not-json`)} {
		if start, end := researchOutputFieldOffsets(value, "missing"); start != 0 || end != 0 {
			t.Fatal("invented offsets")
		}
	}
}

func TestEvidenceNavigationUsesActualScopedTaskReaderForSavedExclusions(t *testing.T) {
	detail, step := evidenceNavigationFixture()
	service, repository := stageMemoryService()
	// A long unrelated analysis precedes the manifest. The original tool can
	// jump directly to the host-located field, preserving its exact source hash.
	detail.Steps[0].Output = rawObject(map[string]any{"analysis": map[string]any{"summary": strings.Repeat("无关长段😀", 9000)}, "evidenceManifest": []map[string]string{{"candidateId": "excluded", "decision": "exclude", "reason": "当前人群不符"}}})
	current := repository.detail.Steps[0]
	current.Ordinal, repository.detail.Run.CurrentStep = 8, 8
	repository.detail.Steps = append(detail.Steps, current)
	index := buildResearchEvidenceNavigation(detail, step)
	location := index.ScreeningRecords.RecordLocation
	value, err := service.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_output", NodeID: location.NodeID, Offset: location.Offset})
	if err != nil {
		t.Fatal("existing authorized task reader could not follow navigation", err)
	}
	var page struct {
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
	}
	if json.Unmarshal(value, &page) != nil || page.SHA256 != location.OutputSHA256 || !strings.Contains(page.Content, "当前人群不符") || strings.Contains(page.Content, "无关長段") || strings.Contains(page.Content, "无关长段") {
		t.Fatal("navigation read wrong source or discarded exclusions", string(value))
	}
	if _, err := service.ReadResearchTask(context.Background(), "foreign-chat", ResearchTaskReadCommand{Section: "stage_output", NodeID: location.NodeID, Offset: location.Offset}); err == nil {
		t.Fatal("index granted another task access")
	}
}

func TestUnknownTrialIndependenceDoesNotForceHostRejectionOrHideTrueDefects(t *testing.T) {
	base, output := acceptanceFixture(t)
	input := decodeObject(base)
	input["context"] = map[string]any{"markdown": "综述仅作背景，不与原始试验叠加；文档数不是独立试验数，重叠尚未核实。"}
	input["evidenceSelectionAudit"] = map[string]any{"independentStudyCountVerified": false, "distinctDocumentCount": 7, "limitedEvidenceAccepted": true}
	input["reviewHistory"] = reviewHistory{IssuePrefix: "issue-1-", Findings: []trackedReviewFinding{}, VerifiedClaims: []string{}}
	stage := mustJSON(input)
	output["reviewedInputSha256"], output["reviewFindings"], output["suggestions"], output["revisionPlan"] = hashJSON(stage), []trackedReviewFinding{}, []string{}, []researchRevisionPlanItem{}
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	if err := validateWorkflowAIStageOutput(node, mustJSON(output), stage); err != nil {
		t.Fatal("host automatically treats its conservative flag as failure", err)
	}
	// A model's actual detection of duplicate pooling remains open/rejected;
	// new interpretation does not delete defects or force approval.
	input["context"] = map[string]any{"markdown": "将综述和同一原始试验相加，作为两个独立试验合并。"}
	stage = mustJSON(input)
	issue := trackedReviewFinding{ID: "issue-1-1", Status: "open", Category: "methodIssues", Summary: "重复计算原始试验", Location: "综合", Basis: []reviewBasis{{"/context/markdown", "作为两个独立试验合并"}}, Reason: "同一试验通过综述与原始报告重复进入合并", Origin: "new", ChangeReason: "首次发现实际重复计数", CorrectionIndex: 0}
	output["approved"], output["reviewedInputSha256"], output["methodIssues"], output["requiredCorrections"] = false, hashJSON(stage), []string{issue.Summary}, []string{"去除实际重复计数"}
	output["reviewFindings"], output["revisionPlan"] = []trackedReviewFinding{issue}, []researchRevisionPlanItem{{CorrectionIndex: 0, NodeID: "", Reason: "无可用修正阶段", WhyNotLater: "本夹具无返修目标", NeedsUserInput: true, RequiredInput: "提供更正结果"}}
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "not_met", "issue-1-1"}, {"criterion-2", "met", "已有计划"}}
	if err := validateWorkflowAIStageOutput(node, mustJSON(output), stage); err != nil {
		t.Fatal("real model rejection not saved", err)
	}
	output["approved"] = true
	if validateWorkflowAIStageOutput(node, mustJSON(output), stage) == nil {
		t.Fatal("guidance bypassed real defects")
	}
}

func TestEvidenceScopeCanWithdrawPriorOverreachWithoutDroppingHistory(t *testing.T) {
	input, output := trackingFixture()
	root := decodeObject(input)
	prior := output["reviewFindings"].([]trackedReviewFinding)[0]
	prior.Category, prior.Summary = "methodIssues", "重叠未知必须判失败"
	root["context"] = map[string]any{"markdown": "综述只作背景、不合并或叠加计数；重叠尚未核实。"}
	root["evidenceSelectionAudit"] = map[string]any{"independentStudyCountVerified": false}
	root["reviewHistory"] = reviewHistory{IssuePrefix: "issue-2-", Findings: []trackedReviewFinding{prior}}
	current := prior
	current.Status, current.Origin, current.CorrectionIndex = "withdrawn", "existing", -1
	current.Basis = []reviewBasis{{"/context/markdown", "不合并或叠加计数"}}
	current.ChangeReason = "原意见仅凭宿主未核验推断重复计数，当前范围仅要求避免重复计数"
	output["reviewFindings"], output["approved"], output["numericIssues"], output["requiredCorrections"] = []trackedReviewFinding{current}, true, []string{}, []string{}
	if err := validateTrackedReview(mustJSON(output), mustJSON(root)); err != nil {
		t.Fatal("substantiated withdrawal cannot be recorded", err)
	}
	output["reviewFindings"] = []trackedReviewFinding{}
	if validateTrackedReview(mustJSON(output), mustJSON(root)) == nil {
		t.Fatal("previous opinion disappeared instead of being reassessed")
	}
}

func TestEvidenceInterpretationInWritingMethodAndReviewWithoutNewProtocol(t *testing.T) {
	d, s := evidenceNavigationFixture()
	for _, node := range []CompiledNode{
		{ID: "method_selection", OutputSchema: dynamicMethodSchema()},
		{ID: "report_drafting", PromptVersion: researchReportVersion, OutputSchema: dynamicReportSchema()},
		{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()},
	} {
		frozenInput, schema := string(s.Input), string(node.OutputSchema)
		prompt := buildAIStagePrompt(d, s, node)
		for _, want := range []string{researchEvidenceNavigationVersion, "不能单凭此值判重复计数", "会议摘要或可能同源试验不等于同一书目", "逐领域有限判断", "包含已有排除", "不为补报告重复运行相同方法"} {
			if !strings.Contains(prompt, want) {
				t.Error(node.ID, "missing interpretation", want)
			}
		}
		if schema != string(node.OutputSchema) || frozenInput != string(s.Input) || !strings.Contains(prompt, "<stage_input>\n"+frozenInput+"\n</stage_input>") {
			t.Fatal("changed scientific input or output protocol")
		}
	}
	for _, node := range []CompiledNode{{ID: "method_implementation"}, {ID: "explore"}, {ID: "arbitrary"}} {
		if researchEvidenceNavigationPrompt(d, s, node) != "" {
			t.Fatal("unrelated stage got evidence guidance", node.ID)
		}
	}
	if researchEvidenceNavigationPrompt(d, Step{Input: raw(`{"context":{}}`)}, CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}) != "" {
		t.Fatal("non-literature review got literature checklist")
	}
}

func TestScopeGuidanceDoesNotOverrideModelRepairDestination(t *testing.T) {
	for _, upstreamError := range []bool{false, true} {
		detail := recommendationFixture(t)
		mutateRecommendationReview(t, &detail, func(out map[string]any) {
			out["requiredCorrections"] = []string{"Present the missing assessment"}
			item := researchRevisionPlanItem{CorrectionIndex: 0, NodeID: "report_drafting", Reason: "Existing adequate method; missing report assessment", WhyNotLater: "Report is the last producer"}
			if upstreamError {
				item.NodeID, item.Reason, item.WhyNotLater = "method_selection", "Current method specifies an unsuitable assessment", "Fixing report prose leaves the reusable wrong method"
			}
			out["revisionPlan"] = []researchRevisionPlanItem{item}
		})
		result := researchRevisionRecommendation(detail)
		wantedNode, wantedStages := "report_drafting", 3
		if upstreamError {
			wantedNode, wantedStages = "method_selection", 6
		}
		if result == nil || result.NodeID != wantedNode || len(result.AffectedStages) != wantedStages || result.RepeatsSideEffects != upstreamError {
			t.Fatal("host changed the model's actual repair plan", upstreamError, result)
		}
	}
}

// Optional replay of the user's task-scoped capture. Does not access the app
// DB or a model. Raw SQL strings preserve the original hashes/escaping.
func TestCapturedEvidenceNavigationPreservesFrozenReviewAndRecommendation(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVISION_OPTIMIZATION_REPLAY")
	if dir == "" {
		t.Skip("no task-scoped capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "detail-raw.json"))
	if err != nil {
		t.Fatal(err)
	}
	var detail RunDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID, Input, Output string
	}
	data, err = os.ReadFile(filepath.Join(dir, "step-bytes.json"))
	if err != nil || json.Unmarshal(data, &rows) != nil {
		t.Fatal("missing raw step strings", err)
	}
	for _, row := range rows {
		for i := range detail.Steps {
			if detail.Steps[i].ID == row.ID {
				detail.Steps[i].Input, detail.Steps[i].Output = json.RawMessage(row.Input), json.RawMessage(row.Output)
			}
		}
	}
	var executions []struct{ ID, Output string }
	data, err = os.ReadFile(filepath.Join(dir, "execution-bytes.json"))
	if err != nil || json.Unmarshal(data, &executions) != nil {
		t.Fatal("missing raw execution strings", err)
	}
	for _, row := range executions {
		for i := range detail.AIExecutions {
			if detail.AIExecutions[i].ID == row.ID {
				detail.AIExecutions[i].Output = json.RawMessage(row.Output)
			}
		}
	}
	s := findStepByNode(detail.Steps, "independent_review")
	if s == nil || hashJSON(s.Input) != s.InputSHA256 {
		t.Fatal("raw frozen review input/hash changed")
	}
	node := compilationNodeMap(detail.Run.Compilation)[s.NodeID]
	before := mustJSON(detail)
	recommendation := researchRevisionRecommendation(detail)
	index := buildResearchEvidenceNavigation(detail, *s)
	if len(index.Citations) != 10 || index.ScreeningRecords == nil || index.ScreeningRecords.RecordCount != 67 || index.ScreeningRecords.DecisionCounts["exclude"] != 13 {
		t.Fatalf("actual exclusions/excerpts lost: %+v", index)
	}
	actualConference := false
	for _, c := range index.Citations {
		if c.BindingStatus != "matched" {
			t.Fatal("actual import not bound", c)
		}
		var i int
		if _, err := fmt.Sscanf(c.EvidencePointer, "/evidenceContext/%d", &i); err != nil {
			t.Fatal(err)
		}
		input := decodeObject(s.Input)
		excerpts := input["evidenceContext"].([]any)
		excerpt := excerpts[i].(map[string]any)
		if excerpt["reference"] == "[K-EC9E79D69246]" {
			value := input["researchSourceContext"].(map[string]any)["bibliography"].([]any)
			var j int
			if _, err := fmt.Sscanf(c.BibliographyPointer, "/researchSourceContext/bibliography/%d", &j); err != nil {
				t.Fatal(err)
			}
			candidate := value[j].(map[string]any)
			if !strings.Contains(string(mustJSON(candidate)), "10.2337/db22-1418-p") || strings.Contains(string(mustJSON(candidate)), "547-P") {
				t.Fatal("conference source bound to wrong bibliography", candidate)
			}
			actualConference = true
		}
	}
	if !actualConference || recommendation == nil || recommendation.NodeID != "evidence_screening" || len(recommendation.AffectedStages) != 7 {
		t.Fatal("historical report/recommendation silently reinterpreted", recommendation)
	}
	prompt := buildAIStagePrompt(detail, *s, node)
	if !strings.Contains(prompt, researchEvidenceNavigationVersion) || !bytes.Equal(before, mustJSON(detail)) || !reflect.DeepEqual(recommendation, researchRevisionRecommendation(detail)) {
		t.Fatal("new guidance mutated captured history")
	}
	analysis, err := outputPort(s.Output, "analysis")
	if err != nil || validateWorkflowAIStageOutput(node, analysis, s.Input) != nil {
		t.Fatal("new guidance changed the old rejection's validity", err)
	}
	// Derived artifacts are separate from the immutable captured records. Used
	// by the optional context-budget replay; never written into the app store.
	if err := os.WriteFile(filepath.Join(dir, "derived-review-prompt.txt"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "derived-evidence-index.json"), mustJSON(index), 0600); err != nil {
		t.Fatal(err)
	}
	var state struct {
		Runs []struct {
			Inputs      string `json:"inputs_json"`
			Compilation string `json:"compilation_json"`
		} `json:"workflow_runs"`
	}
	stateBytes, err := os.ReadFile(filepath.Join(dir, "formal-state-before.json"))
	if err != nil || json.Unmarshal(stateBytes, &state) != nil || len(state.Runs) != 1 {
		t.Fatal("missing original run bytes", err)
	}
	detail.Run.Inputs = json.RawMessage(state.Runs[0].Inputs)
	if err := json.Unmarshal([]byte(state.Runs[0].Compilation), &detail.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	// Simulate the ALREADY existing manual-scope button through production
	// Retry, with an in-memory repository and launches disabled. No DB reset,
	// historical-plan rewrite or extra model planning pass is performed.
	repository := &recommendationRetryRepository{detail: detail}
	service := &RuntimeService{repository: repository, projects: fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}}, permissions: permission.NewEngine(nil), tools: tool.NewService(&indexedCallsFixture{}, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "capture-test-event", nil }, closed: true}
	gate := findStepByNode(detail.Steps, "delivery_gate")
	if gate == nil {
		t.Fatal("missing real failed gate")
	}
	if _, err := service.Retry(context.Background(), RetryCommand{ProjectID: detail.Run.ProjectID, RunID: detail.Run.ID, StepID: gate.ID, RevisionNodeID: "report_drafting", ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}); err != nil || repository.resets != 1 {
		t.Fatal("existing manual report-only retry cannot resume captured task", err, repository.resets)
	}
	var revision reviewRevision
	if json.Unmarshal(repository.event.Payload, &revision) != nil || revision.ProducerNodeID != "report_drafting" || revision.UsedRecommendation || revision.ReviewOutputSHA256 != "0bee4932bcda7c786f5894a4fbcba8dbddb83bb4adab59d26b5d718b3d7b6267" || !strings.Contains(string(revision.IndependentReview), "revisionPlan") {
		t.Fatal("manual retry lost historical review rather than retaining it", string(repository.event.Payload))
	}
	// Match SQLite's confirmed suffix reset in memory, then use the production
	// binding/prompt builder to check the next report's actual context size.
	detail.Events = append(detail.Events, repository.event)
	detail.Run.Status, detail.Run.CurrentStep = RunQueued, 11
	for i := range detail.Steps {
		if detail.Steps[i].Ordinal >= 11 && detail.Steps[i].Ordinal <= gate.Ordinal {
			detail.Steps[i].Status, detail.Steps[i].Input, detail.Steps[i].Output, detail.Steps[i].InputSHA256 = StepQueued, raw(`{}`), raw(`{}`), ""
		}
	}
	reportNode := compilationNodeMap(detail.Run.Compilation)["report_drafting"]
	reportInput, err := bindNodeInput(detail, reportNode)
	if err != nil {
		t.Fatal("next confirmed report cannot bind", err)
	}
	reportStep := *findStepByNode(detail.Steps, "report_drafting")
	reportStep.Input, reportStep.InputSHA256 = reportInput, hashJSON(reportInput)
	reportPrompt := buildAIStagePrompt(detail, reportStep, reportNode)
	if !strings.Contains(reportPrompt, "evidence-navigation-v1") || !strings.Contains(reportPrompt, "_reviewRevision") {
		t.Fatal("next confirmed report lost guidance or prior review")
	}
	if err := os.WriteFile(filepath.Join(dir, "derived-repair-report-prompt.txt"), []byte(reportPrompt), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("next manual report repair: inputCharacters=%d promptCharacters=%d", len([]rune(string(reportInput))), len([]rune(reportPrompt)))
	t.Logf("capture: excerpts=%d screening=%d excluded=%d newPromptCharacters=%d indexCharacters=%d oldPlanStages=%d", len(index.Citations), index.ScreeningRecords.RecordCount, index.ScreeningRecords.DecisionCounts["exclude"], len([]rune(prompt)), len([]rune(string(mustJSON(index)))), len(recommendation.AffectedStages))
}
