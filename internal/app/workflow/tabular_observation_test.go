package workflow

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runTabularObservationFixture(t *testing.T, body, extension, prefix string) map[string]any {
	t.Helper()
	python, err := exec.LookPath("python")
	if err != nil {
		t.Fatal("Python required for tabular observation fixture")
	}
	dir := t.TempDir()
	input, output := filepath.Join(dir, "input"+extension), filepath.Join(dir, "preflight.json")
	if err := os.WriteFile(input, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	inputs, outputs := mustJSON([]string{input}), mustJSON([]string{output})
	code := dynamicDataPreflightCode
	if prefix != "" {
		// Inject only diagnostic budgets, not a shape/quality condition.
		code = strings.Replace(code, "\nclass EqualityObservation:", "\n"+prefix+"\nclass EqualityObservation:", 1)
	}
	log, err := exec.Command(python, "-c", "SCIAIDE_INPUTS="+string(inputs)+"\nSCIAIDE_OUTPUTS="+string(outputs)+"\n"+code).CombinedOutput()
	if err != nil {
		t.Fatalf("preflight execution: %v %s", err, log)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(input); err != nil || !bytes.Equal(actual, []byte(body)) {
		t.Fatal("preflight changed the original records", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTabularObservationFindsRepeatedMeasurementsHiddenByUniqueLabels(t *testing.T) {
	for _, extension := range []string{".csv", ".tsv"} {
		t.Run(extension, func(t *testing.T) {
			body := "row_label,partition,measure,score\n001,train,2.0,5\n002,train,2.0,5\n003,test,3.0,6\n004,test,3.0,6\n005,test,4.0,7\n"
			if extension == ".tsv" {
				body = strings.ReplaceAll(body, ",", "\t")
			}
			summary := runTabularObservationFixture(t, body, extension, "")
			audit := summary["tabularEqualityAudit"].(map[string]any)
			full := audit["fullRows"].(map[string]any)
			projection := audit["withoutFullyUniqueColumns"].(map[string]any)
			if full["repeatedGroupCount"] != float64(0) || projection["repeatedGroupCount"] != float64(2) || projection["recordsInRepeatedGroups"] != float64(4) || projection["additionalRepeatedRecordCount"] != float64(2) || projection["distinctRecordCount"] != float64(3) || projection["complete"] != true {
				t.Fatalf("masked repeats not observed: %s", mustJSON(summary))
			}
			if string(mustJSON(audit["fullyUniqueColumnIndices"])) != "[0]" || audit["identityVerified"] != false || !strings.Contains(audit["interpretation"].(string), "never an adopted grouping") {
				t.Fatal("diagnostic projection advertised scientific identity")
			}
		})
	}
}

func TestTabularObservationDoesNotTurnMissingEvidenceIntoZero(t *testing.T) {
	for _, tc := range []struct {
		name, body, budget string
		fullComplete       bool
	}{
		{"no unique columns", "x,y\n1,2\n1,2\n", "", true},
		{"all columns unique", "x,y\n1,2\n3,4\n", "", true},
		{"one record", "x,y\n1,2\n", "", true},
		{"diagnostic key limit", "id,x,y\na,1,2\nb,3,4\nc,5,6\nd,7,8\n", "AUDIT_KEY_LIMIT = 2", false},
		{"diagnostic record limit with few distinct keys", "x,y\n1,2\n1,2\n1,2\n1,2\n", "AUDIT_RECORD_LIMIT = 2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := runTabularObservationFixture(t, tc.body, ".csv", tc.budget)
			audit := summary["tabularEqualityAudit"].(map[string]any)
			full := audit["fullRows"].(map[string]any)
			if full["complete"] != tc.fullComplete || summary["shapeCheckedAllRows"] != true || len(summary["preview"].([]any)) == 0 {
				t.Fatal("optional budget became a preflight failure", summary)
			}
			if !tc.fullComplete {
				if _, exists := full["repeatedGroupCount"]; exists {
					t.Fatal("incomplete equality audit claims a duplicate count")
				}
				if summary["rowCount"] != float64(4) || audit["uniqueColumnScanComplete"] != false {
					t.Fatal("bounded observation did not preserve full-file shape scan")
				}
			}
			projection := audit["withoutFullyUniqueColumns"].(map[string]any)
			if projection["complete"] != false {
				t.Fatal("empty/no-informative projection treated as observed no repeats")
			}
		})
	}
}

func TestTabularObservationRetainsStringEqualityAndQuotedCells(t *testing.T) {
	body := "id,note,x\na,\"long\ncell\",1\nb,\"long\ncell\",1\nc,\"long\ncell\",01\nd,\"long\ncell\",1.0\n"
	summary := runTabularObservationFixture(t, body, ".csv", "")
	audit := summary["tabularEqualityAudit"].(map[string]any)
	projection := audit["withoutFullyUniqueColumns"].(map[string]any)
	if summary["rowCount"] != float64(4) || summary["preview"].([]any)[0].([]any)[1] != "long\ncell" || projection["distinctRecordCount"] != float64(3) || projection["additionalRepeatedRecordCount"] != float64(1) {
		t.Fatal("numeric coercion or newline corruption altered structural observation", summary)
	}
}

func TestTabularObservationWideAndUniqueMeasurementsRemainDiagnostic(t *testing.T) {
	header, first, second := []string{}, []string{}, []string{}
	for i := 0; i < 257; i++ {
		header = append(header, "field"+string(mustJSON(i)))
		first = append(first, "a")
		second = append(second, "a")
	}
	body := strings.Join(header, ",") + "\n" + strings.Join(first, ",") + "\n" + strings.Join(second, ",") + "\n"
	summary := runTabularObservationFixture(t, body, ".csv", "")
	audit := summary["tabularEqualityAudit"].(map[string]any)
	if summary["columnCount"] != float64(257) || audit["uniqueColumnScanComplete"] != false || audit["fullRows"].(map[string]any)["additionalRepeatedRecordCount"] != float64(1) {
		t.Fatal("wide audit fabricated complete column scan or lost full-row comparison")
	}
	// A genuinely measured, unique continuous value is only a diagnostic
	// exclusion candidate, never automatically an identifier or group rule.
	summary = runTabularObservationFixture(t, "reading,group\n1.01,A\n1.02,A\n2.03,B\n2.04,B\n", ".csv", "")
	audit = summary["tabularEqualityAudit"].(map[string]any)
	if audit["identityVerified"] != false || audit["withoutFullyUniqueColumns"].(map[string]any)["repeatedGroupCount"] != float64(2) || !strings.Contains(audit["interpretation"].(string), "genuine measurements") {
		t.Fatal("unique measurement is being semantically removed")
	}
}

func TestTabularObservationBoundsWideProjectionMetadataAndDoesNotExcludeMissingColumns(t *testing.T) {
	header, first, second := []string{"row_label"}, []string{"a"}, []string{"b"}
	for i := 0; i < 300; i++ {
		header = append(header, "field"+string(mustJSON(i)))
		first = append(first, "same")
		second = append(second, "same")
	}
	summary := runTabularObservationFixture(t, strings.Join(header, ",")+"\n"+strings.Join(first, ",")+"\n"+strings.Join(second, ",")+"\n", ".csv", "")
	audit := summary["tabularEqualityAudit"].(map[string]any)
	projection := audit["withoutFullyUniqueColumns"].(map[string]any)
	if audit["uniqueColumnScanComplete"] != false || projection["retainedColumnCount"] != float64(300) || projection["retainedColumnIndicesComplete"] != false || len(projection["retainedColumnIndices"].([]any)) != 256 || projection["additionalRepeatedRecordCount"] != float64(1) {
		t.Fatal("wide diagnostic metadata is unbounded or hides incomplete variable view", projection)
	}
	summary = runTabularObservationFixture(t, "id,missing,reading\na,,1\nb, ,1\nc,2,2\n", ".csv", "")
	audit = summary["tabularEqualityAudit"].(map[string]any)
	if string(mustJSON(audit["fullyUniqueColumnIndices"])) != "[0]" || audit["uniqueColumnScanComplete"] != true {
		t.Fatal("missing values became a valid fully unique column", audit)
	}
}

func TestCapturedWineTabularObservationReplaysActualDuplicateCounts(t *testing.T) {
	// Checked-in material only; never inspect/mutate the formal task database.
	path := filepath.Join("..", "..", "..", "examples", "research-cases", "02-data", "materials", "initial", "wine-red-initial-1000.csv")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	summary := runTabularObservationFixture(t, string(body), ".csv", "")
	audit := summary["tabularEqualityAudit"].(map[string]any)
	full, projection := audit["fullRows"].(map[string]any), audit["withoutFullyUniqueColumns"].(map[string]any)
	if summary["rowCount"] != float64(1000) || full["repeatedGroupCount"] != float64(0) || string(mustJSON(audit["fullyUniqueColumnIndices"])) != "[0]" || projection["repeatedGroupCount"] != float64(140) || projection["recordsInRepeatedGroups"] != float64(289) || projection["additionalRepeatedRecordCount"] != float64(149) || projection["distinctRecordCount"] != float64(851) {
		t.Fatalf("actual masked duplicates not reproduced: %s", mustJSON(audit))
	}
}

func TestComputationalAuditGuidesRelevantStagesWithoutChangingFrozenContracts(t *testing.T) {
	detail := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{{ID: "python_analysis", Kind: NodePython}}}}}
	for _, id := range []string{"method_selection", "method_implementation", "result_interpretation", "report_drafting", "independent_review"} {
		t.Run(id, func(t *testing.T) {
			schema := dynamicMethodSchema()
			if id == "method_implementation" {
				schema = dynamicImplementationSchema()
			} else if id == "report_drafting" {
				schema = dynamicReportSchema()
			} else if id == "independent_review" {
				schema = trackedResearchReviewSchema()
			}
			node := CompiledNode{ID: id, Kind: NodeAgentStage, PromptVersion: "fixture-v1", OutputSchema: schema}
			step := Step{Input: raw(`{"context":{"markdown":"Frozen report"}}`)}
			step.InputSHA256 = hashJSON(step.Input)
			before := mustJSON(map[string]any{"detail": detail, "node": node, "step": step})
			prompt := buildAIStagePrompt(detail, step, node)
			if !strings.Contains(prompt, computationAuditInstruction) || !strings.Contains(prompt, "<stage_input>\n"+string(step.Input)+"\n</stage_input>") || !strings.Contains(reportExecutionPromptVersion(node, prompt), "-"+computationAuditVersion) {
				t.Fatal("computational guidance not delivered/auditable")
			}
			if !bytes.Equal(before, mustJSON(map[string]any{"detail": detail, "node": node, "step": step})) {
				t.Fatal("new execution guidance rewrote a frozen record")
			}
		})
	}
	for _, id := range []string{"explore", "question_refinement", "research_design"} {
		if computationalStageAudit(detail, Step{Input: raw(`{}`)}, CompiledNode{ID: id, Kind: NodeAgentStage, OutputSchema: dynamicMethodSchema()}) != "" {
			t.Fatal("data-specific guidance leaked into opening/design", id)
		}
	}
	for _, id := range []string{"method_selection", "report_drafting", "independent_review"} {
		node := CompiledNode{ID: id, Kind: NodeAgentStage, OutputSchema: dynamicMethodSchema()}
		if id == "independent_review" {
			node.OutputSchema = trackedResearchReviewSchema()
		}
		if computationalStageAudit(RunDetail{}, Step{Input: raw(`{"evidenceContext":[]}`)}, node) != "" {
			t.Fatal("literature-only route acquired data-analysis obligations")
		}
		if computationalStageAudit(RunDetail{}, Step{Input: raw(`{"computedResults":{"status":"success"}}`)}, node) == "" {
			t.Fatal("standalone computation review missed input channel")
		}
	}
	if computationalStageAudit(detail, Step{}, CompiledNode{ID: "method_implementation", Kind: NodePython}) != "" {
		t.Fatal("Python source received model instruction")
	}
}

func TestComputationAuditGuidancePreservesInferenceAndMinimalRepairBoundaries(t *testing.T) {
	for _, text := range []string{"does NOT prove", "row identifier", "including a unique row label", "predictor-only leakage key", "DIAGNOSTIC projection", "Incomplete/missing counts are unknown", "Do not automatically delete, merge, reweight or change train/test", "record-level resampling", "researchChanges/confirmation flow", "raw from adjusted p-values", "Pointwise intervals", "unused unadjusted supplemental value", "responsible upstream implementation", "not a new output schema"} {
		if !strings.Contains(computationAuditInstruction, text) {
			t.Error("scientific repair guard omitted:", text)
		}
	}
}

func TestComputationAuditVersionRemainsBoundedAndIdempotent(t *testing.T) {
	for _, id := range []string{"method_selection", "method_implementation", "result_interpretation", "report_drafting", "independent_review"} {
		node := CompiledNode{ID: id, Kind: NodeAgentStage, PromptVersion: strings.Repeat("v", 128), OutputSchema: dynamicImplementationSchema()}
		if id == "independent_review" {
			node.OutputSchema = trackedResearchReviewSchema()
		}
		prompt := computationAuditInstruction + implementationEfficiencyInstruction + implementationDecisionInstruction + reviewScopeInstruction + kernelReviewNavigationInstruction
		if id == "method_implementation" {
			prompt += ImplementationPartsInstruction + ImplementationPatchInstruction
		}
		version := reportExecutionPromptVersion(node, prompt)
		if len(version) > 128 || strings.Count(version, "-"+computationAuditVersion) != 1 {
			t.Fatal("new guidance identity cannot persist", version)
		}
		node.PromptVersion = version
		if reportExecutionPromptVersion(node, prompt) != version {
			t.Fatal("effective version not idempotent", version)
		}
	}
}

// Keep generated scientific contracts unchanged by the observability addition.
func TestTabularAuditIsNotANewOutputRequirement(t *testing.T) {
	if strings.Contains(string(dynamicImplementationSchema()), "tabularEqualityAudit") || strings.Contains(string(trackedResearchReviewSchema()), "tabularEqualityAudit") {
		t.Fatal("diagnostic became mandatory AI submission metadata")
	}
	if !json.Valid(mustJSON(map[string]any{"diagnostic": computationAuditVersion})) {
		t.Fatal("invalid navigation metadata")
	}
}
