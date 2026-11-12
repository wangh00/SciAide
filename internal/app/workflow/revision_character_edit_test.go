package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func characterEditFixture() (RevisionEditBase, string) {
	base := raw(`{"populationAndSampling":["原样保留","独立手写或誉写修改稿"],"n":9007199254740993,"methodSummary":"完全保留"}`)
	input := raw(`{"_reviewCorrections":{"corrections":[{"text":"只将誉写恢复为誊写，不改其他字段。"}]},"_userRevision":{"changes":["旧间字不应成为当前来源"]}}`)
	source := revisionCharacterSource(input, base)
	value := RevisionEditBase{Analysis: base, SHA256: hashJSON(base), Characters: source}
	payload := string(mustJSON(map[string]any{"baseSHA256": value.SHA256, "sourceSHA256": source.SHA256, "characterEdits": []any{map[string]any{"path": "/populationAndSampling/1", "before": "誉", "replacementId": "U+8A8A"}}}))
	return value, payload
}

func TestCharacterEditCopiesExactFrozenRuneAndPreservesOtherFields(t *testing.T) {
	base, payload := characterEditFixture()
	got, err := base.ApplyCharacterEdit(payload)
	want := raw(strings.Replace(string(base.Analysis), "誉写", "誊写", 1))
	if err != nil || !equalRevisionEditBase(got, want) || !strings.Contains(string(got), "9007199254740993") {
		t.Fatal("copied wrong glyph or changed untouched fields", string(got), err)
	}
	for _, entry := range base.Characters.Characters {
		if entry.Text == "间" {
			t.Fatal("stale user instruction entered current assigned correction menu")
		}
		if entry.Text == "誊" && (entry.ID != "U+8A8A" || entry.Pointer != "/_reviewCorrections/corrections/0/text") {
			t.Fatal("wrong source binding")
		}
	}
}

func TestCharacterSourceEmptyReviewHandoffDoesNotReviveOldUserChanges(t *testing.T) {
	base, _ := characterEditFixture()
	for _, handoff := range []string{`{"corrections":[]}`, `{}`, `null`} {
		input := raw(`{"_reviewCorrections":` + handoff + `,"_userRevision":{"changes":["旧誊字不得重新进入本轮菜单"]}}`)
		if source := revisionCharacterSource(input, base.Analysis); source != nil {
			t.Fatal("empty current review handoff revived historical user source")
		}
	}
}

func TestCharacterEditRejectsAmbiguousStaleMixedAndUnofferedSelections(t *testing.T) {
	base, valid := characterEditFixture()
	for _, tc := range []struct{ name, payload string }{
		{"wrong_source", strings.Replace(valid, base.Characters.SHA256, "wrong", 1)},
		{"wrong_base", strings.Replace(valid, base.SHA256, "wrong", 1)},
		{"unknown", strings.Replace(valid, "U+8A8A", "U+8A3C", 1)},
		{"relative", strings.Replace(valid, "/populationAndSampling/1", "populationAndSampling/1", 1)},
		{"too_many_before", strings.Replace(valid, `"before":"誉"`, `"before":"誉写"`, 1)},
		{"mixed_after", strings.Replace(valid, `"before":"誉"`, `"before":"誉","after":"誊"`, 1)},
		{"duplicates", strings.Replace(valid, `"sourceSHA256":`, `"sourceSHA256":"wrong","sourceSHA256":`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := base.ApplyCharacterEdit(tc.payload); err == nil || got != nil {
				t.Fatal("invalid source selection accepted", string(got))
			}
		})
	}
	base.Characters = nil
	if got, err := base.ApplyCharacterEdit(valid); err == nil || got != nil {
		t.Fatal("legacy input acquired unoffered copying control")
	}
}

func TestCharacterSourceOnlyOptsInNewFrozenInputAndRejectsTampering(t *testing.T) {
	s, repo, command, base := boundRevisionEditFixture(t)
	if got, err := s.StageRevisionEdit(t.Context(), command); err != nil || got.Characters != nil {
		t.Fatal("old input acquired a new menu", err)
	}
	node := repo.detail.Run.Compilation.Nodes[0]
	step := repo.detail.Steps[0]
	step.Status, step.Attempt = StepQueued, 1
	repo.detail.Steps[0] = step
	input := mustJSON(map[string]any{"_userRevision": map[string]any{"priorOutput": map[string]any{"analysis": base}, "changes": []string{"把敲写恢复为誊写"}}})
	bound, err := s.bindRevisionEditContext(t.Context(), repo.detail, node, input)
	if err != nil {
		t.Fatal(err)
	}
	step.Status, step.Attempt, step.Input, step.InputSHA256 = StepRunning, 2, bound, hashJSON(bound)
	repo.detail.Steps[0] = step
	repo.execution.InputSHA256, command.InputSHA256 = step.InputSHA256, step.InputSHA256
	got, err := s.StageRevisionEdit(t.Context(), command)
	if err != nil || got == nil || got.Characters == nil {
		t.Fatal("new frozen source not bound", err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(bound, &fields)
	var receipt revisionEditReceipt
	_ = json.Unmarshal(fields["_revisionEdit"], &receipt)
	receipt.CharacterSourceSHA256 = "wrong"
	fields["_revisionEdit"] = mustJSON(receipt)
	step.Input = mustJSON(fields)
	step.InputSHA256 = hashJSON(step.Input)
	repo.detail.Steps[0] = step
	repo.execution.InputSHA256, command.InputSHA256 = step.InputSHA256, step.InputSHA256
	if got, err := s.StageRevisionEdit(t.Context(), command); err == nil || got != nil {
		t.Fatal("broken frozen character source became model correction")
	}
}

func TestCharacterCopyStillRequiresConfirmedWriteScope(t *testing.T) {
	s, repo, step, _, base := designIsolationFixture(t, "frozen_design_material")
	value := RevisionEditBase{Analysis: base, SHA256: hashJSON(base), Characters: revisionCharacterSource(raw(`{"_userRevision":{"changes":["仅把糀改成绊"]}}`), base)}
	for _, index := range []int{0, 1} {
		before := "糀"
		if index == 0 {
			before = "各"
		}
		payload := mustJSON(map[string]any{"baseSHA256": value.SHA256, "sourceSHA256": value.Characters.SHA256, "characterEdits": []any{map[string]any{"path": []string{"/deliverables/0/markdown", "/deliverables/1/markdown"}[index], "before": before, "replacementId": "U+7ECA"}}})
		got, err := value.ApplyCharacterEdit(string(payload))
		if err != nil {
			t.Fatal(err)
		}
		err = s.validateDesignRevisionWrites(t.Context(), repo.detail, step, got)
		if (err == nil) != (index == 1) {
			t.Fatal("character copying bypassed material scope", index, err)
		}
	}
}
