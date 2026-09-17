package workflowai

import (
	"encoding/json"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

type cachedActivity struct {
	revision string
	data     []byte
}

func (b *Bridge) loadActivity(runID, revision string) (workflow.AIStageActivity, bool) {
	b.activityCacheMu.Lock()
	cached, ok := b.activityCache[runID]
	b.activityCacheMu.Unlock()
	var value workflow.AIStageActivity
	if !ok || cached.revision != revision {
		return value, false
	}
	// Return independent slices to keep UI projections from mutating cached state.
	if json.Unmarshal(cached.data, &value) != nil {
		return workflow.AIStageActivity{}, false
	}
	return value, true
}

func (b *Bridge) saveActivity(runID, revision string, value workflow.AIStageActivity) {
	data, err := json.Marshal(value)
	const maxBytes = 4 * 1024 * 1024
	if err != nil || len(data) > maxBytes {
		return
	}
	b.activityCacheMu.Lock()
	defer b.activityCacheMu.Unlock()
	if b.activityCache == nil {
		b.activityCache = map[string]cachedActivity{}
	}
	if prior, ok := b.activityCache[runID]; ok {
		b.activityBytes -= len(prior.data)
	} else {
		b.activityOrder = append(b.activityOrder, runID)
	}
	b.activityCache[runID] = cachedActivity{revision: revision, data: data}
	b.activityBytes += len(data)
	for len(b.activityOrder) > 64 || b.activityBytes > maxBytes {
		oldest := b.activityOrder[0]
		b.activityOrder = b.activityOrder[1:]
		b.activityBytes -= len(b.activityCache[oldest].data)
		delete(b.activityCache, oldest)
	}
}
