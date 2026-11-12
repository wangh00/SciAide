package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/reviewcontract"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func computationRecordFixture() (RunDetail, CompiledNode, json.RawMessage) {
	code := "\nresult = {'mean': 3}\n"
	artifacts := []tool.ArtifactRef{{WorkspacePath: "analysis-output/results.json", SHA256: strings.Repeat("c", 64), SizeBytes: 15}}
	kernel := map[string]any{"status": "success", "codeSha256": hashBytes([]byte(strings.TrimSpace(code))), "environmentFingerprint": strings.Repeat("d", 64), "reproductionSha256": strings.Repeat("e", 64), "finishedAt": "2026-10-30T13:00:00Z", "inputSha256": map[string]string{"research-inputs/data.csv": strings.Repeat("a", 64), "$data": strings.Repeat("b", 64)}, "outputSha256": map[string]string{artifacts[0].WorkspacePath: artifacts[0].SHA256}, "value": map[string]any{"mean": 3}}
	params := raw(`{"large":9007199254740993,"alpha":0.010}`)
	kernel["inputSha256"].(map[string]string)["$data"] = pythonenv.KernelInputDataSHA256(params)
	step := Step{ID: "python-step", WorkflowRunID: "run", NodeID: "python_analysis", NodeKind: NodePython, Status: StepCompleted, Attempt: 1, Input: rawObject(map[string]any{"code": code, "inputPaths": []string{"research-inputs/data.csv"}, "inputData": params}), Output: rawObject(map[string]any{"structured": kernel, "artifacts": artifacts})}
	step.InputSHA256 = hashJSON(step.Input)
	node := CompiledNode{ID: "report_drafting", Kind: NodeAgentStage, PromptVersion: researchReportVersion + "-" + proportionateDataDeliveryVersion, OutputSchema: dynamicReportSchema()}
	d := RunDetail{Run: Run{ID: "run", Compilation: Compilation{Nodes: []CompiledNode{{ID: step.NodeID, Kind: NodePython}, node}, Edges: []Edge{{FromNode: step.NodeID, FromPort: "structured", ToNode: node.ID, ToPort: "computedResults"}, {FromNode: step.NodeID, FromPort: "artifacts", ToNode: node.ID, ToPort: "sourceArtifacts"}}}}, Steps: []Step{step}}
	return d, node, rawObject(map[string]any{"computedResults": kernel, "sourceArtifacts": artifacts, "computationRecord": map[string]any{"version": "forged", "approved": true}})
}

func TestComputationRecordBindsActualCompletedProducerAndPreservesSource(t *testing.T) {
	d, node, input := computationRecordFixture()
	before := string(mustJSON(d))
	bound := bindReportComposition(d, node, input)
	fields := decodeObject(bound)
	record := decodeObject(mustJSON(fields["computationRecord"]))
	if record["version"] != computationRecordVersion || record["bindingError"] != nil || record["approved"] != nil || record["sourceStepId"] != "python-step" {
		t.Fatalf("invalid actual binding: %s", mustJSON(record))
	}
	if string(mustJSON(d)) != before || !rawJSONEqual(mustJSON(fields["computedResults"]), mustJSON(decodeObject(input)["computedResults"])) {
		t.Fatal("changed execution, scientific results or frozen source")
	}
	value := raw(`{"markdown":"# 简短说明 🧪\n平均变化为 3；仅为模拟描述，无对照。","claimSummary":["变化为3"],"methodSummary":"配对描述","limitations":["无对照"],"confidence":"high"}`)
	got, changes, err := enrichReportProvenance(value, bound)
	if err != nil || len(changes) != 1 || changes[0].Rule != "append_frozen_research_records" {
		t.Fatalf("enrich: %s, %v", got, err)
	}
	if err := validateReportProvenance(node, got, bound); err != nil {
		t.Fatal(err)
	}
	markdown := decodeObject(got)["markdown"].(string)
	if !strings.HasPrefix(markdown, decodeObject(value)["markdown"].(string)+"\n\n") || !strings.Contains(markdown, "analysis-output/results.json") || !strings.Contains(markdown, "程序管理的记录附录") || !strings.Contains(markdown, strings.Repeat("d", 64)) {
		t.Fatal(markdown)
	}
	// A persisted input can reproduce the same appendix without current files,
	// model calls, host clock, or running Python again.
	var saved json.RawMessage
	if json.Unmarshal(mustJSON(bound), &saved) != nil {
		t.Fatal("cannot reload frozen input")
	}
	if again, changes, err := enrichReportProvenance(got, saved); err != nil || len(changes) != 0 || !rawJSONEqual(again, got) {
		t.Fatal("restart duplicated or changed the report", err)
	}
}

