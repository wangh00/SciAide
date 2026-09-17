package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/skillrun"
)

type workflowSkillLoaderStub struct {
	values []skillrun.Snapshot
	err    error
}

func (stub workflowSkillLoaderStub) ListRunSkillSnapshots(context.Context, string) ([]skillrun.Snapshot, error) {
	return stub.values, stub.err
}

func TestValidateRequiredWorkflowSkillsRequiresFrozenStageSkill(t *testing.T) {
	step := Step{Input: raw(`{"routeContext":{"selectedSkills":[{"name":"statistics-method","role":"选择统计方法","stageIds":["method_selection"],"contentHash":"content-v1","packageHash":"package-v1"}]}}`)}
	node := CompiledNode{ID: "method_selection", SkillRouting: true}
	execution := AIExecution{ChatRunID: "chat-run"}

	tests := []struct {
		name   string
		loader StarterSkillLoader
		want   string
	}{
		{name: "loader missing", want: "加载器尚未配置"},
		{name: "skill missing", loader: workflowSkillLoaderStub{}, want: "没有实际加载"},
		{name: "snapshot changed", loader: workflowSkillLoaderStub{values: []skillrun.Snapshot{{Name: "statistics-method", ContentHash: "content-v2", PackageHash: "package-v1"}}}, want: "包快照不一致"},
		{name: "snapshot matches", loader: workflowSkillLoaderStub{values: []skillrun.Snapshot{{Name: "statistics-method", ContentHash: "content-v1", PackageHash: "package-v1"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &RuntimeService{skills: tt.loader}
			err := service.validateRequiredWorkflowSkills(context.Background(), RunDetail{}, step, node, execution)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validate required Workflow Skills: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateRequiredWorkflowSkillsAllowsUnboundAndUnrelatedStages(t *testing.T) {
	service := &RuntimeService{}
	execution := AIExecution{}
	for _, test := range []struct {
		name string
		step Step
		node CompiledNode
	}{
		{name: "route has no selected Skills", step: Step{Input: raw(`{"routeContext":{}}`)}, node: CompiledNode{ID: "method_selection", SkillRouting: true}},
		{name: "Skill belongs to another stage", step: Step{Input: raw(`{"routeContext":{"selectedSkills":[{"name":"review-method","stageIds":["independent_review"]}]}}`)}, node: CompiledNode{ID: "question_refinement", SkillRouting: true}},
		{name: "stage has routing disabled", step: Step{Input: raw(`{"routeContext":{"selectedSkills":[{"name":"statistics-method","stageIds":["method_selection"]}]}}`)}, node: CompiledNode{ID: "method_selection"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := service.validateRequiredWorkflowSkills(context.Background(), RunDetail{}, test.step, test.node, execution); err != nil {
				t.Fatalf("unexpected Skill validation error: %v", err)
			}
		})
	}
}

func TestRequiredWorkflowSkillsMapsMethodKnowledgeToExecutableAIStages(t *testing.T) {
	step := Step{Input: raw(`{"routeContext":{"selectedSkills":[
		{"name":"question-method","stageIds":["question_refinement"]},
		{"name":"synthesis-method","stageIds":["method_selection"]},
		{"name":"analysis-method","stageIds":["python_analysis"]},
		{"name":"writing-method","stageIds":["report_publication"]}
	]}}`)}
	tests := []struct {
		node  string
		wants []string
	}{
		{node: "question_refinement", wants: []string{"question-method"}},
		{node: "method_selection", wants: []string{"synthesis-method"}},
		{node: "method_implementation", wants: []string{"analysis-method"}},
		{node: "result_interpretation", wants: []string{"analysis-method"}},
		{node: "report_drafting", wants: []string{"writing-method"}},
		{node: "independent_review", wants: []string{"question-method", "synthesis-method", "analysis-method", "writing-method"}},
	}
	for _, tt := range tests {
		t.Run(tt.node, func(t *testing.T) {
			required := requiredWorkflowSkills(nil, step, CompiledNode{ID: tt.node, SkillRouting: true})
			if len(required) != len(tt.wants) {
				t.Fatalf("required = %#v, want %v", required, tt.wants)
			}
			for _, name := range tt.wants {
				if _, ok := required[name]; !ok {
					t.Fatalf("required = %#v, missing %q", required, name)
				}
			}
		})
	}
}

func TestBuildAIStagePromptMakesFrozenSkillsExplicitToSemanticRouter(t *testing.T) {
	step := Step{Input: raw(`{"routeContext":{"selectedSkills":[{"name":"statistics-method","stageIds":["python_analysis"]}]}}`)}
	prompt := buildAIStagePrompt(RunDetail{}, step, CompiledNode{ID: "method_implementation", SkillRouting: true, Prompt: "实现方法", OutputSchema: raw(`{"type":"object"}`)})
	if !strings.Contains(prompt, "Use the statistics-method Skill:") || !strings.Contains(prompt, "call builtin.skill.load for every Skill listed below and no others") {
		t.Fatalf("prompt does not explicitly bind frozen Skills:\n%s", prompt)
	}
}

func TestMethodImplementationPromptForbidsManualDiagnosticFiles(t *testing.T) {
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{ID: "method_implementation", Prompt: "implement", OutputSchema: dynamicImplementationSchema()})
	for _, expected := range []string{"automatically captures open Matplotlib figures", "do not create a diagnostics directory", "never pass an extensionless filename", "split implementation envelope", "fenced python block", "Never embed source code in the JSON metadata"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("method implementation prompt is missing %q:\n%s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "exactly one fenced json object that validates against this output schema") {
		t.Fatalf("method implementation still requests the fragile all-in-one JSON envelope:\n%s", prompt)
	}
}

func TestDynamicImplementationUsesKernelGlobalsDirectly(t *testing.T) {
	valid := raw(`{"methodSummary":"m","dependencies":[],"analysisInput":{},"code":"from pathlib import Path\nsource = Path(SCIAIDE_INPUTS[0])\nPath(SCIAIDE_OUTPUTS[0]).write_text('{}')\nPath(SCIAIDE_OUTPUTS[1]).write_text('# method')\n{'ok': True}","expectedOutputs":["result","method"],"assumptions":[],"limitations":[]}`)
	if err := validateDynamicImplementationOutput(valid); err != nil {
		t.Fatalf("valid Kernel implementation rejected: %v", err)
	}
	for _, code := range []string{
		`inputs = os.environ.get("SCIAIDE_INPUTS", "")\noutputs = SCIAIDE_OUTPUTS`,
		`inputs = SCIAIDE_INPUTS\noutputs = os.getenv('SCIAIDE_OUTPUTS')`,
	} {
		var value map[string]any
		if err := json.Unmarshal(valid, &value); err != nil {
			t.Fatal(err)
		}
		value["code"] = code
		encoded, _ := json.Marshal(value)
		if err := validateDynamicImplementationOutput(encoded); err == nil || !strings.Contains(err.Error(), "错当成环境变量") {
			t.Fatalf("invalid Kernel implementation accepted: %s, %v", code, err)
		}
	}
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{ID: "method_implementation", Prompt: "implement", OutputSchema: dynamicImplementationSchema()})
	if !strings.Contains(prompt, "pre-defined Python list") || !strings.Contains(prompt, "Never access them through os.environ or os.getenv") {
		t.Fatalf("method implementation prompt is missing the Kernel contract:\n%s", prompt)
	}
}

func TestInvalidUpstreamImplementationFindsConfirmedProducer(t *testing.T) {
	invalidAnalysis := raw(`{"methodSummary":"m","dependencies":[],"analysisInput":{},"code":"import os\nroots = os.environ.get('SCIAIDE_INPUTS', '')\nSCIAIDE_OUTPUTS","expectedOutputs":["result","method"],"assumptions":[],"limitations":[]}`)
	producerOutput, _ := json.Marshal(map[string]any{"analysis": json.RawMessage(invalidAnalysis), "text": "frozen"})
	detail := RunDetail{Steps: []Step{
		{ID: "producer", NodeID: "method_implementation", Ordinal: 2, Status: StepCompleted, Output: producerOutput},
		{ID: "prepare", NodeID: "dependency_preparation", Ordinal: 3, Status: StepCompleted, Output: raw(`{}`)},
		{ID: "failed", NodeID: "python_analysis", Ordinal: 4, Status: StepFailed, Output: raw(`{}`)},
	}}
	producer, err := invalidUpstreamImplementation(detail, detail.Steps[2])
	if producer == nil || producer.ID != "producer" || err == nil || !strings.Contains(err.Error(), "错当成环境变量") {
		t.Fatalf("invalid upstream implementation = %#v, %v", producer, err)
	}
}

func TestBuildAIStagePromptScopesReviewedInputDigestToReviewSchema(t *testing.T) {
	step := Step{Input: raw(`{"goal":"define the research boundary"}`), InputSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	ordinary := buildAIStagePrompt(RunDetail{}, step, CompiledNode{
		ID: "question_refinement", Prompt: "Clarify the research question.",
		OutputSchema: dynamicQuestionSchema(),
	})
	if strings.Contains(ordinary, "reviewedInputSha256") || strings.Contains(ordinary, "The trusted SHA-256 of the exact stage_input JSON is") {
		t.Fatalf("ordinary stage received review-only digest instruction:\n%s", ordinary)
	}
	if !strings.Contains(ordinary, "emit exactly the properties declared") {
		t.Fatalf("ordinary stage is missing closed-schema instruction:\n%s", ordinary)
	}

	review := buildAIStagePrompt(RunDetail{}, step, CompiledNode{
		ID: "independent_review", Prompt: "Review the frozen research result.",
		OutputSchema: independentReviewSchema(),
	})
	if !strings.Contains(review, "reviewedInputSha256") || !strings.Contains(review, step.InputSHA256) {
		t.Fatalf("independent review is missing digest instruction:\n%s", review)
	}
}

func TestIndependentReviewPromptIncludesTrustedHostAuditWithoutWeakeningReview(t *testing.T) {
	inputHash := strings.Repeat("1", 64)
	codeHash := strings.Repeat("2", 64)
	outputHash := strings.Repeat("3", 64)
	environmentHash := strings.Repeat("4", 64)
	reproductionHash := strings.Repeat("5", 64)
	artifactHash := strings.Repeat("6", 64)
	pythonOutput, err := json.Marshal(map[string]any{
		"structured": map[string]any{
			"inputSha256": map[string]string{"research-inputs/data.csv": inputHash},
			"codeSha256":  codeHash, "outputSha256": map[string]string{"analysis-output/results.json": outputHash},
			"environmentFingerprint": environmentHash, "reproductionSha256": reproductionHash,
		},
		"artifacts": []map[string]any{{"name": "results.json", "workspacePath": "analysis-output/results.json", "sizeBytes": 42, "sha256": artifactHash}},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{
		Run: Run{ID: "workflow-run", InputsSHA256: strings.Repeat("a", 64), Compilation: Compilation{Nodes: []CompiledNode{
			{ID: "select", Kind: NodeCitationSelection}, {ID: "analysis", Kind: NodePython},
		}}},
		Steps: []Step{
			{NodeID: "select", Status: StepCompleted, Output: raw(`{"citations":[],"evidenceStatus":"no_verified_citations"}`)},
			{NodeID: "analysis", Status: StepCompleted, Attempt: 2, Output: pythonOutput},
		},
	}
	reviewStep := Step{Input: raw(`{"context":{"summary":"frozen"}}`), InputSHA256: strings.Repeat("b", 64)}
	reviewPrompt := buildAIStagePrompt(detail, reviewStep, CompiledNode{ID: "independent_review", Prompt: "review", OutputSchema: independentReviewSchema()})
	for _, expected := range []string{
		"Trusted host audit snapshot", `"evidenceStatus":"no_verified_citations"`, "explicit allowed continuation without verified citations",
		inputHash, codeHash, outputHash, environmentHash, reproductionHash, artifactHash,
		"These facts do not prove the scientific method or conclusion is valid",
	} {
		if !strings.Contains(reviewPrompt, expected) {
			t.Fatalf("independent review prompt is missing %q:\n%s", expected, reviewPrompt)
		}
	}
	reportPrompt := buildAIStagePrompt(detail, reviewStep, CompiledNode{ID: "report_drafting", Prompt: "draft", OutputSchema: dynamicReportSchema()})
	if !strings.Contains(reportPrompt, "Trusted host audit snapshot") || !strings.Contains(reportPrompt, reproductionHash) || !strings.Contains(reportPrompt, `"evidenceStatus":"no_verified_citations"`) {
		t.Fatalf("report drafting prompt is missing trusted host audit:\n%s", reportPrompt)
	}
	ordinaryPrompt := buildAIStagePrompt(detail, reviewStep, CompiledNode{ID: "result_interpretation", Prompt: "interpret", OutputSchema: raw(`{"type":"object","properties":{"summary":{"type":"string"}}}`)})
	if strings.Contains(ordinaryPrompt, "Trusted host audit snapshot") || strings.Contains(ordinaryPrompt, reproductionHash) {
		t.Fatalf("ordinary stage received delivery audit snapshot:\n%s", ordinaryPrompt)
	}
}

func TestExtractStageJSONKeepsOrdinarySchemasStrict(t *testing.T) {
	payload, err := json.Marshal(map[string]string{
		"summary":             "ok",
		"reviewedInputSha256": strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "```json\n" + string(payload) + "\n```"
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	if _, err := extractStageJSON(text, schema); err == nil || !strings.Contains(err.Error(), "unknown property") {
		t.Fatalf("ordinary schema accepted review-only metadata: %v", err)
	}
}

func TestExtractStageJSONRejectsUndeclaredFields(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	for _, field := range []string{"limitations_note", "conflicts_note", "reviewedInputSha256"} {
		t.Run(field, func(t *testing.T) {
			payload := `{"summary":"ok","` + field + `":""}`
			if _, err := extractStageJSON("```json\n"+payload+"\n```", schema); err == nil || !strings.Contains(err.Error(), "unknown property") {
				t.Fatalf("custom schema accepted %s: %v", field, err)
			}
		})
	}
}

func TestExtractWorkflowAIStageOutputRejectsUndeclaredFields(t *testing.T) {
	payload := `{"query":"query","researchQuestion":"r","objectives":["o"],"scope":["s"],"assumptions":[],"successCriteria":["c"],"limitations":[],"unrelated_note":"unexpected"}`
	_, err := extractWorkflowAIStageOutput("```json\n"+payload+"\n```", CompiledNode{PromptVersion: "dynamic-question-v1", OutputSchema: dynamicQuestionSchema()})
	if err == nil || !strings.Contains(err.Error(), "unknown property") {
		t.Fatalf("undeclared field was accepted: %v", err)
	}
}

func TestExtractStageJSONKeepsUnfencedSchemasStrict(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	text := "Result:\n{" + `"summary":"ok","limitations_note":"unexpected"` + "}"
	if _, err := extractStageJSON(text, schema); err == nil || !strings.Contains(err.Error(), "unknown property") {
		t.Fatalf("unfenced schema accepted undeclared field: %v", err)
	}
}

func TestExtractStageJSONAcceptsCompleteJSONWithTruncatedClosingFence(t *testing.T) {
	payload := `{"markdown":"# report\n\nA complete report.","claimSummary":["claim"],"methodSummary":"method","limitations":[],"confidence":"medium"}`
	for _, suffix := range []string{"`", "``", ""} {
		t.Run("suffix_"+strings.ReplaceAll(suffix, "`", "tick"), func(t *testing.T) {
			value, err := extractStageJSON("```json\n"+payload+"\n"+suffix, dynamicReportSchema())
			if err != nil {
				t.Fatalf("complete JSON with truncated fence was rejected: %v", err)
			}
			var decoded struct {
				Markdown string `json:"markdown"`
			}
			if err := json.Unmarshal(value, &decoded); err != nil || decoded.Markdown != "# report\n\nA complete report." {
				t.Fatalf("decoded report is unexpected: %s", value)
			}
		})
	}
}

func TestExtractStageJSONRepairsCurrentImplementationOutputShape(t *testing.T) {
	// The implementation schema keeps metadata optional so a long code field
	// cannot make an otherwise usable method fail solely on trailing prose.
	if err := (tool.JSONSchemaValidator{}).Validate(dynamicImplementationSchema(), json.RawMessage(`{"methodSummary":"m","dependencies":[],"analysisInput":{},"code":"print(1)"}`)); err != nil {
		t.Fatalf("implementation with optional metadata was rejected: %v", err)
	}
}

func TestExtractDynamicImplementationAcceptsSplitEnvelope(t *testing.T) {
	text := "Implementation follows.\n```json\n" +
		`{"methodSummary":"m","dependencies":["pandas"],"analysisInput":{"alpha":0.05},"assumptions":[],"limitations":[]}` +
		"\n```\n```python\n" +
		"source = SCIAIDE_INPUTS[0]\nlabel = c[\"beta\"]\nPath(SCIAIDE_OUTPUTS[0]).write_text(\"{}\", encoding=\"utf-8\")\nPath(SCIAIDE_OUTPUTS[1]).write_text(\"# method\", encoding=\"utf-8\")\n{\"ok\": True}\n```"
	value, err := extractWorkflowAIStageOutput(text, CompiledNode{ID: "method_implementation", OutputSchema: dynamicImplementationSchema()})
	if err != nil {
		t.Fatalf("split implementation rejected: %v", err)
	}
	var decoded struct {
		Code          string         `json:"code"`
		AnalysisInput map[string]any `json:"analysisInput"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil || !strings.Contains(decoded.Code, `c["beta"]`) || decoded.AnalysisInput["alpha"] != 0.05 {
		t.Fatalf("assembled implementation = %s, %v", value, err)
	}
	if err := validateDynamicImplementationOutput(value); err != nil {
		t.Fatalf("assembled implementation failed semantic validation: %v", err)
	}
}

func TestImplementationAcceptsWholeStructuredJSON(t *testing.T) {
	code := "source = SCIAIDE_INPUTS[0]\noutputs = SCIAIDE_OUTPUTS\nresult = {'ok': True}"
	value, _ := json.Marshal(map[string]any{"methodSummary": "m", "dependencies": []string{}, "analysisInput": map[string]any{}, "code": code})
	got, _, err := NormalizeAIStageSubmission(string(value), dynamicImplementationSchema(), "method_implementation")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(got, &decoded) != nil || decoded.Code != code {
		t.Fatal("source changed")
	}
	for _, bad := range []string{string(value) + "\nexplanation", string(value) + string(value), string(value) + "\n```python\nother source\n```", `{"methodSummary":"m","dependencies":[],"analysisInput":{}}`} {
		if _, _, err := NormalizeAIStageSubmission(bad, dynamicImplementationSchema(), "method_implementation"); err == nil {
			t.Fatal("ambiguous/incomplete output accepted")
		}
	}
}

func TestImplementationMetadataErrorKeepsOuterObject(t *testing.T) {
	text := `{"methodSummary":"present","dependencies":[],"analysisInput":{},"expectedOutputs":[{"name":"results","format":"json"},{"name":"methods","format":"md"}]}` + "\n```python\nresult = {}\n```"
	_, _, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema(), "method_implementation")
	if err == nil || !strings.Contains(err.Error(), "expectedOutputs") || strings.Contains(err.Error(), "methodSummary: required") {
		t.Fatalf("misleading error: %v", err)
	}
}

func TestImplementationAcceptsCompleteBareMetadataWithoutChangingCode(t *testing.T) {
	metadata := `{"methodSummary":"m","dependencies":[],"analysisInput":{"alpha":0.05}}`
	code := "source = SCIAIDE_INPUTS[0]\noutputs = SCIAIDE_OUTPUTS\nresult = {'ok': True}"
	for _, prefix := range []string{metadata, "```json\n" + metadata + "\n```"} {
		text := prefix + "\n```python\n" + code + "\n```"
		value, _, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema(), "method_implementation")
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(value, &result); err != nil {
			t.Fatal(err)
		}
		var got string
		if json.Unmarshal(result["code"], &got) != nil || got != code {
			t.Fatal("source changed")
		}
	}
	for _, bad := range []string{
		metadata[:len(metadata)-1], metadata + " trailing prose", metadata + metadata,
		`{"methodSummary":"m","dependencies":[],"analysisInput":{},"code":"conflicting code"}`,
		`{"methodSummary":"m","dependencies":[],"analysisInput":{},"unknown":true}`,
		`{"dependencies":[],"analysisInput":{}}`,
	} {
		if _, _, err := NormalizeAIStageSubmission(bad+"\n```python\n"+code+"\n```", dynamicImplementationSchema(), "method_implementation"); err == nil {
			t.Fatalf("accepted invalid metadata: %s", bad)
		}
	}
}

func TestValidateAIStageProtocolRejectsObsoleteDynamicImplementation(t *testing.T) {
	for _, version := range []string{"", "dynamic-implementation-v1", "dynamic-implementation-v2", "dynamic-implementation-v3"} {
		err := validateAIStageProtocol(CompiledNode{ID: "method_implementation", PromptVersion: version})
		if err == nil || !strings.Contains(err.Error(), "请新建科研任务") {
			t.Fatalf("obsolete implementation protocol %q was not rejected clearly: %v", version, err)
		}
	}
	if err := validateAIStageProtocol(CompiledNode{ID: "method_implementation", PromptVersion: dynamicImplementationPromptVersion}); err != nil {
		t.Fatalf("current implementation protocol was rejected: %v", err)
	}
	if err := validateAIStageProtocol(CompiledNode{ID: "result_interpretation", PromptVersion: "dynamic-interpret-v3"}); err != nil {
		t.Fatalf("unrelated AI stage was rejected: %v", err)
	}
	if err := validateCompilationAIProtocols(Compilation{Nodes: []CompiledNode{{ID: "method_implementation", PromptVersion: "dynamic-implementation-v3"}}}); err == nil || !strings.Contains(err.Error(), "新建科研任务") {
		t.Fatalf("compilation with obsolete implementation protocol was not rejected clearly: %v", err)
	}
}

func TestExtractDynamicImplementationRejectsIncompleteSplitEnvelope(t *testing.T) {
	metadata := "```json\n" + `{"methodSummary":"m","dependencies":[],"analysisInput":{}}` + "\n```\n"
	for _, text := range []string{
		metadata + "```python\nsource = SCIAIDE_INPUTS[0]\nSCIAIDE_OUTPUTS[0]",
		metadata + "```python\nsource = SCIAIDE_INPUTS[0]\nSCIAIDE_OUTPUTS[0]\n```\nunexpected tail",
	} {
		if _, err := extractWorkflowAIStageOutput(text, CompiledNode{ID: "method_implementation", OutputSchema: dynamicImplementationSchema()}); err == nil {
			t.Fatalf("incomplete or tailed split implementation was accepted: %q", text)
		}
	}
}

func TestExtractStageJSONDoesNotRepairUnfencedIncompleteJSON(t *testing.T) {
	text := `{"methodSummary":"m","dependencies":[],"analysisInput":{},"code":"print(1}`
	if _, err := extractStageJSON(text, dynamicImplementationSchema()); err == nil {
		t.Fatal("unfenced incomplete implementation was accepted")
	}
}

func TestExtractStageJSONKeepsMalformedFencedEnvelopeError(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["routes"],"properties":{"routes":{"type":"array","items":{"type":"object","required":["objective"],"properties":{"objective":{"type":"string"}}}}}}`)
	text := "```json\n" + `{"routes":[{"objective":"route"}],"value":"x".replace("x","y")}` + "\n```"
	_, err := extractStageJSON(text, schema)
	if err == nil || !strings.Contains(err.Error(), "invalid character") || strings.Contains(err.Error(), "required property") {
		t.Fatalf("malformed fenced output produced a misleading error: %v", err)
	}
}

func TestExtractResearchStarterFoldsExcessPresentationLayers(t *testing.T) {
	route := starterDataRoute()
	route.RequiredResources = []string{}
	route.Blockers = []string{}
	analysis := route.Layers[2]
	route.Layers = []ResearchRouteLayer{
		route.Layers[0], route.Layers[1],
		{LayerID: "dependency", Title: "依赖", Objective: "准备环境", Stages: []ResearchRouteStage{analysis.Stages[0]}},
		{LayerID: "execution", Title: "执行", Objective: "运行分析", Stages: []ResearchRouteStage{analysis.Stages[1]}},
		{LayerID: "interpretation", Title: "解释", Objective: "解释结果", Stages: []ResearchRouteStage{analysis.Stages[2]}},
		route.Layers[3],
	}
	// Make the fixture match the closed planner schema exactly. The only
	// deliberate defect is six presentation layers instead of the canonical
	// three to five.
	for layerIndex := range route.Layers {
		for stageIndex := range route.Layers[layerIndex].Stages {
			stage := &route.Layers[layerIndex].Stages[stageIndex]
			stage.Methods = []string{}
			stage.SkillNames = []string{}
			stage.Inputs = []string{}
		}
	}
	plan := ResearchStarterPlan{
		NormalizedQuestion: "测试问题", ResearchType: "data", AvailableResources: []string{}, MissingInformation: []string{},
		Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID, RecommendationReason: "测试", Confidence: "medium",
		Limitations: []string{}, SelectedSkills: []ResearchSkillSelection{},
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if relaxed, ok := relaxedResearchStarterSchema(dynamicResearchStarterSchema()); ok {
		if _, relaxedErr := extractStageJSON("```json\n"+string(payload)+"\n```", relaxed); relaxedErr != nil {
			t.Fatalf("relaxed extraction error: %v", relaxedErr)
		}
	}
	value, err := extractWorkflowAIStageOutput("```json\n"+string(payload)+"\n```", CompiledNode{ID: "explore", OutputSchema: dynamicResearchStarterSchema()})
	if err != nil {
		t.Fatalf("excess starter layers were not normalized: %v", err)
	}
	var decoded ResearchStarterPlan
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Routes) != 1 || len(decoded.Routes[0].Layers) < 3 || len(decoded.Routes[0].Layers) > 5 {
		t.Fatalf("normalized layer count = %d, want 3-5", len(decoded.Routes[0].Layers))
	}
	if got := strings.Join(decoded.Routes[0].StageIDs, ","); got != strings.Join(route.StageIDs, ",") {
		t.Fatalf("stage order changed during layer folding: %s", got)
	}
}

func TestExtractResearchStarterFoldsOnlyTheInvalidRoute(t *testing.T) {
	valid := starterDesignRoute()
	valid.RequiredResources, valid.Blockers = []string{}, []string{}
	for layerIndex := range valid.Layers {
		for stageIndex := range valid.Layers[layerIndex].Stages {
			stage := &valid.Layers[layerIndex].Stages[stageIndex]
			stage.Methods, stage.SkillNames, stage.Inputs = []string{}, []string{}, []string{}
		}
	}
	invalid := starterDataRoute()
	invalid.RequiredResources = []string{}
	invalid.Blockers = []string{}
	analysis := invalid.Layers[2]
	invalid.Layers = []ResearchRouteLayer{
		invalid.Layers[0], invalid.Layers[1],
		{LayerID: "dependency", Title: "依赖", Objective: "准备环境", Stages: []ResearchRouteStage{analysis.Stages[0]}},
		{LayerID: "execution", Title: "执行", Objective: "运行分析", Stages: []ResearchRouteStage{analysis.Stages[1]}},
		{LayerID: "interpretation", Title: "解释", Objective: "解释结果", Stages: []ResearchRouteStage{analysis.Stages[2]}},
		invalid.Layers[3],
	}
	for layerIndex := range invalid.Layers {
		for stageIndex := range invalid.Layers[layerIndex].Stages {
			stage := &invalid.Layers[layerIndex].Stages[stageIndex]
			stage.Methods, stage.SkillNames, stage.Inputs = []string{}, []string{}, []string{}
		}
	}
	plan := ResearchStarterPlan{
		NormalizedQuestion: "测试问题", ResearchType: "data", AvailableResources: []string{}, MissingInformation: []string{},
		Routes: []ResearchRoute{valid, invalid}, RecommendedRouteID: valid.RouteID, RecommendationReason: "测试", Confidence: "medium",
		Limitations: []string{}, SelectedSkills: []ResearchSkillSelection{},
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	value, err := extractWorkflowAIStageOutput("```json\n"+string(payload)+"\n```", CompiledNode{ID: "explore", OutputSchema: dynamicResearchStarterSchema()})
	if err != nil {
		t.Fatalf("mixed starter routes were not normalized: %v", err)
	}
	var decoded ResearchStarterPlan
	if err := json.Unmarshal(value, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Routes) != 2 || len(decoded.Routes[0].Layers) != len(valid.Layers) || len(decoded.Routes[1].Layers) < 3 || len(decoded.Routes[1].Layers) > 5 {
		t.Fatalf("route layer counts = %d and %d", len(decoded.Routes[0].Layers), len(decoded.Routes[1].Layers))
	}
	if got := strings.Join(decoded.Routes[0].StageIDs, ","); got != strings.Join(valid.StageIDs, ",") {
		t.Fatalf("valid route stage order changed: %s", got)
	}
}

func TestBuildAIStagePromptIncludesPriorOutputValidationFailure(t *testing.T) {
	step := Step{ID: "step", Attempt: 2, Input: raw(`{"context":"frozen"}`), InputSHA256: strings.Repeat("a", 64)}
	detail := RunDetail{AIExecutions: []AIExecution{{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: `$.routes[0].layers[0].stages[0].objective: required property is missing`}}}
	prompt := buildAIStagePrompt(detail, step, CompiledNode{ID: "explore", Prompt: "plan", OutputSchema: raw(`{"type":"object"}`)})
	for _, expected := range []string{"automatic structured-output correction attempt", "objective: required property is missing", "literal JSON values only", ".replace(...)"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("automatic correction prompt is missing %q:\n%s", expected, prompt)
		}
	}
}

func TestBuildAIStagePromptDirectsLayerMergeRepair(t *testing.T) {
	step := Step{ID: "step", Attempt: 2, Input: raw(`{"context":"frozen"}`), InputSHA256: strings.Repeat("a", 64)}
	detail := RunDetail{AIExecutions: []AIExecution{{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: `AI 阶段输出不符合 Schema: $.routes[1].layers: violates maxItems`}}}
	prompt := buildAIStagePrompt(detail, step, CompiledNode{ID: "explore", Prompt: "plan", OutputSchema: dynamicResearchStarterSchema()})
	for _, expected := range []string{"merge adjacent presentation layers", "Do not delete stages", "unchanged stageIds"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("layer repair prompt is missing %q:\n%s", expected, prompt)
		}
	}
}

func TestExtractStageJSONAcceptsReportWithNestedMarkdownCodeFence(t *testing.T) {
	payload := "{\"markdown\":\"# report\\n\\nExample:\\n```json\\n{\\\"example\\\":true}\\n```\",\"claimSummary\":[\"claim\"],\"methodSummary\":\"method\",\"limitations\":[],\"confidence\":\"medium\"}"
	text := "```json\n" + payload + "\n```\n\n以上内容已完成修订。"
	if _, err := extractStageJSON(text, dynamicReportSchema()); err != nil {
		t.Fatalf("report with nested markdown fence was rejected: %v", err)
	}
}

func TestExtractStageJSONAcceptsSpacedJSONFence(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	value, err := extractStageJSON("``` json\n{\"summary\":\"done\"}\n```", schema)
	if err != nil {
		t.Fatalf("spaced JSON fence was rejected: %v", err)
	}
	if string(value) != `{"summary":"done"}` {
		t.Fatalf("spaced JSON fence value = %s", value)
	}
}

func TestExtractStageJSONRejectsProgressOnlyResponse(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	if _, err := extractStageJSON("我会先加载相关 Skill 并检查项目资料，然后给出路线。", schema); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("progress-only response was accepted: %v", err)
	}
}

func TestExtractStageJSONRepairsPrematureObjectCloseBeforeDeclaredFields(t *testing.T) {
	text := "```json\n{" +
		`"markdown":"# report","claimSummary":[],"methodSummary":"method"}` +
		`,"limitations":[],"confidence":"medium"}` + "\n```"
	value, err := extractStageJSON(text, dynamicReportSchema())
	if err != nil {
		t.Fatalf("premature object close was not repaired: %v", err)
	}
	var decoded struct {
		Markdown    string   `json:"markdown"`
		Limitations []string `json:"limitations"`
		Confidence  string   `json:"confidence"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil || decoded.Markdown != "# report" || decoded.Confidence != "medium" {
		t.Fatalf("repaired output = %s, %v", value, err)
	}
}

func TestExtractStageJSONDoesNotMergeArbitraryTail(t *testing.T) {
	text := "```json\n{" +
		`"markdown":"# report","claimSummary":[],"methodSummary":"method"}` +
		",not prose\n```"
	if _, err := extractStageJSON(text, dynamicReportSchema()); err == nil || !strings.Contains(err.Error(), "尾部") {
		t.Fatalf("arbitrary tail was accepted: %v", err)
	}
}

func TestExtractStageJSONRepairsOnlyPrematureQuotesInsideStringValues(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	text := "```json\n" + `{"summary":"推荐路线与你"明确统计方法并产出报告"的目标一致"}` + "\n```"
	value, err := extractStageJSON(text, schema)
	if err != nil {
		t.Fatalf("recoverable provider JSON was rejected: %v", err)
	}
	var decoded struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil || decoded.Summary != `推荐路线与你"明确统计方法并产出报告"的目标一致` {
		t.Fatalf("repaired JSON changed content: %s, %v", value, err)
	}
}

func TestExtractStageJSONDoesNotRepairMissingStructuralComma(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","detail"],"properties":{"summary":{"type":"string"},"detail":{"type":"string"}}}`)
	text := "```json\n" + `{"summary":"ok" "detail":"missing comma"}` + "\n```"
	if _, err := extractStageJSON(text, schema); err == nil {
		t.Fatal("missing structural comma was silently repaired")
	}
}

func TestExtractAIStageOutputRejectsIncompleteJSONAndUnexpectedTail(t *testing.T) {
	validSchema := dynamicReportSchema()
	for _, test := range []struct {
		name string
		text string
	}{
		{name: "incomplete object", text: "```json\n{\"markdown\":\"unfinished\""},
		{name: "unexpected tail", text: "```json\n{\"markdown\":\"ok\",\"claimSummary\":[],\"methodSummary\":\"m\",\"limitations\":[],\"confidence\":\"low\"}\nnot a fence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := extractStageJSON(test.text, validSchema); err == nil {
				t.Fatal("malformed AI output was accepted")
			}
		})
	}
}

func TestRequiredWorkflowSkillsUsesFrozenRunRouteForIndependentReview(t *testing.T) {
	runInputs := raw(`{"research_goal":"课题","route_context":{"selectedSkills":[{"name":"causal-method","role":"核验因果识别","stageIds":["research_design"],"contentHash":"c","packageHash":"p"}]}}`)
	required := requiredWorkflowSkills(runInputs, Step{Input: raw(`{"context":{"design":"frozen"}}`)}, CompiledNode{ID: "independent_review", SkillRouting: true})
	if len(required) != 1 || required["causal-method"].Role != "核验因果识别" {
		t.Fatalf("independent review Skills = %#v", required)
	}
}

func TestPythonExecutionRepairReasonAcceptsOnlyStructuredKernelErrors(t *testing.T) {
	call := tool.Call{Result: &tool.Result{
		Status:     tool.ResultError,
		Structured: raw(`{"status":"error","exception":{"type":"FileNotFoundError","message":"missing output","traceback":"Traceback (most recent call last): ..."}}`),
	}}
	reason, ok := pythonExecutionRepairReason(call)
	if !ok || !strings.Contains(reason, "FileNotFoundError") || !strings.Contains(reason, "missing output") {
		t.Fatalf("python repair reason = %q, %v", reason, ok)
	}
	for _, value := range []tool.Call{
		{Result: &tool.Result{Status: tool.ResultSuccess, Structured: raw(`{"status":"success"}`)}},
		{Result: &tool.Result{Status: tool.ResultError}},
		{Result: &tool.Result{Status: tool.ResultError, Structured: raw(`{"status":"error","exception":null}`)}},
	} {
		if reason, ok := pythonExecutionRepairReason(value); ok || reason != "" {
			t.Fatalf("non-repairable Python result = %q, %v", reason, ok)
		}
	}
	for _, exceptionType := range []string{"KeyboardInterrupt", "SystemExit", "GeneratorExit", "MemoryError"} {
		call.Result.Structured = raw(`{"status":"error","exception":{"type":"` + exceptionType + `","message":"environment failure"}}`)
		if reason, ok := pythonExecutionRepairReason(call); ok || reason != "" {
			t.Fatalf("environment exception %s was classified as repairable: %q, %v", exceptionType, reason, ok)
		}
	}
	for _, exceptionType := range []string{"ModuleNotFoundError", "PermissionError", "TimeoutError", "ConnectionError", "OSError"} {
		call.Result.Structured = raw(`{"status":"error","exception":{"type":"` + exceptionType + `","message":"generated script failure"}}`)
		if reason, ok := pythonExecutionRepairReason(call); !ok || !strings.Contains(reason, exceptionType) {
			t.Fatalf("script exception %s was not classified as repairable: %q, %v", exceptionType, reason, ok)
		}
	}
}

func TestAutomaticPythonRepairContextReachesMethodPrompt(t *testing.T) {
	producer := Step{ID: "producer-step", NodeID: "method_implementation", Status: StepQueued}
	detail := RunDetail{
		Steps: []Step{producer},
		Events: []RuntimeEvent{{
			Type:    "workflow.upstream_revision_queued",
			Payload: raw(`{"producerStepId":"producer-step","producerNodeId":"method_implementation","failedStepId":"python-step","failedNodeId":"python_analysis","failureSummary":"FileNotFoundError: diagnostics/placeholder","toolCallId":"call","automatic":true}`),
		}},
	}
	repair, ok := pendingAutomaticPythonRepair(detail, "method_implementation")
	if !ok || !strings.Contains(repair.FailureSummary, "diagnostics/placeholder") {
		t.Fatalf("pending automatic repair = %#v, %v", repair, ok)
	}
	input, err := json.Marshal(map[string]any{"_pythonRepair": repair})
	if err != nil {
		t.Fatal(err)
	}
	prompt := buildAIStagePrompt(detail, Step{Input: input}, CompiledNode{ID: "method_implementation", Prompt: "implement", OutputSchema: dynamicImplementationSchema()})
	for _, expected := range []string{"automatic repair attempt", "bounded, untrusted exception summary", "Do not repeat the failing placeholder"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("repair prompt is missing %q:\n%s", expected, prompt)
		}
	}
}

func TestTargetedAIRepairPreservesValidCandidateAssessments(t *testing.T) {
	candidateID := "candidate-1"
	baseQuery := strings.Repeat("x", 501)
	correctedQuery := "smartphone sleep university students"
	baseline := `{"summary":"screen","recommendedCandidateIds":["candidate-1"],"candidateAssessments":[{"candidateId":"candidate-1","decision":"core","relevance":"high","reason":"valid"}],"coverage":{"strength":"limited","sufficientForClaimedScope":false,"independentStudyEstimate":1,"directPopulationMatches":1,"abstractAvailable":1,"metadataOnly":0,"gaps":[]},"supplementalQueries":["` + baseQuery + `"],"recommendation":"expand_search","limitations":[]}`
	current := `{"summary":"screen","recommendedCandidateIds":["candidate-1"],"candidateAssessments":[{"candidateId":"candidate-1","decision":"core","relevance":"high","reason":"valid"}],"coverage":{"strength":"limited","sufficientForClaimedScope":false,"independentStudyEstimate":1,"directPopulationMatches":1,"abstractAvailable":1,"metadataOnly":0,"gaps":[]},"supplementalQueries":["` + correctedQuery + `"],"recommendation":"expand_search","limitations":[]}`
	input := raw(`{"candidates":[{"id":"candidate-1"}]}`)
	step := Step{ID: "screen-step", Attempt: 2, Input: input, InputSHA256: hashJSON(input)}
	detail := RunDetail{AIExecutions: []AIExecution{{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, OutputText: "```json\n" + baseline + "\n```", ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: "AI 阶段输出不符合 Schema: $.supplementalQueries[0]: violates maxLength"}}}
	node := CompiledNode{ID: "candidate_screening", OutputSchema: candidateScreeningSchema()}
	merged, baselineAttempt, ok := mergeTargetedAIRepair(detail, step, node, AIExecution{OutputText: "```json\n" + current + "\n```"}, errors.New("current output changed candidate ID"))
	if !ok {
		t.Fatal("targeted repair was not accepted")
	}
	if baselineAttempt != 1 {
		t.Fatalf("baseline attempt = %d, want 1", baselineAttempt)
	}
	var value struct {
		CandidateAssessments []struct {
			CandidateID string `json:"candidateId"`
		} `json:"candidateAssessments"`
		SupplementalQueries []string `json:"supplementalQueries"`
	}
	if err := json.Unmarshal(merged, &value); err != nil {
		t.Fatalf("decode merged output: %v", err)
	}
	if len(value.CandidateAssessments) != 1 || value.CandidateAssessments[0].CandidateID != candidateID {
		t.Fatalf("candidate assessments were not preserved: %#v", value.CandidateAssessments)
	}
	if len(value.SupplementalQueries) != 1 || value.SupplementalQueries[0] != correctedQuery {
		t.Fatalf("targeted field was not replaced: %#v", value.SupplementalQueries)
	}
	if err := validateWorkflowAIStageOutput(node, merged, input); err != nil {
		t.Fatalf("merged output failed validation: %v", err)
	}
}

func TestTargetedAIRepairRejectsChangedInputHash(t *testing.T) {
	input := raw(`{"candidates":[{"id":"candidate-1"}]}`)
	step := Step{ID: "screen-step", Attempt: 2, Input: input, InputSHA256: hashJSON(input)}
	baseline := `{"summary":"screen","recommendedCandidateIds":[],"candidateAssessments":[],"coverage":{"strength":"limited","sufficientForClaimedScope":false,"independentStudyEstimate":0,"directPopulationMatches":0,"abstractAvailable":0,"metadataOnly":0,"gaps":[]},"supplementalQueries":[],"recommendation":"expand_search","limitations":[]}`
	detail := RunDetail{AIExecutions: []AIExecution{{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: "different-input", OutputText: "```json\n" + baseline + "\n```", ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: "AI 阶段输出不符合 Schema: $.supplementalQueries[0]: violates maxLength"}}}
	node := CompiledNode{ID: "candidate_screening", OutputSchema: candidateScreeningSchema()}
	if _, _, ok := mergeTargetedAIRepair(detail, step, node, AIExecution{OutputText: "```json\n" + baseline + "\n```"}, errors.New("invalid")); ok {
		t.Fatal("targeted repair accepted a baseline from a different input")
	}
}
