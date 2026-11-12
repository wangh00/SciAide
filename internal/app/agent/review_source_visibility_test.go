package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

func sourceVisiblePage(t *testing.T, s *reviewSourceSubmission, a, b, index int, pointers ...string) (model.Message, tool.Call) {
	t.Helper()
	pointer := "/context/markdown"
	if len(pointers) == 1 {
		pointer = pointers[0]
	}
	v, err := s.materials.material(pointer)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("read-%d", index)
	page := stageMaterialPage{Section: "stage_input", NodeID: s.materials.contract.NodeID, AddressMode: workflow.StageMaterialAddressMode, Pointer: v.Pointer, Encoding: v.Encoding, SHA256: s.materials.contract.InputSHA256, ValueSHA256: v.ValueSHA256, Content: string([]rune(v.Content)[a:b]), Offset: a, NextOffset: b, TotalCharacters: utf8.RuneCountInString(v.Content)}
	result := &tool.Result{Status: tool.ResultSuccess, Structured: mustAgentJSON(t, page)}
	call := tool.Call{RunID: s.materials.runID, ProviderCallID: id, ToolName: workflow.ResearchTaskReadTool, Status: tool.CallCompleted, Arguments: mustAgentJSON(t, map[string]any{"section": "stage_input", "pointer": v.Pointer, "offset": a}), Result: result}
	return model.Message{Role: model.RoleTool, ToolCallID: id, Content: string(mustAgentJSON(t, map[string]any{"status": tool.ResultSuccess, "structured": page}))}, call
}

func TestFirstReviewSourceVerifiedParentJSONPagesExposeOnlyReadChildRunes(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	_, payload := bindFirstSourceFixture(t, loop, state)
	s, err := loop.loadReviewSourceSubmission(t.Context(), "project", state.run, loop.runs.(workflowContractRuns).contract, true)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := s.materials.material("/context")
	position := strings.Index(parent.Content, "当前")
	start := utf8.RuneCountInString(parent.Content[:position])
	for _, mode := range []string{"full_parent", "partial_parent", "wrapped_parent", "retained_parent", "cut_quote"} {
		t.Run(mode, func(t *testing.T) {
			a, b := 0, utf8.RuneCountInString(parent.Content)
			if mode != "full_parent" {
				a, b = start, start+6
			}
			if mode == "cut_quote" {
				b--
			}
			message, call := sourceVisiblePage(t, s, a, b, 1, "/context")
			r := model.ChatRequest{Tools: []model.ToolDefinition{stageResultDefinition(s.fullSchema)}, Messages: []model.Message{message}}
			if mode == "wrapped_parent" {
				r = stageSubmissionRequest(r, s.fullSchema)
			}
			if mode == "retained_parent" {
				r.Messages = []model.Message{{Role: model.RoleUser, Content: stageMaterialEvidencePrefix + string(mustAgentJSON(t, []any{map[string]any{"inputSha256": s.materials.contract.InputSHA256, "pointer": "/context", "encoding": "json", "valueSha256": parent.ValueSHA256, "offset": a, "endOffset": b, "content": string([]rune(parent.Content)[a:b])}}))}}
			}
			_, err := s.resolve(payload, stageSubmissionTool, r, []tool.Call{call})
			if mode == "cut_quote" {
				var missing *workflow.ReviewSourceVisibilityError
				if !errors.As(err, &missing) {
					t.Fatal("unseen child rune was copied", err)
				}
			} else if err != nil {
				t.Fatal("current visible child in parent JSON was rejected", err)
			}
		})
	}
}

