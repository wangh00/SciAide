package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type workingStateFixture struct {
	t     *testing.T
	store *Store
	path  string
	repo  *WorkflowRuntimeRepository
	now   time.Time
}

func newWorkingStateFixture(t *testing.T) *workingStateFixture {
	return newWorkingStateFixtureAtVersion(t, 0)
}

func newWorkingStateFixtureAtVersion(t *testing.T, version int) *workingStateFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "working-state.db")
	var store *Store
	var err error
	if version == 0 {
		store, err = Open(context.Background(), path)
	} else {
		db, openErr := openDatabase(context.Background(), path, false)
		if openErr != nil {
			t.Fatal(openErr)
		}
		store = &Store{db: db}
		err = migrateToVersion(context.Background(), db, version)
	}
	if err != nil {
		t.Fatal(err)
	}
	f := &workingStateFixture{t: t, store: store, path: path, repo: NewWorkflowRuntimeRepository(store.DB()), now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { _ = f.store.Close() })
	at := formatTime(f.now)
	hash := strings.Repeat("a", 64)
	f.exec(`INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('p','P','','C:/fixture','external',?,?),('other','Other','','C:/other','external',?,?)`, at, at, at, at)
	f.exec(`INSERT INTO workflows(id,project_id,name,purpose,version,created_at,updated_at) VALUES ('wf','p','Research','research_starter',1,?,?)`, at, at)
	f.exec(`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES ('wv','wf',1,'{}',?,'{}',?,?)`, hash, hash, at)
	f.exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,created_at,updated_at,research_task_id,workflow_name,workflow_purpose) VALUES ('task','p','wf','wv','completed','{"discussion_only":true}',?,'{}',?,'{}',?,?,'task','Research','research_starter')`, hash, hash, at, at)
	f.exec(`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('conv','p','Discussion',?,?)`, at, at)
	f.exec(`INSERT INTO workflow_conversations(workflow_run_id,conversation_id,created_at) VALUES ('task','conv',?)`, at)
	f.exec(`INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','fixture-secret',60,'{}',1,1,?,?)`, at, at)
	f.chat("chat", "conv", "human")
	return f
}

func (f *workingStateFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.store.DB().Exec(query, args...); err != nil {
		f.t.Fatalf("fixture SQL %s: %v", query, err)
	}
}

func (f *workingStateFixture) chat(chat, conv, human string) {
	f.t.Helper()
	at := formatTime(f.now)
	f.exec(`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES (?,?,?,'user','complete',?,?)`, human, conv, chat, at, at)
	f.exec(`INSERT INTO runs(id,conversation_id,user_message_id,model_profile_id,status,created_at,updated_at,model_id) VALUES (?,?,?,'profile','running',?,?,'model')`, chat, conv, human, at, at)
}

func workingCommand(version int, focus string) workflow.ResearchWorkingStateUpdateCommand {
	return workflow.ResearchWorkingStateUpdateCommand{ExpectedVersion: version, CurrentFocus: focus, Rationale: "why", NextStep: "inspect", Unknowns: []string{"unverified"}}
}

