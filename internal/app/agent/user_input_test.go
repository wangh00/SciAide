package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/model"
)

type questionWaitFixture struct {
	state  *loopState
	callID string
}

func (w *questionWaitFixture) WaitForUserInput(_ context.Context, runID, callID string) (chat.Run, error) {
	w.callID = callID
	w.state.run.Status = chat.RunWaitingUserInput
	w.state.run.PendingUserInputCallID = callID
	return w.state.run, nil
}

type forbiddenQuestionCoordinator struct{ t *testing.T }

func (c forbiddenQuestionCoordinator) EvaluateCall(context.Context, string, string) (coord permission.Coordination, err error) {
	c.t.Fatal("question/batch touched permission before user answer")
	return
}

type forbiddenQuestionExecutor struct{ t *testing.T }

func (e forbiddenQuestionExecutor) Execute(context.Context, string, string) (execution tool.Execution, err error) {
	e.t.Fatal("question/batch touched executor before user answer")
	return
}

func TestUserQuestionPausesWholeBatchBeforePermissionsOrExecution(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	state.run.Status = chat.RunRunning
	wait := &questionWaitFixture{state: state}
	loop.userInput = wait
	loop.approvals = forbiddenQuestionCoordinator{t}
	loop.executor = forbiddenQuestionExecutor{t}
	calls := []tool.Call{
		{ID: "other", ToolName: "builtin.fixture", Status: tool.CallPending},
		{ID: "question", ToolName: userinput.ToolName, Status: tool.CallPending, Arguments: json.RawMessage(`{"questions":[{"id":"goal","question":"目标？","required":true}]}`)},
	}
	waiting, err := loop.processCalls(context.Background(), &state.run, "project", calls)
	if err != nil || !waiting || wait.callID != "question" || state.run.Status != chat.RunWaitingUserInput {
		t.Fatal(waiting, err, state.run)
	}
}

func TestMalformedQuestionBecomesToolErrorWithoutKillingConversation(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	state.run.Status = chat.RunRunning
	call := tool.Call{ID: "question", ToolName: userinput.ToolName, Status: tool.CallPending, Arguments: json.RawMessage(`{"questions":[]}`)}
	state.calls[call.ID] = call
	state.callOrder = []string{call.ID}
	waiting, err := loop.processCalls(context.Background(), &state.run, "project", []tool.Call{call})
	if err != nil || waiting || state.run.Status != chat.RunRunning || state.calls[call.ID].Status != tool.CallFailed {
		t.Fatal(waiting, err, state.calls[call.ID])
	}
}

func TestHumanQuestionAnswersAreNotTruncatedInNormalizedOrNativeHistory(t *testing.T) {
	text := strings.Repeat("决策", 3000) + "中间关键条件" + strings.Repeat("范围", 3000) + "末尾关键条件"
	structured, err := json.Marshal(map[string]any{"answers": userinput.Answers{"first": {Text: text[:len(text)/2]}, "second": {Text: "结构化完整回答"}}})
	if err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "question", ProviderCallID: "provider-question", ToolName: userinput.ToolName, Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured}}
	call.ModelContextVersion = tool.ModelContextSnapshotVersion
	call.ModelContext = tool.BuildModelContextSnapshot(call.ID, "", *call.Result)
	if strings.Contains(call.ModelContext, "中间关键条件") {
		t.Fatal("fixture must exercise an already truncated stored preview")
	}
	expected := toolCallContext(call)
	var decoded struct {
		Text       string          `json:"text"`
		Structured json.RawMessage `json:"structured"`
	}
	if err = json.Unmarshal([]byte(expected), &decoded); err != nil || decoded.Text != text || string(decoded.Structured) != string(structured) {
		t.Fatal("human reply must be complete valid JSON", err)
	}
	normalized, _, _, _, err := newestToolMessages([]tool.Call{call}, 100000, 1000)
	if err != nil || len(normalized) != 2 || normalized[1].Content != expected {
		t.Fatal("human answer truncated", err)
	}
	turns := []model.ProviderTurn{{TurnIndex: 1, Items: []model.ProviderItem{{Ordinal: 0, CallID: call.ProviderCallID, Payload: json.RawMessage(`{}`)}}}}
	native, _, _, _, _, _, err := newestProviderTurns(turns, []tool.Call{call}, 100000, 1000)
	if err != nil || native[0].ToolResults[0].Content != expected {
		t.Fatal("native human answer truncated", err)
	}
	if _, _, _, _, err = newestToolMessages([]tool.Call{call}, 1000, 1000); err == nil {
		t.Fatal("silently truncated human answer for tiny budget")
	}
}
