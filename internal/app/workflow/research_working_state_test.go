package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestResearchWorkingStateUpdateContract(t *testing.T) {
	valid := []string{
		`{"expectedVersion":0,"currentFocus":"问题","rationale":"理由"}`,
		`{"expectedVersion":2,"currentFocus":"问题","rationale":"理由","nextStep":"","unknowns":[]}`,
	}
	for _, raw := range valid {
		value, err := ParseResearchWorkingStateUpdate(json.RawMessage(raw))
		if err != nil || value.Unknowns == nil {
			t.Fatalf("valid state %s: %+v, %v", raw, value, err)
		}
	}
	invalid := []string{
		`{"currentFocus":"x","rationale":"y"}`,
		`{"expectedVersion":-1,"currentFocus":"x","rationale":"y"}`,
		`{"expectedVersion":0,"currentFocus":"  ","rationale":"y"}`,
		`{"expectedVersion":0,"currentFocus":"x","rationale":"y","unknowns":[" "]}`,
		`{"expectedVersion":0,"currentFocus":"x","rationale":"y","projectId":"other"}`,
		`{"expectedVersion":0,"expectedVersion":1,"currentFocus":"x","rationale":"y"}`,
		`{"expectedVersion":0,"currentFocus":"` + strings.Repeat("文", 1001) + `","rationale":"y"}`,
		`{"expectedVersion":0,"currentFocus":"x","rationale":"y","unknowns":["1","2","3","4","5","6","7","8","9"]}`,
	}
	for _, raw := range invalid {
		if _, err := ParseResearchWorkingStateUpdate(json.RawMessage(raw)); err == nil {
			t.Errorf("unexpectedly accepted %s", raw)
		}
	}
}

func TestResearchWorkingStateCommandRoundTrip(t *testing.T) {
	for _, focus := range []string{"focus", "新轮意见"} {
		c, err := NormalizeResearchWorkingStateUpdate(ResearchWorkingStateUpdateCommand{CurrentFocus: focus, Rationale: "why", NextStep: "inspect", Unknowns: []string{"unverified"}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(c)
		got, err := ParseResearchWorkingStateUpdate(raw)
		if err != nil || !reflect.DeepEqual(c, got) {
			t.Fatalf("normalized command round-trip mismatch: %#v / %#v / %s / %v", c, got, raw, err)
		}
	}
}

func TestResearchWorkingStatePatchPreservesUnreadFields(t *testing.T) {
	command, err := ParseResearchWorkingStateUpdate(json.RawMessage(`{"expectedVersion":1,"nextStep":"先解释冲突，不扩大检索"}`))
	if err != nil {
		t.Fatal(err)
	}
	base := &ResearchWorkingState{CurrentFocus: "old focus", Rationale: "old reason", NextStep: "old step", Unknowns: []string{"must remain unknown", "no full text"}}
	got := ApplyResearchWorkingStateUpdate(base, command)
	if got.CurrentFocus != base.CurrentFocus || got.Rationale != base.Rationale || len(got.Unknowns) != 2 || got.Unknowns[0] != base.Unknowns[0] || got.NextStep == base.NextStep {
		t.Fatal("sparse patch erased unread state", got)
	}
	encoded, _ := json.Marshal(command)
	if strings.Contains(string(encoded), "unknowns") || strings.Contains(string(encoded), "currentFocus") {
		t.Fatal("patch became a complete replacement", string(encoded))
	}
	if _, err := ParseResearchWorkingStateUpdate(json.RawMessage(`{"expectedVersion":1}`)); err == nil {
		t.Fatal("empty patch accepted")
	}
	if _, err := ParseResearchWorkingStateUpdate(json.RawMessage(`{"expectedVersion":0,"nextStep":"initial"}`)); err == nil {
		t.Fatal("initial state lacks focus/reason")
	}
}

func TestResearchWorkingStateMetadataNeverPinsBodyOrSilentlyTruncates(t *testing.T) {
	note := &ResearchWorkingState{Version: 3, CurrentFocus: strings.Repeat("焦", 1000), Rationale: strings.Repeat("因", 1000), NextStep: strings.Repeat("步", 1000), Unknowns: []string{strings.Repeat("疑", 400)}}
	raw := ResearchWorkingStateMetadata(note)
	if len(raw) > 256 || !strings.Contains(string(raw), `"version":3`) || !strings.Contains(string(raw), `"contentOmitted":true`) || strings.Contains(string(raw), "焦") {
		t.Fatal("working body was pinned or omission hidden", string(raw))
	}
	oversized := `{"expectedVersion":1,"unknowns":["` + strings.Repeat("x", 400) + `","` + strings.Repeat("x", 400) + `","` + strings.Repeat("x", 400) + `","` + strings.Repeat("x", 400) + `","` + strings.Repeat("x", 400) + `"]}`
	if len(oversized) <= MaxResearchStateUpdateBytes {
		t.Fatal("fixture is not over the aggregate wire limit")
	}
	if _, err := ParseResearchWorkingStateUpdate(json.RawMessage(oversized)); err == nil {
		t.Fatal("large native argument accepted before persistence")
	}
}
