package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type workingProvenanceFixture struct {
	*openingRecordFixture
	denied bool
	reads  int
}

func (f *workingProvenanceFixture) ValidateResearchWorkingStateRead(_ context.Context, project, task, chat, call string) error {
	if f.denied || project != "p" || task != "task" || chat != "human" || call != "read" {
		return fmt.Errorf("foreign or closed discussion")
	}
	return nil
}

func (f *workingProvenanceFixture) GetResearchWorkingStateProvenance(_ context.Context, project, task string, version int) (ResearchWorkingStateProvenance, error) {
	f.reads++
	if project != "p" || task != "task" || version != 2 {
		return ResearchWorkingStateProvenance{}, fmt.Errorf("version changed")
	}
	return ResearchWorkingStateProvenance{ProjectID: project, ResearchTaskID: task, StateID: "current", Version: version, Status: "unverified_submission_provenance",
		Fields: []ResearchWorkingStateFieldSource{{Field: "rationale", Status: "recorded", StateID: "old", Version: 1, SourceChatRunID: strings.Repeat("历史🙂\"\n", 80), SourceUserMessageID: "human-1", SourceCallID: "call-1"}}}, nil
}

func TestResearchWorkingStateProvenanceUsesOriginalPermissionAndPaging(t *testing.T) {
	f := &workingProvenanceFixture{openingRecordFixture: &openingRecordFixture{}}
	s := &RuntimeService{repository: f}
	ctx := context.Background()
	full, err := s.GetResearchWorkingStateProvenance(ctx, "p", "task", 2)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(full)
	digest := sha256.Sum256(want)
	command := ResearchWorkingStateReadCommand{ExpectedVersion: 2, Field: "provenance", Limit: 400}
	var content strings.Builder
	for n := 0; ; n++ {
		if n > 20 {
			t.Fatal("provenance paging did not advance")
		}
		raw, err := s.ReadResearchWorkingState(ctx, "p", "task", "human", "read", command)
		var page struct {
			Version, Offset, NextOffset, TotalRunes int
			Field, SHA256, Content                  string
		}
		if err != nil || json.Unmarshal(raw, &page) != nil || page.Version != 2 || page.Offset != command.Offset || page.Field != "provenance" || page.SHA256 != hex.EncodeToString(digest[:]) || page.TotalRunes != len([]rune(string(want))) || len([]rune(page.Content)) > 400 {
			t.Fatal("invalid exact provenance page", string(raw), err)
		}
		content.WriteString(page.Content)
		if page.NextOffset == -1 {
			break
		}
		if page.NextOffset != command.Offset+len([]rune(page.Content)) {
			t.Fatal("provenance cursor skipped source bytes")
		}
		command.Offset = page.NextOffset
	}
	if content.String() != string(want) {
		t.Fatal("paged provenance did not restore the exact original JSON")
	}
	f.denied = true
	before := f.reads
	if _, err := s.ReadResearchWorkingState(ctx, "p", "task", "human", "read", command); err == nil || f.reads != before {
		t.Fatal("permission denial read source history")
	}
	f.denied = false
	command.ExpectedVersion = 1
	if _, err := s.ReadResearchWorkingState(ctx, "p", "task", "human", "read", command); err == nil {
		t.Fatal("stale requested version silently replaced")
	}
	for _, c := range []ResearchWorkingStateReadCommand{{ExpectedVersion: 2, Field: "provenance", Offset: -1}, {ExpectedVersion: 2, Field: "provenance", Limit: 401}, {ExpectedVersion: 2, Field: "provenance", Offset: 100000}} {
		if _, err := s.ReadResearchWorkingState(ctx, "p", "task", "human", "read", c); err == nil {
			t.Fatal("invalid range accepted", c)
		}
	}
}

func TestResearchWorkingStateReadRejectsAmbiguousSourceSelectors(t *testing.T) {
	for _, raw := range []string{`{"expectedVersion":1,"expectedVersion":2,"field":"provenance"}`, `{"expectedVersion":2,"field":"rationale","field":"provenance"}`, `{"expectedVersion":2,"field":"provenance","offset":0,"offset":400}`, `{"expectedVersion":2,"field":"provenance","projectId":"other"}`} {
		if _, err := ParseResearchWorkingStateRead(json.RawMessage(raw)); err == nil {
			t.Fatal("ambiguous selector accepted", raw)
		}
	}
}