func TestFirstReviewSourceVisibilityRequiresAllSelectedCurrentRunes(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	_, payload := bindFirstSourceFixture(t, loop, state)
	c := loop.runs.(workflowContractRuns).contract
	s, err := loop.loadReviewSourceSubmission(t.Context(), "project", state.run, c, true)
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	_ = json.Unmarshal([]byte(payload), &full)
	basis := full["reviewFindings"].([]any)[0].(map[string]any)["basis"].([]any)[0].(map[string]any)
	delete(basis, "startOffset")
	delete(basis, "endOffset")
	payload = string(mustAgentJSON(t, full))
	v, _ := s.materials.material("/context/markdown")
	total := utf8.RuneCountInString(v.Content)
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, HostToolReferences: true, Content: "<stage_input>\n{\"context\":{\"markdown\":{\"$read\":{\"pointer\":\"/context/markdown\"}}}}\n</stage_input>"}}, Tools: []model.ToolDefinition{stageResultDefinition(s.fullSchema)}}
	a, ca := sourceVisiblePage(t, s, 0, 7, 1)
	b, cb := sourceVisiblePage(t, s, 9, total, 2)
	middle, cm := sourceVisiblePage(t, s, 7, 9, 3)
	request.Messages = append(request.Messages, a, b)
	calls := []tool.Call{ca, cb, cm}
	_, err = s.resolve(payload, stageSubmissionTool, request, calls)
	var unavailable *workflow.ReviewSourceVisibilityError
	if !errors.As(err, &unavailable) {
		t.Fatal("unread middle negation was copied", err)
	}
	request.Messages = append(request.Messages, middle)
	for _, mode := range []string{"ordinary", "native", "submit", "native_submit", "trimmed", "wrong_run"} {
		t.Run(mode, func(t *testing.T) {
			r := request
			r.Messages = cloneModelMessages(request.Messages)
			cs := append([]tool.Call(nil), calls...)
			if mode == "native" || mode == "native_submit" {
				r.ProviderTurns = []model.ProviderTurn{{TurnIndex: 1, ToolResults: r.Messages[1:]}}
				r.Messages = r.Messages[:1]
			}
			if mode == "submit" || mode == "native_submit" {
				r = stageSubmissionRequest(r, s.fullSchema)
			}
			if mode == "trimmed" {
				r = stageSubmissionRequest(r, s.fullSchema)
				for i := range r.Messages {
					if r.Messages[i].EvidenceToolCallID == middle.ToolCallID {
						r.Messages[i].Content = r.Messages[i].EvidencePrefix + "{\"status\":\"success\",\"truncated\":true}"
					}
				}
			}
			if mode == "wrong_run" {
				cs[2].RunID = "old-chat"
			}
			_, err := s.resolve(payload, stageSubmissionTool, r, cs)
			if mode == "trimmed" || mode == "wrong_run" {
				if !errors.As(err, &unavailable) {
					t.Fatal("hidden/stale page proved visible", err)
				}
			} else if err != nil {
				t.Fatal("actual admitted page lost at submission boundary", err)
			}
		})
	}
}

func TestFirstReviewSourceUnseenCompletionStopsWithoutImpossibleRepair(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	h, payload := bindFirstSourceFixture(t, loop, state)
	r := loop.runs.(workflowContractRuns)
	r.contract.PromptText = "<stage_input>\n{\"context\":{\"markdown\":{\"$read\":{\"pointer\":\"/context/markdown\"}}}}\n</stage_input>"
	r.contract.PromptSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(r.contract.PromptText)))
	loop.runs = r
	state.messages[0].Parts = []conversation.MessagePart{{Type: "text", Text: r.contract.PromptText}}
	p := fake.New(submissionToolScript(payload), submissionToolScript(payload))
	loop.models = fakeResolver{p}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_REVIEW_SOURCE_NOT_VISIBLE" || len(p.Requests()) != 1 || len(h.validated) != 0 || state.journalDrafts[1] != payload {
		t.Fatal("unseen completion retried locked IO or entered science", got, state.run.ErrorCode, len(p.Requests()))
	}
}

func TestFirstReviewSourceMenuSharesPrefixAndNeverCopiesHiddenText(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	h, _ := bindFirstSourceFixture(t, loop, state)
	menu := string(h.catalog.SubmissionMenu())
	if strings.Contains(menu, "当前誊写原文") || !strings.Contains(menu, "segmentIdPrefix") || !strings.Contains(menu, "sourceColumns") {
		t.Fatal("menu duplicated evidence or lost full navigation")
	}
	var parsed struct {
		SegmentIDPrefix string
		Sources         [][]json.RawMessage
	}
	_ = json.Unmarshal([]byte(menu), &parsed)
	var first int
	_ = json.Unmarshal(parsed.Sources[0][1], &first)
	if parsed.SegmentIDPrefix+fmt.Sprint(first) != h.catalog.Segments[0].ID {
		t.Fatal("compact navigation points at another execution/source")
	}
}
