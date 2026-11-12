package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestWideReviewSourceNavigationAndInputShareOriginalBudget(t *testing.T) {
	rows := []any{}
	for i := 0; i < 100; i++ {
		fields := map[string]any{}
		for j := 0; j < 25; j++ {
			fields[fmt.Sprintf("originalProviderCatalogAttribute%02d", j)] = fmt.Sprintf("record-%d-value-%d", i, j)
		}
		rows = append(rows, map[string]any{"savedOriginalProviderSnapshot": fields})
	}
	input := mustAgentJSON(t, map[string]any{"originalRequest": "Preserve the original human request.", "context": map[string]any{"markdown": "A short synthetic report."}, "researchSourceContext": map[string]any{"retrievedCandidateInventory": map[string]any{"savedRawSourceSnapshots": rows}}})
	prompt := strings.Repeat("Immutable host instruction. ", 1700) + "<stage_input>\n" + string(input) + "\n</stage_input>\n" + workflow.ReviewSourceInstruction("")
	assertReviewJointBudget(t, input, prompt, nil)
}

// The old capture is never resumed or rewritten. This constructs a distinct
// in-memory v2 contract using the identical saved input and only the host-owned
// instruction suffix; zero provider/model requests are made by this replay.
func TestCapturedReviewSourceNavigationAndInputShareOriginalBudget(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CONTEXT_REPLAY")
	if dir == "" {
		t.Skip("no read-only captured review supplied")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "captured-review-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Input            json.RawMessage `json:"input"`
		PromptText       string          `json:"prompt_text"`
		PromptSHA256     string          `json:"prompt_sha256"`
		InputSHA256      string          `json:"input_sha256"`
		OutputSchemaJSON string          `json:"output_schema_json"`
	}
	if json.Unmarshal(raw, &capture) != nil || capture.PromptSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(capture.PromptText))) || capture.InputSHA256 != fmt.Sprintf("%x", sha256.Sum256(capture.Input)) {
		t.Fatal("capture identity mismatch")
	}
	end := strings.LastIndex(capture.PromptText, "\n</stage_input>")
	if end < 0 || strings.Count(capture.PromptText[end:], workflow.ReviewSourceSubmissionInstruction) != 1 {
		t.Fatal("cannot identify the captured host-owned suffix")
	}
	prompt := capture.PromptText[:end] + strings.Replace(capture.PromptText[end:], workflow.ReviewSourceSubmissionInstruction, workflow.ReviewSourceInstruction(""), 1)
	assertReviewJointBudget(t, capture.Input, prompt, json.RawMessage(capture.OutputSchemaJSON))
}

func assertReviewJointBudget(t *testing.T, input json.RawMessage, prompt string, schema json.RawMessage) {
	t.Helper()
	if len(schema) == 0 {
		schema, _ = issueRepairFixture(t)
	}
	const limit = 380000
	originalInput, originalPrompt := string(input), prompt
	system := strings.Repeat("Actual fixed host overhead. ", 1800)
	for _, version := range []string{"review-source-v1", workflow.ReviewSourceProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			casePrompt := prompt
			if version == "review-source-v1" {
				index := strings.LastIndex(casePrompt, workflow.ReviewSourceInstruction(""))
				if index < 0 {
					t.Fatal("missing host instruction")
				}
				casePrompt = casePrompt[:index] + workflow.ReviewSourceSubmissionInstruction + casePrompt[index+len(workflow.ReviewSourceInstruction("")):]
			}
			contract := stageBudgetContract(input, casePrompt)
			contract.ID, contract.NodeID, contract.WorkflowStepID, contract.Attempt = "new-offline-execution", "independent_review", "review", 1
			contract.OutputSchema, contract.OutputSchemaSHA256 = schema, fmt.Sprintf("%x", sha256.Sum256(schema))
			contract.PromptVersion = "dynamic-independent-review-v9|" + version
			binding := workflow.ReviewSourceBinding{StageMaterialRequest: workflow.StageMaterialRequest{ProjectID: "project", ConversationID: "conversation", WorkflowRunID: contract.WorkflowRunID, WorkflowStepID: contract.WorkflowStepID, ChatRunID: "run", Attempt: 1, InputSHA256: contract.InputSHA256}, ExecutionID: contract.ID, PromptVersion: contract.PromptVersion, OutputSchemaSHA256: contract.OutputSchemaSHA256}
			catalog, err := workflow.BuildReviewSourceCatalog(t.Context(), input, binding)
			if err != nil {
				t.Fatal(err)
			}
			submission := &reviewSourceSubmission{catalog: catalog, fullSchema: workflow.ReviewSourceSubmissionSchema(workflow.ReviewSubmissionSchema(schema))}
			run := chat.Run{ID: "run", UserMessageID: "u"}
			messages := []conversation.Message{checkpointText("u", "run", casePrompt)}
			defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "Read current frozen input without executing research", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			projected := budgetedStageMessages(messages, run, contract, defs, nil, system, "", "", limit, 360000, submission.initialGrowth())
			request, _, err := NewContextBuilder(0).buildWithResearchState(t.Context(), projected, "", "u", defs, nil, skill.RunContext{}, "", system, "", ContextLimits{EffectiveTokens: limit, AutoCompactTokens: 360000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
			if err != nil {
				t.Fatal(err)
			}
			final, err := submission.request(request, nil, limit)
			if version == "review-source-v1" {
				if err == nil {
					t.Fatal("capture must still reproduce the unchanged legacy admission failure")
				}
				return
			}
			if err != nil || currentRequestBudgetTokens(final, nil) > limit {
				t.Fatal("wide review still cannot admit its complete final interface", err)
			}
			if !strings.Contains(conversationText(projected[0]), `"$read"`) || !strings.Contains(submissionRequestText(final), `"navigationEncoding":"prefix-groups-v1"`) || !final.RequireTool {
				t.Fatal("joint view lost paging, complete navigation or native completion")
			}
			if string(contract.Input) != originalInput || prompt != originalPrompt || conversationText(messages[0]) != originalPrompt || len(final.Tools) != 2 {
				t.Fatal("budget fitting changed frozen input, prompt or permissions")
			}
			t.Logf("input=%d navigation=%d segments=%d final estimate=%d original limit=%d projected prompt=%d", len(input), len(catalog.SubmissionMenu()), len(catalog.Segments), currentRequestBudgetTokens(final, nil), limit, len([]rune(conversationText(projected[0]))))
		})
	}
}
