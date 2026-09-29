package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requirementReviewFixture() (json.RawMessage, map[string]any) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["_userRevision"] = map[string]any{"changes": []string{"交付稿不写内部证据流程用语。"}, "reason": "历史理由", "priorOutput": map[string]any{"markdown": "旧交付稿"}}
	stage["acceptanceCriteria"] = []string{"报告包含当前结果"}
	stage["researchContract"] = map[string]any{"requirement": "报告包含当前结果"}
	f := output["reviewFindings"].([]trackedReviewFinding)[0]
	stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-2-", Findings: []trackedReviewFinding{f}}
	f.Status, f.Origin, f.CorrectionIndex = "resolved", "existing", -1
	f.ChangeReason = "当前交付稿已删除内部流程用语"
	f.Basis = []reviewBasis{{"/_userRevision/changes/0", "交付稿不写内部证据流程用语。"}, {"/context/markdown", "Result: 5."}}
	output["reviewFindings"], output["approved"], output["numericIssues"], output["requiredCorrections"] = []trackedReviewFinding{f}, true, []string{}, []string{}
	return mustJSON(stage), output
}

func TestReviewRequirementNeedsCurrentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		basis []reviewBasis
		want  string
	}{
		{"requirement plus deliverable", []reviewBasis{{"/_userRevision/changes/0", "交付稿不写内部证据流程用语。"}, {"/context/markdown", "Result: 5."}}, ""},
		{"requirement only", []reviewBasis{{"/_userRevision/changes/0", "交付稿不写内部证据流程用语。"}}, "仅引用了验收/返修要求"},
		{"resolved without deliverable", []reviewBasis{{"/_userRevision/changes/0", "交付稿不写内部证据流程用语。"}, {"/computedResults/mean", "3"}}, "必须同时引用 /context"},
		{"prior output", []reviewBasis{{"/_userRevision/priorOutput/markdown", "旧交付稿"}}, "依据路径不允许"},
		{"reason", []reviewBasis{{"/_userRevision/reason", "历史理由"}}, "依据路径不允许"},
		{"history", []reviewBasis{{"/reviewHistory/issuePrefix", "issue-2-"}}, "依据路径不允许"},
		{"missing", []reviewBasis{{"/_userRevision/changes/5", "交付稿不写内部证据流程用语。"}}, "依据路径不存在"},
		{"mismatch", []reviewBasis{{"/_userRevision/changes/0", "交付稿需要写内部证据流程用语。"}}, "引文不匹配"},
		{"object", []reviewBasis{{"/context", "Result: 5."}}, "指向对象或数组"},
		{"contract only", []reviewBasis{{"/researchContract/requirement", "报告包含当前结果"}}, "仅引用了验收/返修要求"},
		{"criteria only", []reviewBasis{{"/acceptanceCriteria/0", "报告包含当前结果"}}, "仅引用了验收/返修要求"},
		{"numeric mismatch", []reviewBasis{{"/computedResults/mean", "3.0"}}, "引文不匹配"},
		{"scientific evidence alone cannot prove resolved", []reviewBasis{{"/computedResults/mean", "3"}}, "必须同时引用 /context"},
		{"scientific evidence plus current deliverable", []reviewBasis{{"/computedResults/mean", "3"}, {"/context/markdown", "Result: 5."}}, ""},
		{"bad escape", []reviewBasis{{"/context/bad~2escape", "x"}}, "路径转义无效"},
		{"noncanonical index", []reviewBasis{{"/_userRevision/changes/00", "交付稿不写内部证据流程用语。"}}, "下标无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, output := requirementReviewFixture()
			output["reviewFindings"].([]trackedReviewFinding)[0].Basis = tc.basis
			err := validateTrackedReview(mustJSON(output), input)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestOpenRequirementFindingStillNeedsScientificOrDeliverableBasis(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["_userRevision"] = map[string]any{"changes": []string{"Check the result"}}
	f := &output["reviewFindings"].([]trackedReviewFinding)[0]
	f.Basis = []reviewBasis{{"/_userRevision/changes/0", "Check the result"}, {"/computedResults/mean", "3"}}
	if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err != nil {
		t.Fatal(err)
	}
	f.Basis = f.Basis[:1]
	if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err == nil {
		t.Fatal("requirement became fact")
	}
}

func TestRetryReviewPromptAddsRequirementPolicyWithoutChangingFrozenSchema(t *testing.T) {
	input, _ := requirementReviewFixture()
	node := CompiledNode{ID: "independent_review", PromptVersion: "frozen-before-fix", OutputSchema: trackedResearchReviewSchema()}
	beforeSchema, beforeInput := string(node.OutputSchema), string(input)
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: input, Attempt: 4}, node)
	for _, text := range []string{"/_userRevision/changes/<zero-based index>", "never as scientific evidence", "current deliverable /context is mandatory", "Do not cite other _userRevision fields"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("missing current host policy: %s", text)
		}
	}
	if beforeSchema != string(node.OutputSchema) || beforeInput != string(input) {
		t.Fatal("mutated frozen contract/input")
	}
}

// Optional replay of private, read-only-exported production drafts. Never
// rewrite the draft or mark the real task complete to make a replay pass.
func TestReviewRequirementLocalReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_REQUIREMENT_REPLAY")
	if dir == "" {
		t.Skip("no private fixture supplied")
	}
	input, err := os.ReadFile(filepath.Join(dir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []string{"2", "3", "4"} {
		output, err := os.ReadFile(filepath.Join(dir, "turn-"+turn+".json"))
		if err != nil {
			t.Fatal(err)
		}
		err = validateTrackedReview(output, input)
		if turn == "4" {
			if err == nil || !strings.Contains(err.Error(), "仅引用了验收/返修要求") {
				t.Fatalf("turn %s should still reject requirement-only closure: %v", turn, err)
			}
		} else if err != nil {
			t.Fatalf("turn %s: %v", turn, err)
		}
		t.Logf("turn %s: %v", turn, err)
	}
}