func (f *workingStateFixture) call(callID, chatID string, command workflow.ResearchWorkingStateUpdateCommand) {
	f.t.Helper()
	raw, err := json.Marshal(command)
	if err != nil {
		f.t.Fatal(err)
	}
	at := formatTime(f.now)
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES (?,?,?,?,'1',?,'running','moderate','[]',0,?,?)`, callID, chatID, "provider-"+callID, workflow.ResearchStateUpdateTool, string(raw), at, at)
}

func (f *workingStateFixture) save(callID, chatID string, command workflow.ResearchWorkingStateUpdateCommand) (workflow.ResearchWorkingState, error) {
	return f.repo.SaveResearchWorkingState(context.Background(), "p", "task", chatID, callID, "state-"+callID, f.now, command)
}

func (f *workingStateFixture) finish(callID string, success bool) error {
	status, resultStatus := tool.CallFailed, tool.ResultError
	if success {
		status, resultStatus = tool.CallCompleted, tool.ResultSuccess
	}
	return NewToolRepository(f.store.DB()).Finish(context.Background(), callID, tool.CallRunning, status,
		tool.Result{Status: resultStatus, Text: "fixture", Structured: json.RawMessage(`{}`), CreatedAt: f.now}, "", "", f.now)
}

func (f *workingStateFixture) read() *workflow.ResearchWorkingState {
	f.t.Helper()
	value, err := f.repo.GetResearchWorkingState(context.Background(), "p", "task")
	if err != nil {
		f.t.Fatal(err)
	}
	return value
}

func TestResearchWorkingStateLifecycle(t *testing.T) {
	f := newWorkingStateFixture(t)
	first := workingCommand(0, "initial focus")
	f.call("c1", "chat", first)
	value, err := f.save("c1", "chat", first)
	if err != nil || value.Version != 1 || value.Status != "working_note" {
		t.Fatalf("pending state: %+v %v", value, err)
	}
	if got := f.read(); got != nil {
		t.Fatalf("pending state visible: %+v", got)
	}
	replayed, err := f.save("c1", "chat", first)
	if err != nil || replayed.ID != value.ID {
		t.Fatalf("same call not idempotent: %+v %v", replayed, err)
	}
	if _, err := f.save("c1", "chat", workingCommand(0, "different")); err == nil {
		t.Fatal("same call accepted conflicting contents")
	}
	if err := f.finish("c1", true); err != nil {
		t.Fatal(err)
	}
	if got := f.read(); got == nil || got.Version != 1 || got.CurrentFocus != "initial focus" || got.SourceCallID != "c1" || got.Status != "working_note" {
		t.Fatalf("completed projection: %+v", got)
	}
	second := workingCommand(1, "must not become effective")
	f.call("c2", "chat", second)
	if _, err := f.save("c2", "chat", second); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("c2", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(); got == nil || got.Version != 1 || got.SourceCallID != "c1" {
		t.Fatalf("failed call replaced current state: %+v", got)
	}
	third := workingCommand(1, "retry after failed result")
	f.call("c3", "chat", third)
	if _, err := f.save("c3", "chat", third); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("c3", true); err != nil {
		t.Fatal(err)
	}
	if got := f.read(); got == nil || got.Version != 2 || got.CurrentFocus != third.CurrentFocus {
		t.Fatalf("retry projection: %+v", got)
	}
	if _, err := f.repo.GetResearchWorkingState(context.Background(), "other", "task"); err == nil {
		t.Fatal("cross-project read accepted")
	}
	if _, err := f.repo.GetResearchWorkingState(context.Background(), "p", "missing"); err == nil {
		t.Fatal("missing task read accepted")
	}
}

func TestResearchWorkingStateSparsePatchAndReadScope(t *testing.T) {
	f := newWorkingStateFixture(t)
	initial := workingCommand(0, "initial focus")
	f.call("initial", "chat", initial)
	if _, err := f.save("initial", "chat", initial); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("initial", true); err != nil {
		t.Fatal(err)
	}
	patch, err := workflow.ParseResearchWorkingStateUpdate(json.RawMessage(`{"expectedVersion":1,"nextStep":"new next step only"}`))
	if err != nil {
		t.Fatal(err)
	}
	f.call("patch", "chat", patch)
	state, err := f.save("patch", "chat", patch)
	if err != nil || state.CurrentFocus != initial.CurrentFocus || state.Rationale != initial.Rationale || !reflect.DeepEqual(state.Unknowns, initial.Unknowns) || state.NextStep != "new next step only" {
		t.Fatal("patch lost preserved fields", state, err)
	}
	if err := f.finish("patch", true); err != nil {
		t.Fatal(err)
	}
	at := formatTime(f.now)
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES ('read','chat','read-provider',?,'1','{"expectedVersion":2,"field":"unknowns"}','running','low','[]',1,?,?)`, workflow.ResearchStateReadTool, at, at)
	if err := f.repo.ValidateResearchWorkingStateRead(context.Background(), "p", "task", "chat", "read"); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][4]string{{"other", "task", "chat", "read"}, {"p", "other-task", "chat", "read"}, {"p", "task", "chat", "initial"}, {"p", "task", "other-chat", "read"}} {
		if err := f.repo.ValidateResearchWorkingStateRead(context.Background(), ids[0], ids[1], ids[2], ids[3]); err == nil {
			t.Fatal("foreign or non-reader context accepted", ids)
		}
	}
}

