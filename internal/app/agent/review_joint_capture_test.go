package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Real frozen materials with explicitly synthetic fixed host cost. No model,
// Application or database is opened, and the saved execution is never resumed.
func TestCapturedSharedReviewAdmitsCompleteJointInterface(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_JOINT_REPLAY")
	if dir == "" {
		t.Skip("no read-only joint review capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "captured-review-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		ID             string          `json:"id"`
		WorkflowRunID  string          `json:"workflow_run_id"`
		WorkflowStepID string          `json:"workflow_step_id"`
		PromptVersion  string          `json:"prompt_version"`
		Input          json.RawMessage `json:"input"`
		InputSHA256    string          `json:"input_sha256"`
		PromptText     string          `json:"prompt_text"`
		PromptSHA256   string          `json:"prompt_sha256"`
		Schema         string          `json:"output_schema_json"`
		SchemaSHA256   string          `json:"output_schema_sha256"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.InputSHA256 != fmt.Sprintf("%x", sha256.Sum256(capture.Input)) || capture.PromptSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(capture.PromptText))) || capture.SchemaSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(capture.Schema))) {
		t.Fatal("capture identity mismatch")
	}
	contract := stageBudgetContract(capture.Input, capture.PromptText)
	contract.ID, contract.NodeID, contract.WorkflowRunID, contract.WorkflowStepID, contract.Attempt = capture.ID, "independent_review", capture.WorkflowRunID, capture.WorkflowStepID, 1
	contract.PromptVersion = capture.PromptVersion
	contract.OutputSchema = json.RawMessage(capture.Schema)
	contract.OutputSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256(contract.OutputSchema))
	binding := workflow.ReviewSourceBinding{StageMaterialRequest: workflow.StageMaterialRequest{ProjectID: "offline-project", ConversationID: "offline-conversation", WorkflowRunID: contract.WorkflowRunID, WorkflowStepID: contract.WorkflowStepID, ChatRunID: "offline-chat", Attempt: 1, InputSHA256: contract.InputSHA256}, ExecutionID: contract.ID, PromptVersion: contract.PromptVersion, OutputSchemaSHA256: contract.OutputSchemaSHA256}
	catalog, err := workflow.BuildReviewSourceCatalog(t.Context(), capture.Input, binding)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "citation-decision.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decision struct {
		Context []tool.CitationRef `json:"context"`
	}
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	catalog.CitationAliases = map[string]string{}
	for _, ref := range decision.Context {
		catalog.CitationAliases[citation.KnowledgeReference(binding.ChatRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)] = ref.Reference
	}
	menu := string(catalog.SubmissionMenu())
	submission := &reviewSourceSubmission{catalog: catalog, fullSchema: workflow.ReviewSourceSubmissionSchema(workflow.ReviewSubmissionSchema(contract.OutputSchema))}
	for _, fixed := range []int{48107, 80000, 400000} {
		t.Run(fmt.Sprint(fixed), func(t *testing.T) {
			const limit = 380000
			messages := []conversation.Message{checkpointText("u", "run", capture.PromptText)}
			run := chat.Run{ID: "run", UserMessageID: "u"}
			defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "Diagnostic frozen reader", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			system := strings.Repeat("s", fixed)
			projected := budgetedStageMessages(messages, run, contract, defs, nil, system, "", "", limit, 360000, submission.initialGrowth())
			request, _, err := NewContextBuilder(0).buildWithResearchState(t.Context(), projected, "", "u", defs, nil, skill.RunContext{}, "", system, "", ContextLimits{EffectiveTokens: limit, AutoCompactTokens: 360000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
			if fixed >= limit && err != nil {
				t.Log("oversized fixed contract still stops before model IO")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			final, err := submission.request(request, nil, limit)
			if fixed >= limit {
				if err == nil {
					t.Fatal("oversized fixed contract bypassed final budget")
				}
				return
			}
			if err != nil {
				t.Fatal("original materials still cannot admit complete joint interface", err)
			}
			if currentRequestBudgetTokens(final, nil) > limit || !strings.Contains(submissionRequestText(final), menu) || !final.RequireTool || len(final.Tools) != 2 {
				t.Fatal("budget fit lost complete menu or native submission")
			}
			if string(catalog.SubmissionMenu()) != menu || len(catalog.Segments) != 3629 || string(contract.Input) != string(capture.Input) || conversationText(messages[0]) != capture.PromptText {
				t.Fatal("frozen input/catalog/prompt changed")
			}
			initialTokens := currentRequestBudgetTokens(final, nil)
			if fixed == 48107 {
				submission.materials = newStageMaterialLedger(contract, "run")
				pointer := ""
				for _, segment := range catalog.Segments {
					if strings.HasPrefix(segment.Pointer, "/researchSourceContext/bibliography/0/") {
						material, e := submission.materials.material(segment.Pointer)
						if e == nil && material.Encoding == "text" && utf8.RuneCountInString(material.Content) > 0 && utf8.RuneCountInString(material.Content) < 400 {
							pointer = segment.Pointer
							break
						}
					}
				}
				if pointer == "" {
					t.Fatal("no paged source scalar to verify")
				}
				selected := map[string]bool{pointer: true}
				if len(submission.visibleSources(final, nil, selected)[pointer]) != 0 {
					t.Fatal("navigation/hidden source counted as visible evidence")
				}
				material, _ := submission.materials.material(pointer)
				message, call := sourceVisiblePage(t, submission, 0, utf8.RuneCountInString(material.Content), 1, pointer)
				final.Messages = append(cloneModelMessages(final.Messages), message)
				visible := submission.visibleSources(final, []tool.Call{call}, selected)[pointer]
				if len(visible) != 1 || visible[0] != [2]int{0, utf8.RuneCountInString(material.Content)} {
					t.Fatal("actual exact source page did not establish its own visible range", visible)
				}
				t.Log("hidden source remains unread until an exact current source page is present")
			}
			t.Logf("synthetic fixed=%d; navigation bytes=%d segments=%d; initial final estimate=%d/%d after visible page=%d; material view=%d", fixed, len(menu), len(catalog.Segments), initialTokens, limit, currentRequestBudgetTokens(final, nil), len([]rune(conversationText(projected[0]))))
		})
	}
}
