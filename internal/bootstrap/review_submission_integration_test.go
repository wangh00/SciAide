package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSingleSourceReviewRejectionPersistsWithoutContentRepair(t *testing.T) {
	testReviewRejectionSnapshot(t, false, false)
}

func TestLargeUnicodeReviewReachesProviderAndPersists(t *testing.T) {
	testReviewRejectionSnapshot(t, true, false)
}

func TestLargeUnicodeReviewRetriesSameFailedStage(t *testing.T) {
	testReviewRejectionSnapshot(t, true, true)
}

func testReviewRejectionSnapshot(t *testing.T, large, retryLegacyFailure bool) {
	t.Helper()
	var reads atomic.Int32
	var readVerified atomic.Bool
	base := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if _, design := output["status"]; design && large {
			output["deliverables"] = []any{map[string]any{"title": "中文研究材料", "markdown": strings.Repeat("研", 77000)}}
		}
		if _, review := output["reviewedInputSha256"]; !review {
			return
		}
		if large {
			if !readVerified.Load() || reads.Load() < 2 {
				t.Error("long source never reached provider through actual pointer reads")
			}
			t.Logf("provider received review bytes=%d chars=%d", len(prompt), utf8.RuneCountInString(prompt))
			// Current model views may share/page long source text. The old
			// persistence byte guard is exercised below against the complete
			// execution snapshot, not by forcing duplicate inline model text.
			if len(prompt) <= 262144 || utf8.RuneCountInString(prompt) > 262144 || !strings.Contains(prompt, "<review_submission_sources>") {
				t.Error("complete source did not reach provider across the original byte bound")
			}
		}
		for _, required := range []string{"review-scope-v4", "Review BEFORE downstream delivery gates", "NOT an exhaustive inventory", `"downstreamDeliveryGateIds":["delivery_gate"]`} {
			if !strings.Contains(prompt, required) {
				t.Errorf("review request missing responsibility context: %s", required)
			}
		}
		output["approved"] = false
		output["methodIssues"] = []any{"交付缺少实际招募文案"}
		output["requiredCorrections"] = []any{"补充可直接使用的招募文案，保留伦理审批边界"}
		// The fixture turns this defect into ONE finding, omits wire category
		// arrays, and copies exact basis from current input. No false approval.
	})
	server := base
	if large {
		server = longUnicodeReviewReaderFixture(t, base, &reads, &readVerified)
		defer server.Close()
	}
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	if retryLegacyFailure {
		// Reproduce the released byte guard at the persistence boundary in
		// this isolated DB, then remove it before an ordinary user retry.
		if _, err := app.store.DB().Exec(`CREATE TRIGGER fixture_old_prompt_byte_limit BEFORE INSERT ON workflow_ai_executions
		WHEN length(cast(NEW.prompt_text AS blob)) > 262144
		BEGIN SELECT RAISE(ABORT, 'invalid Workflow AI execution snapshot'); END;`); err != nil {
			t.Fatal(err)
		}
	}
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "单源审查意见"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "交付研究设计与实际招募文案", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	plan := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: plan.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatal(err)
	}
	formal := waitForWorkflowState(t, app, project.ID, adopted.Run.Run.ID, workflow.RunFailed, 30*time.Second)
	expectedAttempt := 1
	if retryLegacyFailure {
		if formal.Run.ErrorCode != "WORKFLOW_AI_START_FAILED" || !strings.Contains(formal.Run.ErrorMessage, "invalid Workflow AI execution snapshot") {
			t.Fatal("old failure not reproduced", formal.Run.ErrorCode, formal.Run.ErrorMessage)
		}
		var failed workflow.Step
		for _, step := range formal.Steps {
			if step.NodeID == "independent_review" {
				failed = step
			}
		}
		if failed.ID == "" || failed.Status != workflow.StepFailed {
			t.Fatal("missing failed review")
		}
		for _, execution := range formal.AIExecutions {
			if execution.WorkflowStepID == failed.ID {
				t.Fatal("failed creation left partial execution")
			}
		}
		if _, err := app.store.DB().Exec(`DROP TRIGGER fixture_old_prompt_byte_limit`); err != nil {
			t.Fatal(err)
		}
		if _, err := app.workflow.Retry(context.Background(), workflow.RetryCommand{ProjectID: project.ID, RunID: formal.Run.ID, StepID: failed.ID}); err != nil {
			t.Fatal(err)
		}
		formal = waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunFailed, 30*time.Second)
		expectedAttempt = 2
	}
	if formal.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" {
		t.Fatalf("review became program failure: %s %s", formal.Run.ErrorCode, formal.Run.ErrorMessage)
	}
	guidance, bound, err := app.workflow.GuidanceForConversation(context.Background(), formal.Run.ConversationID)
	if err != nil || !bound {
		t.Fatal("terminal guidance missing", err)
	}
	for _, text := range []string{"reviewRecovery", "publicly discoverable evidence", "generic host-added", "user-input question-card", "Never pass URLs or invented IDs"} {
		if !strings.Contains(guidance.SystemContext, text) {
			t.Error("terminal recovery guidance missing", text)
		}
	}
	for _, name := range []string{"builtin.web.search", "builtin.web.open", "builtin.research.search", "builtin.research.task.read"} {
		found := false
		for _, allowed := range guidance.AllowedToolNames {
			found = found || allowed == name
		}
		if !found {
			t.Error("recovery lookup tool unavailable", name)
		}
	}
	stepID := ""
	for _, step := range formal.Steps {
		if step.NodeID == "independent_review" {
			stepID = step.ID
			if step.Status != workflow.StepCompleted {
				t.Fatalf("review not saved: %+v", step)
			}
			var output struct {
				Analysis map[string]json.RawMessage `json:"analysis"`
			}
			if json.Unmarshal(step.Output, &output) != nil {
				t.Fatal("bad saved output")
			}
			var issues []string
			_ = json.Unmarshal(output.Analysis["methodIssues"], &issues)
			if len(issues) != 1 || issues[0] != "交付缺少实际招募文案" {
				t.Fatal("derived issue lost", issues)
			}
		}
	}
	for _, execution := range formal.AIExecutions {
		if execution.WorkflowStepID == stepID {
			if large {
				var bytes, chars int
				var completePrompt string
				if err := app.store.DB().QueryRow(`SELECT length(cast(prompt_text AS blob)),length(prompt_text),prompt_text FROM workflow_ai_executions WHERE id=?`, execution.ID).Scan(&bytes, &chars, &completePrompt); err != nil {
					t.Fatal(err)
				}
				t.Logf("persisted actual prompt bytes=%d chars=%d", bytes, chars)
				if bytes <= 262144 || chars > 262144 || !strings.Contains(completePrompt, strings.Repeat("研", 77000)) {
					t.Fatal("complete source snapshot did not preserve long input across original byte guard")
				}
			}
			// Later independent guidance (for example delivery-form-v1) may
			// follow this exact marker without removing its frozen identity.
			if !strings.HasSuffix(execution.PromptVersion, "-review-scope-v4") && !strings.Contains(execution.PromptVersion, "-review-scope-v4-") {
				t.Fatal("effective review prompt not versioned", execution.PromptVersion)
			}
			if execution.Status != "completed" || execution.Attempt != expectedAttempt {
				t.Fatal("review unnecessarily retried", execution.Status)
			}
		}
	}
}