func TestResearchWorkingStateConcurrentCAS(t *testing.T) {
	f := newWorkingStateFixture(t)
	a, b := workingCommand(0, "A"), workingCommand(0, "B")
	f.call("a", "chat", a)
	f.call("b", "chat", b)
	type outcome struct {
		call string
		err  error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, item := range []struct {
		call    string
		command workflow.ResearchWorkingStateUpdateCommand
	}{{"a", a}, {"b", b}} {
		wg.Add(1)
		go func(call string, command workflow.ResearchWorkingStateUpdateCommand) {
			defer wg.Done()
			<-start
			_, err := f.repo.SaveResearchWorkingState(context.Background(), "p", "task", "chat", call, "state-"+call, f.now, command)
			results <- outcome{call, err}
		}(item.call, item.command)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner, loser string
	for result := range results {
		if result.err == nil {
			winner = result.call
		} else {
			loser = result.call
		}
	}
	if winner == "" || loser == "" {
		t.Fatalf("expected exactly one pending write, winner=%q loser=%q", winner, loser)
	}
	if got := f.read(); got != nil {
		t.Fatalf("pending exposed: %+v", got)
	}
	if err := f.finish(winner, true); err != nil {
		t.Fatal(err)
	}
	loserCommand := a
	if loser == "b" {
		loserCommand = b
	}
	if _, err := f.save(loser, "chat", loserCommand); err == nil {
		t.Fatal("stale version accepted after winner")
	}
}

func TestResearchWorkingStateRequiresCurrentBoundDiscussion(t *testing.T) {
	tests := []struct {
		name   string
		change func(*workingStateFixture)
	}{
		{"latest user", func(f *workingStateFixture) {
			f.exec(`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('new-human','conv','other-run','user','complete',?,?)`, formatTime(f.now), formatTime(f.now))
		}},
		{"terminal chat", func(f *workingStateFixture) { f.exec(`UPDATE runs SET status='cancelled' WHERE id='chat'`) }},
		{"terminal call", func(f *workingStateFixture) { f.exec(`UPDATE tool_calls SET status='cancelled' WHERE id='call'`) }},
		{"wrong tool", func(f *workingStateFixture) {
			f.exec(`UPDATE tool_calls SET tool_name='builtin.other' WHERE id='call'`)
		}},
		{"archived", func(f *workingStateFixture) { f.exec(`UPDATE research_tasks SET status='archived' WHERE id='task'`) }},
		{"formal plan", func(f *workingStateFixture) {
			f.exec(`UPDATE workflow_runs SET workflow_purpose='user_plan' WHERE id='task'`)
		}},
		{"route adopted", func(f *workingStateFixture) {
			f.exec(`INSERT INTO workflow_events(id,workflow_run_id,sequence,event_type,payload_json,created_at) VALUES ('adopted','task',1,'research.route_adopted','{}',?)`, formatTime(f.now))
		}},
		{"new formal workflow", func(f *workingStateFixture) {
			h := strings.Repeat("a", 64)
			at := formatTime(f.now.Add(time.Second))
			f.exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,created_at,updated_at,research_task_id,workflow_name,workflow_purpose) VALUES ('newer','p','wf','wv','completed','{}',?,'{}',?,'{}',?,?,'task','Research','user_plan')`, h, h, at, at)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			cmd := workingCommand(0, "focus")
			f.call("call", "chat", cmd)
			test.change(f)
			if _, err := f.save("call", "chat", cmd); err == nil {
				t.Fatal("invalid scope accepted")
			}
		})
	}
	t.Run("ordinary chat", func(t *testing.T) {
		f := newWorkingStateFixture(t)
		at := formatTime(f.now)
		f.exec(`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('ordinary','p','Normal',?,?)`, at, at)
		f.chat("plain-chat", "ordinary", "plain-human")
		cmd := workingCommand(0, "focus")
		f.call("call", "plain-chat", cmd)
		if _, err := f.save("call", "plain-chat", cmd); err == nil {
			t.Fatal("ordinary chat accepted")
		}
	})
	t.Run("wrong project or task", func(t *testing.T) {
		f := newWorkingStateFixture(t)
		cmd := workingCommand(0, "focus")
		f.call("call", "chat", cmd)
		for _, ids := range [][2]string{{"other", "task"}, {"p", "not-task"}} {
			if _, err := f.repo.SaveResearchWorkingState(context.Background(), ids[0], ids[1], "chat", "call", "state", f.now, cmd); err == nil {
				t.Fatal("wrong task accepted", ids)
			}
		}
	})
	t.Run("workflow AI chat", func(t *testing.T) {
		f := newWorkingStateFixture(t)
		cmd := workingCommand(0, "focus")
		f.call("call", "chat", cmd)
		f.attachWorkflowAI()
		if _, err := f.save("call", "chat", cmd); err == nil {
			t.Fatal("workflow AI accepted")
		}
	})
	t.Run("candidate discussion accepted", func(t *testing.T) {
		f := newWorkingStateFixture(t)
		f.exec(`UPDATE workflow_runs SET inputs_json='{"starter_context":{"researchIdea":"candidate"}}' WHERE id='task'`)
		cmd := workingCommand(0, "focus")
		f.call("call", "chat", cmd)
		if _, err := f.save("call", "chat", cmd); err != nil {
			t.Fatal(err)
		}
	})
}

func (f *workingStateFixture) attachWorkflowAI() {
	f.t.Helper()
	at := formatTime(f.now)
	h := strings.Repeat("a", 64)
	f.exec(`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,idempotency_key,updated_at) VALUES ('step','task','ai',0,'agent_stage','running',1,'{}',?,'{}','',?)`, h, at)
	f.exec(`INSERT INTO workflow_ai_executions(id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,status,created_at,updated_at) VALUES ('execution','task','step',1,'agent_stage','profile','model','none','v1','prompt',?,?,'[]','{}',?,'running',?,?)`, h, h, h, at, at)
	f.exec(`INSERT INTO workflow_ai_chat_runs(execution_id,chat_run_id,created_at) VALUES ('execution','chat',?)`, at)
}

func TestResearchWorkingStateLateCompletionAndDeletion(t *testing.T) {
	f := newWorkingStateFixture(t)
	cmd := workingCommand(0, "pending")
	f.call("stale", "chat", cmd)
	if _, err := f.save("stale", "chat", cmd); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE runs SET status='cancelled' WHERE id='chat'`)
	if err := f.finish("stale", true); err == nil {
		t.Fatal("late cancelled chat made a stale state effective")
	}
	if got := f.read(); got != nil {
		t.Fatalf("stale late state visible: %+v", got)
	}
	f.exec(`UPDATE tool_calls SET status='cancelled' WHERE id='stale'`)
	f.chat("new-chat", "conv", "new-human")
	retry := workingCommand(0, "valid")
	f.call("valid", "new-chat", retry)
	if _, err := f.save("valid", "new-chat", retry); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("valid", true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = NewWorkflowRuntimeRepository(f.store.DB())
	if got := f.read(); got == nil || got.SourceCallID != "valid" {
		t.Fatalf("state lost after reopen: %+v", got)
	}
	f.exec(`DELETE FROM tool_calls WHERE id='valid'`)
	if got := f.read(); got != nil {
		t.Fatalf("state survived source deletion: %+v", got)
	}
	var count int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM research_working_states WHERE source_call_id='valid'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("source cascade count=%d err=%v", count, err)
	}
	if _, err := f.repo.GetResearchWorkingState(context.Background(), "p", "missing"); err == nil {
		t.Fatalf("expected scoped missing-task error, got %v", err)
	}
}

func TestResearchWorkingStateCancelledPendingDoesNotBlock(t *testing.T) {
	f := newWorkingStateFixture(t)
	first := workingCommand(0, "cancelled")
	f.call("first", "chat", first)
	if _, err := f.save("first", "chat", first); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE tool_calls SET status='cancelled' WHERE id='first'`)
	second := workingCommand(0, "current")
	f.call("second", "chat", second)
	if _, err := f.save("second", "chat", second); err != nil {
		t.Fatal("cancelled provisional call blocked a new write:", err)
	}
	if err := f.finish("second", true); err != nil {
		t.Fatal(err)
	}
	if got := f.read(); got == nil || got.CurrentFocus != "current" || got.Version != 1 {
		t.Fatalf("wrong effective state: %+v", got)
	}
}

func (f *workingStateFixture) proposeQuestion() {
	f.t.Helper()
	ctx := context.Background()
	question := tool.Call{ID: "question", RunID: "chat", ToolName: userinput.ToolName, ToolVersion: "1", ProviderCallID: "question", Status: tool.CallPending, Risk: tool.RiskLow, Idempotent: true,
		Arguments: json.RawMessage(`{"questions":[{"id":"q","question":"比较什么？","required":true}]}`), CreatedAt: f.now, UpdatedAt: f.now}
	if err := NewToolRepository(f.store.DB()).Create(ctx, question); err != nil {
		f.t.Fatal(err)
	}
}

func (f *workingStateFixture) answerProposedQuestion() string {
	f.t.Helper()
	ctx := context.Background()
	runs := NewRunRepository(f.store.DB())
	if _, err := runs.WaitForUserInput(ctx, "chat", "question", f.now); err != nil {
		f.t.Fatal(err)
	}
	if _, err := runs.RespondToUserInput(ctx, userinput.Response{ConversationID: "conv", RunID: "chat", CallID: "question", Answers: userinput.Answers{"q": {Text: "比较长期结局"}}}, f.now); err != nil {
		f.t.Fatal(err)
	}
	var answerID string
	if err := f.store.DB().QueryRow(`SELECT id FROM messages WHERE conversation_id='conv' AND role='user' ORDER BY rowid DESC LIMIT 1`).Scan(&answerID); err != nil || answerID == "human" {
		f.t.Fatalf("real question answer was not appended: %q %v", answerID, err)
	}
	return answerID
}

func (f *workingStateFixture) answerQuestion() string {
	f.t.Helper()
	f.proposeQuestion()
	return f.answerProposedQuestion()
}

func TestResearchWorkingStateProposalBeforeAnswerWithStaticClock(t *testing.T) {
	for _, questionFirst := range []bool{false, true} {
		name := "state-first"
		if questionFirst {
			name = "question-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			early := workingCommand(0, "before answer")
			if questionFirst {
				f.proposeQuestion()
			}
			f.call("early", "chat", early)
			f.exec(`UPDATE tool_calls SET status='pending' WHERE id='early'`)
			if !questionFirst {
				f.proposeQuestion()
			}
			// Every row (both proposals, question answer and invocation) has the
			// same timestamp. Only transactionally captured message IDs matter.
			var proposed string
			if err := f.store.DB().QueryRow(`SELECT source_user_message_id FROM research_working_state_proposals WHERE source_call_id='early'`).Scan(&proposed); err != nil || proposed != "human" {
				t.Fatalf("wrong original proposal binding: %q, %v", proposed, err)
			}
			answerID := f.answerProposedQuestion()
			f.exec(`UPDATE tool_calls SET status='running' WHERE id='early'`)
			if _, err := f.save("early", "chat", early); err == nil || !strings.Contains(err.Error(), "重新") {
				t.Fatalf("old proposal did not require a fresh call after the answer: %v", err)
			}
			if got := f.read(); got != nil {
				t.Fatalf("old proposal created a note: %+v", got)
			}
			fresh := workingCommand(0, "after answer")
			f.call("fresh", "chat", fresh)
			state, err := f.save("fresh", "chat", fresh)
			if err != nil || state.SourceUserMessageID != answerID {
				t.Fatalf("fresh proposal did not bind the actual answer: %+v %v", state, err)
			}
			if err := f.finish("fresh", true); err != nil {
				t.Fatal(err)
			}
			if got := f.read(); got == nil || got.SourceCallID != "fresh" || got.SourceUserMessageID != answerID {
				t.Fatalf("fresh call did not become effective: %+v", got)
			}
		})
	}
}

func TestResearchWorkingStateMissingProposalFailsClosed(t *testing.T) {
	f := newWorkingStateFixture(t)
	cmd := workingCommand(0, "focus")
	f.call("call", "chat", cmd)
	if _, err := f.store.DB().Exec(`UPDATE research_working_state_proposals SET source_user_message_id='human' WHERE source_call_id='call'`); err == nil {
		t.Fatal("proposal identity was mutable")
	}
	f.exec(`DELETE FROM research_working_state_proposals WHERE source_call_id='call'`)
	if _, err := f.save("call", "chat", cmd); err == nil {
		t.Fatal("missing proposal identity was guessed from the current message")
	}
	if got := f.read(); got != nil {
		t.Fatalf("unbound proposal became visible: %+v", got)
	}
}

func TestResearchWorkingStateAfterUserInputAnswer(t *testing.T) {
	f := newWorkingStateFixture(t)
	answerID := f.answerQuestion()
	cmd := workingCommand(0, "比较长期结局")
	f.call("after-answer", "chat", cmd)
	value, err := f.save("after-answer", "chat", cmd)
	if err != nil {
		t.Fatal("same-run question answer must permit a working note:", err)
	}
	if value.SourceUserMessageID != answerID {
		t.Fatalf("note bound to old message, got %q want %q", value.SourceUserMessageID, answerID)
	}
	if err := f.finish("after-answer", true); err != nil {
		t.Fatal("question answer note did not finish:", err)
	}
	if got := f.read(); got == nil || got.SourceUserMessageID != answerID {
		t.Fatalf("wrong effective question-answer source: %+v", got)
	}
}

func TestResearchWorkingStateQuestionAnswerDoesNotAuthorizeOldRun(t *testing.T) {
	f := newWorkingStateFixture(t)
	f.answerQuestion()
	old := workingCommand(0, "旧轮意见")
	f.call("old-call", "chat", old)
	f.exec(`UPDATE runs SET status='completed' WHERE id='chat'`)
	f.chat("new-chat", "conv", "new-human")
	if _, err := f.save("old-call", "chat", old); err == nil {
		t.Fatal("prior ChatRun retained write authority after a new user turn")
	}
	current := workingCommand(0, "新轮意见")
	f.call("new-call", "new-chat", current)
	value, err := f.save("new-call", "new-chat", current)
	if err != nil || value.SourceUserMessageID != "new-human" {
		t.Fatalf("current turn not authorized or wrong message binding: %+v %v", value, err)
	}
	if err := f.finish("new-call", true); err != nil {
		t.Fatal(err)
	}
}

func TestResearchWorkingStateLateFinishRechecksScope(t *testing.T) {
	tests := []struct {
		name   string
		change func(*workingStateFixture)
	}{
		{"later human", func(f *workingStateFixture) {
			f.exec(`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('later','conv','later-chat','user','complete',?,?)`, formatTime(f.now), formatTime(f.now))
		}},
		{"archived", func(f *workingStateFixture) {
			f.exec(`UPDATE research_tasks SET status='archived' WHERE id='task'`)
		}},
		{"new formal workflow", func(f *workingStateFixture) {
			h := strings.Repeat("a", 64)
			at := formatTime(f.now.Add(time.Second))
			f.exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,created_at,updated_at,research_task_id,workflow_name,workflow_purpose) VALUES ('later-wr','p','wf','wv','completed','{}',?,'{}',?,'{}',?,?,'task','Research','user_plan')`, h, h, at, at)
		}},
		{"adopted", func(f *workingStateFixture) {
			f.exec(`INSERT INTO workflow_events(id,workflow_run_id,sequence,event_type,payload_json,created_at) VALUES ('adoption','task',1,'research.route_adopted','{}',?)`, formatTime(f.now))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			cmd := workingCommand(0, "provisional")
			f.call("call", "chat", cmd)
			if _, err := f.save("call", "chat", cmd); err != nil {
				t.Fatal(err)
			}
			test.change(f)
			if err := f.finish("call", true); err == nil {
				t.Fatal("stale provisional note completed")
			}
			var count int
			if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_results WHERE tool_call_id='call'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("finish did not roll back atomically, results=%d error=%v", count, err)
			}
		})
	}
}