func TestComputationRecordRejectsStaleFailedAndForeignExecution(t *testing.T) {
	for _, tc := range []string{"attempt", "status", "workflow", "input_hash", "code", "computed_result", "artifact_hash", "missing_artifacts", "duplicate_artifacts", "absolute_path"} {
		t.Run(tc, func(t *testing.T) {
			d, node, input := computationRecordFixture()
			fields := decodeObject(input)
			switch tc {
			case "attempt":
				d.Steps[0].Attempt = 0
			case "status":
				d.Steps[0].Status = StepFailed
			case "workflow":
				d.Steps[0].WorkflowRunID = "foreign"
			case "input_hash":
				d.Steps[0].InputSHA256 = "stale"
			case "code":
				d.Steps[0].Input = raw(`{"code":"result = 99"}`)
				d.Steps[0].InputSHA256 = hashJSON(d.Steps[0].Input)
			case "computed_result":
				fields["computedResults"].(map[string]any)["value"] = map[string]any{"mean": 99}
			case "missing_artifacts":
				fields["sourceArtifacts"] = []any{}
			default:
				out := decodeObject(d.Steps[0].Output)
				artifacts := out["artifacts"].([]any)
				if tc == "artifact_hash" {
					artifacts[0].(map[string]any)["sha256"] = strings.Repeat("f", 64)
				}
				if tc == "duplicate_artifacts" {
					artifacts = append(artifacts, artifacts[0])
				}
				if tc == "absolute_path" {
					artifacts[0].(map[string]any)["workspacePath"] = "/outside/results.json"
				}
				out["artifacts"] = artifacts
				fields["sourceArtifacts"] = artifacts
				d.Steps[0].Output = mustJSON(out)
			}
			bound := bindReportComposition(d, node, mustJSON(fields))
			if _, err := reportComputationAppendix(bound); err == nil {
				t.Fatal("unverified execution record accepted")
			}
		})
	}
}

func TestComputationRecordMatchesExactFrozenParametersAndInputSet(t *testing.T) {
	for _, tc := range []string{"parameters", "missing_input", "extra_input", "content_hash", "environment", "large_result", "decimal_result"} {
		t.Run(tc, func(t *testing.T) {
			d, node, input := computationRecordFixture()
			in := decodeObject(d.Steps[0].Input)
			out := decodeObject(d.Steps[0].Output)
			kernel := out["structured"].(map[string]any)
			hashes := kernel["inputSha256"].(map[string]any)
			fields := decodeObject(input)
			switch tc {
			case "parameters":
				in["inputData"] = map[string]any{"large": json.Number("9007199254740994"), "alpha": json.Number("0.010")}
			case "missing_input":
				delete(hashes, "research-inputs/data.csv")
			case "extra_input":
				hashes["other.csv"] = strings.Repeat("f", 64)
			case "content_hash":
				path := "research-inputs/data-" + strings.Repeat("f", 64) + ".csv"
				in["inputPaths"] = []string{path}
				hashes[path] = hashes["research-inputs/data.csv"]
				delete(hashes, "research-inputs/data.csv")
			case "environment":
				in["expectedEnvironmentFingerprint"] = strings.Repeat("f", 64)
			case "large_result", "decimal_result":
				left, right := "9007199254740992", "9007199254740993"
				if tc == "decimal_result" {
					left, right = "0.10000000000000001", "0.10000000000000002"
				}
				kernel["value"] = map[string]any{"mean": json.Number(left)}
				copy := decodeObject(mustJSON(kernel))
				copy["value"] = map[string]any{"mean": json.Number(right)}
				fields["computedResults"] = copy
			}
			if tc != "large_result" && tc != "decimal_result" {
				fields["computedResults"] = kernel
			}
			d.Steps[0].Input = mustJSON(in)
			d.Steps[0].InputSHA256 = hashJSON(d.Steps[0].Input)
			d.Steps[0].Output = mustJSON(out)
			if _, err := reportComputationAppendix(bindReportComposition(d, node, mustJSON(fields))); err == nil {
				t.Fatal("changed exact input or result accepted")
			}
		})
	}
}