// Only pages received in actual provider requests are assembled. No DB read or
// fixture source text supplies missing page bytes, and only an offered pointer
// tool can read this frozen long field before the scientific rejection.
func longUnicodeReviewReaderFixture(t *testing.T, base *httptest.Server, reads *atomic.Int32, verified *atomic.Bool) *httptest.Server {
	pointer := ""
	const total = 77000
	pages := map[int]string{}
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture read failed", 400)
			return
		}
		prompt := workflowAITestPrompt(body)
		start := strings.LastIndex(prompt, "<review_submission_sources>")
		if start >= 0 {
			mu.Lock()
			defer mu.Unlock()
			raw := prompt[start+len("<review_submission_sources>"):]
			end := strings.Index(raw, "</review_submission_sources>")
			var menu struct {
				InputSHA256 string
				Sources     [][]json.RawMessage
			}
			if end < 0 || json.Unmarshal([]byte(raw[:end]), &menu) != nil {
				t.Error("invalid review source navigation")
				http.Error(w, "invalid source menu", 400)
				return
			}
			linked := false
			for _, row := range menu.Sources {
				if len(row) < 3 {
					continue
				}
				var path string
				var ends []int
				if json.Unmarshal(row[0], &path) == nil && strings.HasSuffix(path, "/deliverables/0/markdown") && (pointer == "" || pointer == path) && json.Unmarshal(row[2], &ends) == nil && len(ends) > 0 && ends[len(ends)-1] == total {
					// Follow the pointer actually issued for this route. A
					// direct design review uses /context, not /designContext.
					pointer, linked = path, true
				}
			}
			if !linked || len(menu.InputSHA256) != 64 {
				t.Error("long field missing from current source navigation")
				http.Error(w, "missing long source", 400)
				return
			}
			var request struct {
				Messages []struct {
					Role    string
					Content json.RawMessage
				}
				Tools []struct {
					Function struct {
						Name       string
						Parameters struct{ Properties map[string]json.RawMessage }
					}
				}
			}
			if json.Unmarshal(body, &request) != nil {
				t.Error("invalid provider request")
				http.Error(w, "invalid request", 400)
				return
			}
			toolName := ""
			for _, d := range request.Tools {
				if d.Function.Parameters.Properties["pointer"] != nil && d.Function.Parameters.Properties["section"] != nil {
					toolName = d.Function.Name
				}
			}
			expectedSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Repeat("研", total))))
			for _, m := range request.Messages {
				if m.Role != "tool" {
					continue
				}
				var text string
				if json.Unmarshal(m.Content, &text) != nil {
					continue
				}
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				var result struct {
					Structured struct {
						Section, Pointer, Encoding, SHA256, ValueSHA256, Content string
						Offset, NextOffset, TotalCharacters                      int
						HasMore                                                  bool
					}
				}
				if json.Unmarshal([]byte(text), &result) != nil {
					continue
				}
				page := result.Structured
				if page.Section != "stage_input" || page.Pointer != pointer || page.SHA256 != menu.InputSHA256 {
					continue
				}
				if page.Encoding != "text" || page.ValueSHA256 != expectedSHA || page.TotalCharacters != total || page.Offset < 0 || page.NextOffset > total || page.NextOffset != page.Offset+utf8.RuneCountInString(page.Content) || page.HasMore != (page.NextOffset < total) {
					t.Error("invalid long-source page identity/range")
					http.Error(w, "invalid source page", 400)
					return
				}
				if old, ok := pages[page.Offset]; ok && old != page.Content {
					t.Error("long source page changed")
					http.Error(w, "changed source", 400)
					return
				}
				pages[page.Offset] = page.Content
			}
			var full strings.Builder
			offset := 0
			for offset < total {
				text, ok := pages[offset]
				if !ok {
					break
				}
				if text == "" {
					t.Error("empty source page")
					http.Error(w, "empty source", 400)
					return
				}
				full.WriteString(text)
				offset += utf8.RuneCountInString(text)
			}
			if offset < total {
				if toolName == "" {
					t.Error("long source has no actually offered pointer reader")
					http.Error(w, "no source reader", 400)
					return
				}
				args, _ := json.Marshal(map[string]any{"section": "stage_input", "pointer": pointer, "offset": offset})
				n := reads.Add(1)
				emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, int(n), "", toolName, string(args))
				return
			}
			if full.String() != strings.Repeat("研", total) || fmt.Sprintf("%x", sha256.Sum256([]byte(full.String()))) != expectedSHA {
				t.Error("provider never reconstructed the complete long source")
				http.Error(w, "source mismatch", 400)
				return
			}
			verified.Store(true)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		base.Config.Handler.ServeHTTP(w, r)
	}))
}