func TestResearchWorkingStateSourceChatDeletionAndWorkflowArchival(t *testing.T) {
	for _, source := range []string{"chat", "workflow"} {
		t.Run(source, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			cmd := workingCommand(0, "committed")
			f.call("call", "chat", cmd)
			if _, err := f.save("call", "chat", cmd); err != nil {
				t.Fatal(err)
			}
			if err := f.finish("call", true); err != nil {
				t.Fatal(err)
			}
			if source == "chat" {
				f.exec(`DELETE FROM runs WHERE id='chat'`)
			} else {
				f.exec(`DELETE FROM workflow_runs WHERE id='task'`)
			}
			var count int
			wantCount := 0
			if source == "workflow" {
				wantCount = 1 // Planner is provenance; the still-existing Chat owns the record.
			}
			if err := f.store.DB().QueryRow(`SELECT count(*) FROM research_working_states`).Scan(&count); err != nil || count != wantCount {
				t.Fatalf("source cascade count=%d error=%v", count, err)
			}
			got, err := f.repo.GetResearchWorkingState(context.Background(), "p", "task")
			if source == "workflow" {
				if err == nil || got != nil {
					t.Fatalf("last Workflow deletion should leave an archived task: %+v, %v", got, err)
				}
			} else if err != nil || got != nil {
				t.Fatalf("deleted source still visible: %+v, %v", got, err)
			}
		})
	}
}

