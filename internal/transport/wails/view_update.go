package wails

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

// The client owns its last view. The host keeps no per-window payload cache.
type ViewUpdate struct {
	Revision  string          `json:"revision"`
	Unchanged bool            `json:"unchanged"`
	Value     json.RawMessage `json:"value,omitempty"`
	Patch     json.RawMessage `json:"patch,omitempty"`
	Removed   []string        `json:"removed,omitempty"`
}

// Field revisions avoid retransmitting static prompts/code when only live
// activity changes. The host retains no payloads and still performs scoped reads.
func conditionalObjectView(value any, revision string, err error) (ViewUpdate, error) {
	if err != nil {
		return ViewUpdate{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ViewUpdate{}, err
	}
	var fields map[string]json.RawMessage
	if bytes.Equal(data, []byte("null")) || json.Unmarshal(data, &fields) != nil || fields == nil {
		return conditionalView(value, revision, nil)
	}
	prior := map[string]string{}
	valid := len(revision) <= 16384 && json.Unmarshal([]byte(revision), &prior) == nil && prior != nil
	current := make(map[string]string, len(fields))
	patch := map[string]json.RawMessage{}
	for key, field := range fields {
		hash := sha256.Sum256(field)
		current[key] = hex.EncodeToString(hash[:])
		if !valid || prior[key] != current[key] {
			patch[key] = field
		}
	}
	var removed []string
	for key := range prior {
		if _, ok := fields[key]; !ok {
			removed = append(removed, key)
		}
	}
	manifest, _ := json.Marshal(current)
	sort.Strings(removed)
	update := ViewUpdate{Revision: string(manifest)}
	if !valid {
		update.Value = data
		return update, nil
	}
	if len(patch) == 0 && len(removed) == 0 {
		update.Unchanged = true
		return update, nil
	}
	update.Patch, err = json.Marshal(patch)
	update.Removed = removed
	return update, err
}

func conditionalView(value any, revision string, err error) (ViewUpdate, error) {
	if err != nil {
		return ViewUpdate{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ViewUpdate{}, err
	}
	hash := sha256.Sum256(data)
	current := hex.EncodeToString(hash[:])
	if current == revision {
		return ViewUpdate{Revision: current, Unchanged: true}, nil
	}
	return ViewUpdate{Revision: current, Value: data}, nil
}

func (f *ChatFacade) PollRunSnapshot(runID, revision string) (ViewUpdate, error) {
	value, err := f.GetRunSnapshot(runID)
	return conditionalObjectView(value, revision, err)
}

func (f *ChatFacade) PollLatestRunSnapshot(conversationID, revision string) (ViewUpdate, error) {
	value, err := f.GetLatestRunSnapshot(conversationID)
	return conditionalObjectView(value, revision, err)
}

func (f *WorkflowFacade) PollRun(projectID, runID, revision string) (ViewUpdate, error) {
	value, err := f.GetRun(projectID, runID)
	return conditionalObjectView(value, revision, err)
}

func (f *WorkflowFacade) PollResearchTimeline(query workflow.ResearchTimelineQuery, revision string) (ViewUpdate, error) {
	value, err := f.ResearchTimeline(query)
	return conditionalView(value, revision, err)
}
