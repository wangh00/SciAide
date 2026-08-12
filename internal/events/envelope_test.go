package events

import (
	"encoding/json"
	"testing"
)

func TestNewUsesCurrentVersionAndUTC(t *testing.T) {
	event := New("event-1", "run-1", "run", "run.started", 1, json.RawMessage(`{"ok":true}`))
	if event.Version != CurrentVersion || event.Sequence != 1 {
		t.Fatalf("event = %#v", event)
	}
	if event.Timestamp.Location().String() != "UTC" {
		t.Fatalf("timestamp location = %v, want UTC", event.Timestamp.Location())
	}
}