func TestComputationRecordKeepsOriginalAuthoredLimitAndExactSuffixCapacity(t *testing.T) {
	d, node, input := computationRecordFixture()
	bound := bindReportComposition(d, node, input)
	for _, length := range []int{180000, 180001} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			value := rawObject(map[string]any{"markdown": strings.Repeat("x", length), "claimSummary": []string{}, "methodSummary": "配对描述", "limitations": []string{}, "confidence": "low"})
			got, _, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, bound)
			if length > 180000 {
				if err == nil {
					t.Fatal("host capacity leaked to authored body")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := validateReportProvenance(node, got, bound); err != nil {
				t.Fatal("legal authored body rejected after host appendix", err)
			}
			if again, _, err := normalizeWorkflowAIStageSubmissionForInput(string(got), node, bound); err != nil || !reviewcontract.Equal(again, got) {
				t.Fatal("exact assembled report cannot be resubmitted", err)
			}
			fields := decodeObject(got)
			fields["markdown"] = strings.Replace(fields["markdown"].(string), "环境指纹：", "环境指纹（已改）：", 1)
			if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(fields)), node, bound); err == nil {
				t.Fatal("altered suffix gained host capacity")
			}
		})
	}
}

func TestComputationRecordParsingByteBoundaryAndEmptyBody(t *testing.T) {
	d, node, input := computationRecordFixture()
	bound := bindReportComposition(d, node, input)
	fields := map[string]any{"markdown": strings.Repeat("文", 42000), "claimSummary": []string{}, "methodSummary": "methods", "limitations": []string{}, "confidence": "low"}
	fields["markdown"] = fields["markdown"].(string) + strings.Repeat("x", 262000-len(mustJSON(fields)))
	original := mustJSON(fields)
	if len(original) > 256*1024 {
		t.Fatal("fixture exceeds original JSON bound")
	}
	got, _, err := normalizeWorkflowAIStageSubmissionForInput(string(original), node, bound)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= 256*1024 {
		t.Fatal("fixture does not exercise added record byte capacity")
	}
	for _, text := range []string{string(got), "```json\n" + string(got) + "\n```", "``` json\n" + string(got) + "\n```", "```JSON\r\n" + string(got) + "\r\n```", "Here is the result:\n" + string(got)} {
		if again, _, err := normalizeWorkflowAIStageSubmissionForInput(text, node, bound); err != nil || !reviewcontract.Equal(again, got) {
			t.Fatal("exact saved record cannot be replayed at JSON byte boundary", err)
		}
	}
	// JSON escape choices must not change original source byte accounting.
	var rawFields map[string]json.RawMessage
	if json.Unmarshal(got, &rawFields) != nil {
		t.Fatal("invalid fixture JSON")
	}
	var markdown string
	_ = json.Unmarshal(rawFields["markdown"], &markdown)
	var originalFields map[string]json.RawMessage
	_ = json.Unmarshal(original, &originalFields)
	var authored string
	_ = json.Unmarshal(originalFields["markdown"], &authored)
	sectionEscaped, _ := computationRecordSection(bound)
	// Keep the large authored prefix as UTF-8, but escape every appendix rune.
	prefix, _ := json.Marshal(authored)
	quoted := string(prefix[:len(prefix)-1]) + strings.TrimSuffix(strings.TrimPrefix(strconv.QuoteToASCII("\n\n"+sectionEscaped), "\""), "\"") + "\""
	rawFields["markdown"] = json.RawMessage(quoted)
	var escaped bytes.Buffer
	encoder := json.NewEncoder(&escaped)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(rawFields); err != nil {
		t.Fatal(err)
	}
	if again, _, err := normalizeWorkflowAIStageSubmissionForInput(escaped.String(), node, bound); err != nil || !reviewcontract.Equal(again, got) {
		t.Fatal("equivalent escaped appendix rejected", err)
	}
	// Do not compact away untrusted whitespace to let an oversized original
	// candidate borrow capacity from the appendix's encoding choices.
	var direct bytes.Buffer
	encoder = json.NewEncoder(&direct)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(decodeObject(got)); err != nil {
		t.Fatal(err)
	}
	directJSON := bytes.TrimSpace(direct.Bytes())
	padding := maxStageJSONCandidateBytes + 1 - len(original)
	oversized := append([]byte{'{'}, bytes.Repeat([]byte{' '}, padding)...)
	oversized = append(oversized, directJSON[1:]...)
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(oversized), node, bound); err == nil {
		t.Fatal("original raw candidate byte ceiling bypassed by record encoding")
	}
	section, err := computationRecordSection(bound)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{section, "unseparated" + section} {
		fields["markdown"] = body
		if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(fields)), node, bound); err == nil {
			t.Fatal("record substituted for authored report or separator")
		}
	}
	fields["markdown"] = strings.Repeat("文", 42000) + strings.Repeat("x", 140000) + "\n\n" + section
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(fields)), node, bound); err == nil {
		t.Fatal("host suffix bypassed original body byte/character ceiling")
	}
}