func TestResearchWorkingStateProjectArchiveRoundTrip(t *testing.T) {
	f := newWorkingStateFixture(t)
	ctx := context.Background()
	answerID := f.answerQuestion()
	first := workingCommand(0, "verified call, unverified note")
	f.call("success", "chat", first)
	original, err := f.save("success", "chat", first)
	if err != nil {
		t.Fatal(err)
	}
	if original.SourceUserMessageID != answerID {
		t.Fatalf("working note should bind the question answer, got %+v", original)
	}
	receipt, err := json.Marshal(map[string]any{"id": original.ID, "version": 1, "writeStatus": "pending_tool_completion"})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewToolRepository(f.store.DB()).Finish(ctx, "success", tool.CallRunning, tool.CallCompleted,
		tool.Result{Status: tool.ResultSuccess, Text: "unverified", Structured: receipt, CreatedAt: f.now}, "", "", f.now); err != nil {
		t.Fatal(err)
	}
	failed := workingCommand(1, "failed output must stay invisible")
	f.call("failed", "chat", failed)
	if _, err := f.save("failed", "chat", failed); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("failed", false); err != nil {
		t.Fatal(err)
	}
	pending := workingCommand(1, "pending output must stay invisible")
	f.call("pending", "chat", pending)
	if _, err := f.save("pending", "chat", pending); err != nil {
		t.Fatal(err)
	}
	// This later message belongs to the same run but was not visible when any
	// of the state calls were proposed. Its timestamp is deliberately equal.
	f.exec(`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('later-human','conv','chat','user','complete',?,?)`, formatTime(f.now), formatTime(f.now))
	// Archive export admits terminal chats only; the orphaned running call
	// deliberately models a crash window between write and result persistence.
	f.exec(`UPDATE runs SET status='interrupted' WHERE id='chat'`)
	archivePath := filepath.Join(t.TempDir(), "snapshot.db")
	archive := NewProjectArchiveRepository(f.store.DB())
	if _, err := archive.CreateSnapshot(ctx, "p", archivePath); err != nil {
		t.Fatal("snapshot:", err)
	}
	restored := project.Project{ID: "restored-project", Name: "Restored", WorkspacePath: t.TempDir(), CreatedAt: f.now, UpdatedAt: f.now}
	plan, err := archive.RewriteSnapshot(ctx, archivePath, restored)
	if err != nil {
		t.Fatal("rewrite:", err)
	}
	newTaskID := plan.TaskIDs["task"]
	if newTaskID == "" || newTaskID == "task" {
		t.Fatalf("task identity not remapped: %#v", plan.TaskIDs)
	}
	// Force the archive-merge trigger to encounter an active source run. This
	// simulates a conflicting automatic capture in the destination and verifies
	// it is replaced by the authentic archived proposal, not the later answer.
	staged, err := OpenExisting(ctx, archivePath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staged.Exec(`UPDATE runs SET status='running' WHERE id IN (SELECT source_chat_run_id FROM research_working_states)`); err != nil {
		staged.Close()
		t.Fatal(err)
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.MergeSnapshot(ctx, archivePath, nil); err != nil {
		t.Fatal("merge:", err)
	}
	got, err := f.repo.GetResearchWorkingState(ctx, restored.ID, newTaskID)
	if err != nil || got == nil || got.Version != 1 || got.CurrentFocus != first.CurrentFocus || got.Status != "working_note" {
		t.Fatalf("effective restored note: %+v, %v", got, err)
	}
	if got.ID == original.ID || got.ProjectID == original.ProjectID || got.ResearchTaskID == original.ResearchTaskID || got.SourceChatRunID == original.SourceChatRunID || got.SourceUserMessageID == original.SourceUserMessageID || got.SourceCallID == original.SourceCallID {
		t.Fatalf("restored source identities not all remapped: old=%+v new=%+v", original, got)
	}
	var restoredProposal, restoredLatest, originalProposal string
	if err := f.store.DB().QueryRow(`SELECT source_user_message_id FROM research_working_state_proposals WHERE source_call_id=?`, got.SourceCallID).Scan(&restoredProposal); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRow(`SELECT id FROM messages WHERE run_id=? AND role='user' ORDER BY rowid DESC LIMIT 1`, got.SourceChatRunID).Scan(&restoredLatest); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRow(`SELECT source_user_message_id FROM research_working_state_proposals WHERE source_call_id='success'`).Scan(&originalProposal); err != nil {
		t.Fatal(err)
	}
	if restoredProposal != got.SourceUserMessageID || restoredProposal == restoredLatest || originalProposal != answerID {
		t.Fatalf("archive changed proposal-time identity or another project: restored=%s source=%s latest=%s original=%s", restoredProposal, got.SourceUserMessageID, restoredLatest, originalProposal)
	}
	var succeeded, failedCount, pendingCount int
	if err := f.store.DB().QueryRow(`SELECT
  SUM(CASE WHEN tc.status='completed' AND tr.status='success' THEN 1 ELSE 0 END),
  SUM(CASE WHEN tc.status='failed' THEN 1 ELSE 0 END),
  SUM(CASE WHEN tc.status='running' THEN 1 ELSE 0 END)
 FROM research_working_states s JOIN tool_calls tc ON tc.id=s.source_call_id
 LEFT JOIN tool_results tr ON tr.tool_call_id=tc.id
 WHERE s.project_id=? AND s.research_task_id=?`, restored.ID, newTaskID).Scan(&succeeded, &failedCount, &pendingCount); err != nil || succeeded != 1 || failedCount != 1 || pendingCount != 1 {
		t.Fatalf("restored call statuses: success=%d failed=%d pending=%d error=%v", succeeded, failedCount, pendingCount, err)
	}
	var structured, modelContext string
	if err := f.store.DB().QueryRow(`SELECT structured_json,model_context_text FROM tool_results WHERE tool_call_id=?`, got.SourceCallID).Scan(&structured, &modelContext); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(structured, got.ID) || strings.Contains(structured, original.ID) || !strings.Contains(modelContext, got.ID) || !strings.Contains(modelContext, got.SourceCallID) || strings.Contains(modelContext, original.ID) {
		t.Fatalf("state receipt identities were not restored consistently: structured=%s modelContext=%s", structured, modelContext)
	}
	// No restored provisional record can acquire success after its original
	// chat has stopped; the target database's normal finish guard still applies.
	var pendingCall string
	if err := f.store.DB().QueryRow(`SELECT s.source_call_id FROM research_working_states s JOIN tool_calls tc ON tc.id=s.source_call_id WHERE s.project_id=? AND s.research_task_id=? AND tc.status='running'`, restored.ID, newTaskID).Scan(&pendingCall); err != nil {
		t.Fatal(err)
	}
	if err := f.finish(pendingCall, true); err == nil {
		t.Fatal("restored pending note became successful")
	}
}
