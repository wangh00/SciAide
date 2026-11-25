package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func workingPatch(t *testing.T, raw string) workflow.ResearchWorkingStateUpdateCommand {
	t.Helper()
	command, err := workflow.ParseResearchWorkingStateUpdate(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func TestResearchWorkingStateFieldSourcesDeriveRemappedArchiveIdentities(t *testing.T) {
	f := newWorkingStateFixture(t)
	publishWorkingPatch(f, "first", "chat", `{"expectedVersion":0,"currentFocus":"旧关注","rationale":"原理由"}`)
	publishWorkingPatch(f, "second", "chat", `{"expectedVersion":1,"nextStep":"新步骤"}`)
	f.exec(`UPDATE runs SET status='completed' WHERE id='chat'`)
	ctx := context.Background()
	original, err := f.repo.GetResearchWorkingStateProvenance(ctx, "p", "task", 2)
	if err != nil {
		t.Fatal(err)
	}
	archive := NewProjectArchiveRepository(f.store.DB())
	file := filepath.Join(t.TempDir(), "provenance.db")
	if _, err := archive.CreateSnapshot(ctx, "p", file); err != nil {
		t.Fatal(err)
	}
	restored := project.Project{ID: "restored", Name: "Restored", WorkspacePath: t.TempDir(), CreatedAt: f.now, UpdatedAt: f.now}
	plan, err := archive.RewriteSnapshot(ctx, file, restored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.MergeSnapshot(ctx, file, nil); err != nil {
		t.Fatal(err)
	}
	got, err := f.repo.GetResearchWorkingStateProvenance(ctx, restored.ID, plan.TaskIDs["task"], 2)
	if err != nil || got.Version != 2 || got.StateID == original.StateID || len(got.Fields) != 4 {
		t.Fatal("restored provenance lost host identity", got, err)
	}
	for i, source := range got.Fields {
		if source.Field != original.Fields[i].Field || source.Status != original.Fields[i].Status || source.Version != original.Fields[i].Version {
			t.Fatal("restore inferred a new field submission", source)
		}
		if source.Status == "recorded" && (source.StateID == original.Fields[i].StateID || source.SourceCallID == original.Fields[i].SourceCallID || source.SourceChatRunID == original.Fields[i].SourceChatRunID || source.SourceUserMessageID == original.Fields[i].SourceUserMessageID) {
			t.Fatal("restored provenance leaked original live source identities", source)
		}
	}
	unchanged, err := f.repo.GetResearchWorkingStateProvenance(ctx, "p", "task", 2)
	if err != nil || !reflect.DeepEqual(original, unchanged) {
		t.Fatal("restoring source identities mutated the original project", unchanged, err)
	}
}

func publishWorkingPatch(f *workingStateFixture, call, chat, raw string) workflow.ResearchWorkingState {
	f.t.Helper()
	cmd := workingPatch(f.t, raw)
	f.call(call, chat, cmd)
	value, err := f.save(call, chat, cmd)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.finish(call, true); err != nil {
		f.t.Fatal(err)
	}
	return value
}

func TestResearchWorkingStateFieldSourcesPreserveSparseOrigins(t *testing.T) {
	f := newWorkingStateFixture(t)
	publishWorkingPatch(f, "first", "chat", `{"expectedVersion":0,"currentFocus":"旧关注🧪","rationale":"原理由","unknowns":["未核读全文"]}`)
	f.exec(`UPDATE runs SET status='completed' WHERE id='chat'`)
	f.chat("chat-2", "conv", "human-2")
	second := publishWorkingPatch(f, "second", "chat-2", `{"expectedVersion":1,"nextStep":"比较冲突"}`)
	beforeStates := databaseRows(t, f.store.DB(), "research_working_states")
	value, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 2)
	if err != nil || value.StateID != second.ID || value.Status != "unverified_submission_provenance" || len(value.Fields) != 4 {
		t.Fatal("missing field source view", value, err)
	}
	for _, source := range value.Fields {
		wantVersion, wantCall, wantChat, wantHuman := 1, "first", "chat", "human"
		if source.Field == "nextStep" {
			wantVersion, wantCall, wantChat, wantHuman = 2, "second", "chat-2", "human-2"
		}
		if source.Status != "recorded" || source.Version != wantVersion || source.SourceCallID != wantCall || source.SourceChatRunID != wantChat || source.SourceUserMessageID != wantHuman {
			t.Fatal("inherited field attributed to latest patch", source)
		}
	}
	// Explicitly submitting an unchanged value is a new submission, not a
	// semantic claim that it was newly discovered, changed or confirmed.
	f.exec(`UPDATE runs SET status='completed' WHERE id='chat-2'`)
	f.chat("chat-3", "conv", "human-3")
	publishWorkingPatch(f, "third", "chat-3", `{"expectedVersion":2,"rationale":"原理由","unknowns":[]}`)
	value, err = f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range value.Fields {
		if source.Field == "rationale" || source.Field == "unknowns" {
			if source.Version != 3 || source.SourceCallID != "third" {
				t.Fatal("explicit same-value/empty-array submission treated as inheritance", source)
			}
		}
	}
	if _, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 2); err == nil {
		t.Fatal("stale provenance silently followed a newer version")
	}
	// A view read cannot mutate previously stored field values or provenance.
	after := databaseRows(t, f.store.DB(), "research_working_states")
	if len(after) != len(beforeStates)+1 {
		t.Fatal("read wrote history")
	}
	for _, original := range beforeStates {
		found := false
		for _, row := range after {
			if reflect.DeepEqual(original[0], row[0]) {
				found = true
				if !reflect.DeepEqual(original, row) {
					t.Fatal("source view changed a previously stored row")
				}
			}
		}
		if !found {
			t.Fatal("source view deleted previous history")
		}
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = NewWorkflowRuntimeRepository(f.store.DB())
	reopened, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 3)
	if err != nil || !reflect.DeepEqual(value, reopened) {
		t.Fatal("reopen guessed or lost field history", reopened, err)
	}
}

func TestResearchWorkingStateFieldSourcesDoNotGuessMissingOrInvalidHistory(t *testing.T) {
	tests := []struct {
		name   string
		change func(*workingStateFixture)
	}{
		{"missing first submission", func(f *workingStateFixture) { f.exec(`DELETE FROM tool_calls WHERE id='first'`) }},
		{"invalid source arguments", func(f *workingStateFixture) {
			f.exec(`UPDATE tool_calls SET arguments_json='{"expectedVersion":0,"currentFocus":42,"rationale":"why"}' WHERE id='first'`)
		}},
		{"duplicate source arguments", func(f *workingStateFixture) {
			f.exec(`UPDATE tool_calls SET arguments_json='{"expectedVersion":0,"currentFocus":"old","currentFocus":"other","rationale":"why"}' WHERE id='first'`)
		}},
		{"mismatched source arguments", func(f *workingStateFixture) {
			f.exec(`UPDATE tool_calls SET arguments_json='{"expectedVersion":0,"currentFocus":"other","rationale":"why"}' WHERE id='first'`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			publishWorkingPatch(f, "first", "chat", `{"expectedVersion":0,"currentFocus":"old","rationale":"why"}`)
			publishWorkingPatch(f, "second", "chat", `{"expectedVersion":1,"nextStep":"new"}`)
			tc.change(f)
			value, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 2)
			if err != nil {
				t.Fatal(err)
			}
			if value.Fields[0].Status != "unavailable" || value.Fields[0].SourceCallID != "" {
				t.Fatal("guessed inherited field source", value.Fields)
			}
			if tc.name == "missing first submission" && value.Fields[2].SourceCallID != "second" {
				t.Fatal("missing history erased known current explicit submission", value.Fields)
			}
		})
	}
	f := newWorkingStateFixture(t)
	publishWorkingPatch(f, "first", "chat", `{"expectedVersion":0,"currentFocus":"old","rationale":"why"}`)
	value, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 1)
	if err != nil || value.Fields[2].Status != "not_recorded" || value.Fields[3].Status != "not_recorded" {
		t.Fatal("unsubmitted default field assigned to user", value, err)
	}
	for _, ids := range [][2]string{{"other", "task"}, {"p", "foreign"}} {
		if _, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), ids[0], ids[1], 1); err == nil {
			t.Fatal("foreign scope accepted", ids)
		}
	}
	f.exec(`UPDATE research_tasks SET status='archived' WHERE id='task'`)
	if _, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 1); err == nil {
		t.Fatal("archived task provenance was exposed")
	}
}

