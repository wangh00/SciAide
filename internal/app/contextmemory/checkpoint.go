package contextmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/modelcap"
)

const MaxSummaryTokens = 20_000

type Checkpoint struct {
	ID                    string               `json:"id"`
	ConversationID        string               `json:"conversationId"`
	Revision              int                  `json:"revision"`
	ThroughMessageID      string               `json:"throughMessageId"`
	Summary               string               `json:"summary"`
	CheckpointSHA256      string               `json:"checkpointSha256"`
	SourceMessageCount    int                  `json:"sourceMessageCount"`
	SourceEstimatedTokens int                  `json:"sourceEstimatedTokens"`
	ModelProfileID        string               `json:"modelProfileId"`
	ModelID               string               `json:"modelId"`
	APIProtocol           modelcap.APIProtocol `json:"apiProtocol"`
	CreatedAt             time.Time            `json:"createdAt"`
}

type Repository interface {
	Latest(ctx context.Context, conversationID string) (Checkpoint, bool, error)
	Save(ctx context.Context, value Checkpoint) (Checkpoint, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Latest(ctx context.Context, conversationID string) (Checkpoint, bool, error) {
	return s.repository.Latest(ctx, strings.TrimSpace(conversationID))
}

func (s *Service) Save(ctx context.Context, value Checkpoint) (Checkpoint, error) {
	value.ConversationID = strings.TrimSpace(value.ConversationID)
	value.ThroughMessageID = strings.TrimSpace(value.ThroughMessageID)
	value.Summary = strings.TrimSpace(value.Summary)
	value.ModelProfileID = strings.TrimSpace(value.ModelProfileID)
	value.ModelID = strings.TrimSpace(value.ModelID)
	if value.ConversationID == "" || value.ThroughMessageID == "" || value.Summary == "" {
		return Checkpoint{}, fmt.Errorf("conversation, boundary message and summary are required")
	}
	if len([]rune(value.Summary)) > MaxSummaryTokens {
		return Checkpoint{}, fmt.Errorf("context checkpoint summary exceeds %d tokens", MaxSummaryTokens)
	}
	if value.SourceMessageCount <= 0 || value.SourceEstimatedTokens <= 0 {
		return Checkpoint{}, fmt.Errorf("context checkpoint source accounting is required")
	}
	if !value.APIProtocol.Valid() {
		return Checkpoint{}, fmt.Errorf("context checkpoint API protocol is invalid")
	}
	var err error
	value.ID, err = id.New()
	if err != nil {
		return Checkpoint{}, err
	}
	value.CheckpointSHA256 = checkpointHash(value)
	value.CreatedAt = s.now()
	return s.repository.Save(ctx, value)
}

func Verify(value Checkpoint) error {
	if !strings.EqualFold(value.CheckpointSHA256, checkpointHash(value)) {
		return fmt.Errorf("context checkpoint payload hash mismatch")
	}
	return nil
}

func checkpointHash(value Checkpoint) string {
	payload, _ := json.Marshal(struct {
		ConversationID        string               `json:"conversation_id"`
		ThroughMessageID      string               `json:"through_message_id"`
		Summary               string               `json:"summary"`
		SourceMessageCount    int                  `json:"source_message_count"`
		SourceEstimatedTokens int                  `json:"source_estimated_tokens"`
		ModelProfileID        string               `json:"model_profile_id"`
		ModelID               string               `json:"model_id"`
		APIProtocol           modelcap.APIProtocol `json:"api_protocol"`
	}{value.ConversationID, value.ThroughMessageID, value.Summary, value.SourceMessageCount, value.SourceEstimatedTokens, value.ModelProfileID, value.ModelID, value.APIProtocol})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}