func TestComputationRecordJSONRuneBoundaryPreservesEncoding(t *testing.T) {
	for _, token := range []string{`"a🧪中\nquote\"\\x"`, `"a\ud83e\uddea\u4e2d\nquote\"\\x"`, `"\ud800\ud83e\uddea\udfff"`} {
		var decoded string
		if json.Unmarshal([]byte(token), &decoded) != nil {
			t.Fatal("invalid fixture")
		}
		runes := []rune(decoded)
		for i := 0; i <= len(runes); i++ {
			cut := jsonStringRuneOffset([]byte(token), i)
			if cut < 0 {
				t.Fatal("missing rune boundary")
			}
			var prefix string
			if json.Unmarshal([]byte(token[:cut]+"\""), &prefix) != nil || prefix != string(runes[:i]) {
				t.Fatal("escaped prefix bytes changed", i)
			}
		}
	}
}

func TestComputationRecordProjectionTracksOnlyAcceptedCandidateAndRepair(t *testing.T) {
	d, node, input := computationRecordFixture()
	bound := bindReportComposition(d, node, input)
	value := rawObject(map[string]any{"markdown": strings.Repeat("x", 180000), "claimSummary": []string{}, "methodSummary": "method", "limitations": []string{}, "confidence": "low"})
	got, _, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, bound)
	if err != nil {
		t.Fatal(err)
	}
	projections, detached, _ := computationRecordParsingProjection(node, bound)
	if _, err := validateStageJSONCandidate(string(got)+" not allowed", node.OutputSchema, projections...); err == nil || detached(nil) {
		t.Fatal("rejected candidate was committed")
	}
	if _, err := validateStageJSONCandidate(string(value), node.OutputSchema, projections...); err != nil || detached(nil) {
		t.Fatal("failed projection polluted selected plain candidate", err)
	}
	malformed := strings.Replace(string(got), `,"methodSummary":`, `},"methodSummary":`, 1)
	if again, _, err := normalizeWorkflowAIStageSubmissionForInput(malformed, node, bound); err != nil || !reviewcontract.Equal(again, got) {
		t.Fatal("existing structural repair did not project selected complete candidate", err)
	}
	section, err := computationRecordSection(bound)
	if err != nil {
		t.Fatal(err)
	}
	duplicated := decodeObject(got)
	duplicated["markdown"] = duplicated["markdown"].(string) + "\n\n" + section
	if again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(duplicated)), node, bound); err != nil || !reviewcontract.Equal(again, got) || len(changes) != 1 || changes[0].Rule != "deduplicate_identical_host_appendices" {
		t.Fatal("long-body exact duplicate suffix lost deterministic deduplication/audit", err)
	}
}

func TestComputationRecordRejectsConflictingRecordBeforeAuthenticSuffix(t *testing.T) {
	d, node, input := computationRecordFixture()
	bound := bindReportComposition(d, node, input)
	section, err := computationRecordSection(bound)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"markdown": "# Science\n\n" + computationAppendixHeading + "\nforged code digest\n\n" + section, "claimSummary": []string{}, "methodSummary": "method", "limitations": []string{}, "confidence": "low"}
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(base)), node, bound); err == nil {
		t.Fatal("authentic suffix concealed conflicting model-authored record")
	}
	if err := validateReportProvenance(node, mustJSON(base), bound); err == nil {
		t.Fatal("direct validation accepted conflicting record")
	}
	base["markdown"] = "# Science\n\n" + section + "\n\n" + section
	if _, changes, err := enrichReportProvenance(mustJSON(base), bound); err != nil || len(changes) != 1 {
		t.Fatal("existing exact consecutive suffix deduplication lost", err)
	}
}