func TestResearchWorkingStateFieldSourcesGrowingHistoryAndUnpublishedCalls(t *testing.T) {
	f := newWorkingStateFixture(t)
	publishWorkingPatch(f, "initial", "chat", `{"expectedVersion":0,"currentFocus":"原关注","rationale":"原理由"}`)
	for i := 1; i <= 128; i++ {
		publishWorkingPatch(f, fmt.Sprint("next-", i), "chat", fmt.Sprintf(`{"expectedVersion":%d,"nextStep":"步骤%d"}`, i, i))
	}
	cmd := workingPatch(t, `{"expectedVersion":129,"rationale":"尚未发布的理由"}`)
	f.call("pending", "chat", cmd)
	if _, err := f.save("pending", "chat", cmd); err != nil {
		t.Fatal(err)
	}
	read := func() workflow.ResearchWorkingStateProvenance {
		value, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 129)
		if err != nil {
			t.Fatal(err)
		}
		if value.Fields[0].Version != 1 || value.Fields[1].Version != 1 || value.Fields[2].Version != 129 || value.Fields[3].Status != "not_recorded" {
			t.Fatal("growing history or provisional write changed a field origin", value.Fields)
		}
		return value
	}
	before := read()
	if _, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 130); err == nil {
		t.Fatal("uncompleted source note was exposed")
	}
	if err := f.finish("pending", false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("failed source write changed provenance")
	}
}
