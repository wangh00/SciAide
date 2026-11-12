package chat

import (
	"context"
	"encoding/hex"

	"github.com/wangh00/SciAide/internal/modelcap"
)

// ContextUsageObservation is a successful request's accounting anchor, not
// conversation content. Only exact component fingerprints are retained; no
// text, tool result, signature or encrypted reasoning is copied into events.
type ContextUsageObservation struct {
	Version           int                  `json:"version"`
	RunID             string               `json:"runId"`
	TurnIndex         int                  `json:"turnIndex"`
	ModelProfileID    string               `json:"modelProfileId"`
	ModelID           string               `json:"modelId"`
	APIProtocol       modelcap.APIProtocol `json:"apiProtocol"`
	InputTokens       int                  `json:"inputTokens"`
	CharacterEstimate int                  `json:"characterEstimate"`
	Components        map[string]int       `json:"components"`
	// Fingerprints only: restore the exact source subset/order used by this
	// measured request, never source bodies or arbitrary workspace locators.
	StageSourceIDs []string `json:"stageSourceIds,omitempty"`
}

func (o ContextUsageObservation) Matches(run Run) bool {
	if o.Version != 1 || o.RunID != run.ID || o.TurnIndex <= 0 || o.TurnIndex > run.ModelTurns ||
		o.ModelProfileID != run.ModelProfileID || o.ModelID != run.ModelID || o.APIProtocol != run.APIProtocol || !o.APIProtocol.Valid() ||
		o.InputTokens <= 0 || o.InputTokens > 10_000_000 || o.CharacterEstimate <= 0 || o.CharacterEstimate > 100_000_000 || len(o.Components) == 0 || len(o.Components) > 16_384 {
		return false
	}
	for key, count := range o.Components {
		if len(key) != 64 || count <= 0 || count > 16_384 {
			return false
		}
		if _, err := hex.DecodeString(key); err != nil {
			return false
		}
	}
	if len(o.StageSourceIDs) > 24 {
		return false
	}
	for _, key := range o.StageSourceIDs {
		if len(key) != 64 {
			return false
		}
		if _, err := hex.DecodeString(key); err != nil {
			return false
		}
	}
	return true
}

// Optional storage capability: missing/failed observations never block chat.
type ContextUsageRepository interface {
	SaveContextUsage(context.Context, ContextUsageObservation) error
	LatestContextUsage(context.Context, string) (ContextUsageObservation, bool, error)
}