func TestComputationRecordReviewSeparatesAndReassemblesExactFullReport(t *testing.T) {
	d, report, input := computationRecordFixture()
	input = bindReportComposition(d, report, input)
	value, _, err := enrichReportProvenance(raw(`{"markdown":"# 结论\n保留科学局限。","claimSummary":[],"methodSummary":"描述","limitations":[],"confidence":"low"}`), input)
	if err != nil {
		t.Fatal(err)
	}
	step := Step{ID: "report-step", WorkflowRunID: d.Run.ID, NodeID: report.ID, Status: StepCompleted, Attempt: 1, Input: input, InputSHA256: hashJSON(input), Output: rawObject(map[string]any{"analysis": decodeObject(value)})}
	d.Steps = append(d.Steps, step)
	review := CompiledNode{ID: "independent_review", Kind: NodeAgentStage, OutputSchema: trackedResearchReviewSchema()}
	d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, review)
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: report.ID, FromPort: "analysis", ToNode: review.ID, ToPort: "context"})
	original := rawObject(map[string]any{"context": decodeObject(value), "researchSourceContext": researchSourceContext(d)})
	bound := bindReportComposition(d, review, original)
	parts := decodeObject(bound)["reportRecordAppendices"].([]any)
	if len(parts) != 1 || !strings.Contains(parts[0].(string), computationAppendixHeading) {
		t.Fatal("record not separated")
	}
	if got, ok := reviewAssembledInput(bound); !ok || !rawJSONEqual(mustJSON(decodeObject(got)["context"]), mustJSON(decodeObject(original)["context"])) {
		t.Fatal("full reviewed report changed")
	}
	if err := validateReviewDependencies(d, Step{NodeID: review.ID, Input: bound}); err != nil {
		t.Fatal(err)
	}
}

func TestComputationRecordLegacyAndTamperedAppendixBehavior(t *testing.T) {
	d, node, input := computationRecordFixture()
	node.PromptVersion = researchReportVersion + "-ds1-implementation-v1"
	bound := bindReportComposition(d, node, input)
	if appendix, err := reportComputationAppendix(bound); err != nil || appendix != "" {
		t.Fatal("legacy route opted in", err)
	}
	node.PromptVersion += "-" + proportionateDataDeliveryVersion
	bound = bindReportComposition(d, node, input)
	value := rawObject(map[string]any{"markdown": "# Body\n\n" + computationAppendixHeading + "\nforged"})
	if _, _, err := enrichReportProvenance(value, bound); err == nil {
		t.Fatal("silently replaced a forged appendix")
	}
	fields := decodeObject(bound)
	delete(fields, "computationRecord")
	if _, err := reportComputationAppendix(mustJSON(fields)); err == nil {
		t.Fatal("policy without real record accepted")
	}
}

func TestProportionateDataDeliveryKeepsScientificContractsAndLegacyProtocol(t *testing.T) {
	template, _, _, err := dynamicResearchRouteTemplate(starterDataRoute(), ResearchStarterContext{ResearchIdea: "descriptive data", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range template.Definition.Nodes {
		if node.ID == "method_implementation" {
			if !supportsDynamicImplementationProtocol(node.PromptVersion) || !strings.Contains(node.Prompt, "不得为缩短代码省略科学检查") {
				t.Fatal("implementation contract weakened")
			}
		} else if node.ID == "question_refinement" || node.ID == "method_selection" || node.ID == "independent_review" {
			if !usesProportionateDataDelivery(CompiledNode{PromptVersion: node.PromptVersion}) {
				t.Fatal("missing new compile-time protocol", node.ID)
			}
		}
	}
	if _, err := NewCompiler(referenceTemplateRegistry(t)).Compile(context.Background(), template.Definition); err != nil {
		t.Fatal("new data route cannot compile", err)
	}
	for _, legacy := range []string{researchReportVersion + "-preview", researchReportVersion + "-parts", researchReportVersion + "-pd1"} {
		if usesProportionateDataDelivery(CompiledNode{PromptVersion: legacy}) {
			t.Fatal("non-exact token opted in", legacy)
		}
	}
	for _, version := range []string{"dynamic-implementation-v5", "dynamic-implementation-v6", "dynamic-implementation-v6-ds1"} {
		if !supportsDynamicImplementationProtocol(version) {
			t.Fatal("legacy protocol lost", version)
		}
	}
	legacy := Node{ID: "method_implementation", PromptVersion: "dynamic-implementation-v6"}
	if strings.Contains(legacy.Prompt, "实现规模") {
		t.Fatal("legacy mutated")
	}
}
