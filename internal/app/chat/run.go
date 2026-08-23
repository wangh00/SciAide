package chat

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
	RunInterrupted     RunStatus = "interrupted"
)

type Run struct {
	ID                            string                      `json:"id"`
	ConversationID                string                      `json:"conversationId"`
	UserMessageID                 string                      `json:"userMessageId"`
	AssistantMessageID            string                      `json:"assistantMessageId,omitempty"`
	ModelProfileID                string                      `json:"modelProfileId"`
	ModelID                       string                      `json:"modelId"`
	APIProtocol                   modelcap.APIProtocol        `json:"apiProtocol"`
	RequestedReasoningLevel       modelcap.ReasoningLevel     `json:"requestedReasoningLevel"`
	ResolvedReasoningLevel        modelcap.ReasoningLevel     `json:"resolvedReasoningLevel,omitempty"`
	ContextWindowTokens           int                         `json:"contextWindowTokens"`
	ContextBudgetTokens           int                         `json:"contextBudgetTokens"`
	AutoCompactTokenLimit         int                         `json:"autoCompactTokenLimit"`
	ContextWindowSource           string                      `json:"contextWindowSource"`
	ContextCompacted              bool                        `json:"contextCompacted"`
	PermissionMode                conversation.PermissionMode `json:"permissionMode"`
	Status                        RunStatus                   `json:"status"`
	ErrorCode                     string                      `json:"errorCode,omitempty"`
	ErrorMessage                  string                      `json:"errorMessage,omitempty"`
	ErrorDetails                  string                      `json:"errorDetails,omitempty"`
	InputTokens                   int                         `json:"inputTokens"`
	FreshInputTokens              int                         `json:"freshInputTokens"`
	OutputTokens                  int                         `json:"outputTokens"`
	ReasoningTokens               int                         `json:"reasoningTokens"`
	ReasoningObserved             bool                        `json:"reasoningObserved"`
	ReasoningSignatureObserved    bool                        `json:"reasoningSignatureObserved"`
	ReasoningSummary              string                      `json:"reasoningSummary,omitempty"`
	CachedInputTokens             int                         `json:"cachedInputTokens"`
	CacheWriteTokens              int                         `json:"cacheWriteTokens"`
	CacheReportedTurns            int                         `json:"cacheReportedTurns"`
	CacheReportedFreshInputTokens int                         `json:"cacheReportedFreshInputTokens"`
	CacheHitTurns                 int                         `json:"cacheHitTurns"`
	ModelTurns                    int                         `json:"modelTurns"`
	FinishReason                  string                      `json:"finishReason,omitempty"`
	CreatedAt                     time.Time                   `json:"createdAt"`
	StartedAt                     *time.Time                  `json:"startedAt,omitempty"`
	CompletedAt                   *time.Time                  `json:"completedAt,omitempty"`
	UpdatedAt                     time.Time                   `json:"updatedAt"`
}

// RunStep is a model-turn activity record. It deliberately lives outside the
// assistant message so commentary emitted before a tool call can never become
// part of the final answer.
type RunStep struct {
	RunID                      string    `json:"runId"`
	TurnIndex                  int       `json:"turnIndex"`
	Commentary                 string    `json:"commentary,omitempty"`
	ReasoningSummary           string    `json:"reasoningSummary,omitempty"`
	ReasoningObserved          bool      `json:"reasoningObserved"`
	ReasoningSignatureObserved bool      `json:"reasoningSignatureObserved"`
	CreatedAt                  time.Time `json:"createdAt"`
	CompletedAt                time.Time `json:"completedAt"`
}

type ModelTurnStatus string

const (
	ModelTurnStreaming   ModelTurnStatus = "streaming"
	ModelTurnCompleted   ModelTurnStatus = "completed"
	ModelTurnInterrupted ModelTurnStatus = "interrupted"
	ModelTurnFailed      ModelTurnStatus = "failed"
)

// ModelTurnJournal is the durable, non-UI record of one provider request
// step. DraftText contains only provider-visible answer/commentary text, never
// hidden reasoning payloads.
type ModelTurnJournal struct {
	RunID             string          `json:"runId"`
	TurnIndex         int             `json:"turnIndex"`
	Status            ModelTurnStatus `json:"status"`
	DraftText         string          `json:"draftText,omitempty"`
	FinishReason      string          `json:"finishReason,omitempty"`
	ProviderItemCount int             `json:"providerItemCount"`
	StartedAt         time.Time       `json:"startedAt"`
	CompletedAt       *time.Time      `json:"completedAt,omitempty"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type Repository interface {
	CreateWithMessages(ctx context.Context, value Run, userMessage, assistantMessage conversation.Message) error
	Get(ctx context.Context, id string) (Run, error)
	LatestForConversation(ctx context.Context, conversationID string) (Run, bool, error)
	Update(ctx context.Context, value Run) error
	IncrementModelTurns(ctx context.Context, runID string, at time.Time) (Run, error)
	CancelRun(ctx context.Context, runID, errorCode, errorMessage string, at time.Time, event events.Envelope) (Run, bool, error)
	InterruptActive(ctx context.Context, at time.Time) (int64, error)
	RecordModelUsage(ctx context.Context, value RequestUsage) (Run, bool, error)
	UsageDashboard(ctx context.Context, query UsageQuery) (UsageDashboard, error)
	UsageRequests(ctx context.Context, query UsageRequestQuery) (UsageRequestPage, error)
}

// UsageQuery uses local calendar dates (YYYY-MM-DD). Empty dimensions mean
// all API profiles/models, so changing the active chat model never changes
// the default client-wide statistics.
type UsageQuery struct {
	StartDate      string `json:"startDate,omitempty"`
	EndDate        string `json:"endDate,omitempty"`
	StartTime      string `json:"startTime,omitempty"`
	EndTime        string `json:"endTime,omitempty"`
	ModelProfileID string `json:"modelProfileId,omitempty"`
	ModelID        string `json:"modelId,omitempty"`
}

type RequestUsage struct {
	ID                   string               `json:"id"`
	RunID                string               `json:"runId"`
	TurnIndex            int                  `json:"turnIndex"`
	RequestKind          string               `json:"requestKind"`
	ModelProfileID       string               `json:"modelProfileId"`
	ProfileName          string               `json:"profileName,omitempty"`
	ModelID              string               `json:"modelId"`
	APIProtocol          modelcap.APIProtocol `json:"apiProtocol"`
	InputTokens          int                  `json:"inputTokens"`
	FreshInputTokens     int                  `json:"freshInputTokens"`
	OutputTokens         int                  `json:"outputTokens"`
	ReasoningTokens      int                  `json:"reasoningTokens"`
	CachedInputTokens    int                  `json:"cachedInputTokens"`
	CacheWriteTokens     int                  `json:"cacheWriteTokens"`
	CacheDetailsReported bool                 `json:"cacheDetailsReported"`
	StatusCode           int                  `json:"statusCode"`
	ErrorCode            string               `json:"errorCode,omitempty"`
	ErrorMessage         string               `json:"errorMessage,omitempty"`
	FirstTokenMillis     *int64               `json:"firstTokenMillis,omitempty"`
	IsStreaming          bool                 `json:"isStreaming"`
	StartedAt            time.Time            `json:"startedAt"`
	CompletedAt          time.Time            `json:"completedAt"`
	DurationMillis       int64                `json:"durationMillis"`
}

type UsageRequestQuery struct {
	StartDate      string `json:"startDate,omitempty"`
	EndDate        string `json:"endDate,omitempty"`
	StartTime      string `json:"startTime,omitempty"`
	EndTime        string `json:"endTime,omitempty"`
	ModelProfileID string `json:"modelProfileId,omitempty"`
	ModelID        string `json:"modelId,omitempty"`
	StatusCode     int    `json:"statusCode,omitempty"`
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type UsageRequestPage struct {
	Items  []RequestUsage    `json:"items"`
	Total  int               `json:"total"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
	Query  UsageRequestQuery `json:"query"`
}

// UsageSummary is cache-normalized into four mutually exclusive buckets.
// CacheHitRate is a fraction in [0,1], calculated from cache-reporting turns
// only so a provider which omits cache fields is not recorded as a miss.
type UsageSummary struct {
	RunCount            int     `json:"runCount"`
	ModelTurns          int     `json:"modelTurns"`
	RequestCount        int     `json:"requestCount"`
	SuccessfulRequests  int     `json:"successfulRequests"`
	FailedRequests      int     `json:"failedRequests"`
	SuccessRate         float64 `json:"successRate"`
	FreshInputTokens    int     `json:"freshInputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	ReasoningTokens     int     `json:"reasoningTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	RealTotalTokens     int     `json:"realTotalTokens"`
	CacheReportedTurns  int     `json:"cacheReportedTurns"`
	CacheHitTurns       int     `json:"cacheHitTurns"`
	CacheHitRate        float64 `json:"cacheHitRate"`
	CacheDataAvailable  bool    `json:"cacheDataAvailable"`
}

type DailyUsage struct {
	Date string `json:"date"`
	UsageSummary
}

type ModelUsage struct {
	ModelProfileID string `json:"modelProfileId"`
	ProfileName    string `json:"profileName"`
	ModelID        string `json:"modelId"`
	UsageSummary
}

type UsageDashboard struct {
	Query   UsageQuery   `json:"query"`
	Summary UsageSummary `json:"summary"`
	Daily   []DailyUsage `json:"daily"`
	Models  []ModelUsage `json:"models"`
}

type ConversationRepository interface {
	GetConversation(ctx context.Context, id string) (conversation.Conversation, error)
	UpdateModelSelection(ctx context.Context, conversationID, modelProfileID, modelID string, updatedAt time.Time) error
	UpdateReasoningLevel(ctx context.Context, conversationID string, level modelcap.ReasoningLevel, updatedAt time.Time) error
	UpdateMessageText(ctx context.Context, messageID string, status conversation.MessageStatus, text string, updatedAt time.Time) error
	ListMessages(ctx context.Context, conversationID string, limit int) ([]conversation.Message, error)
}

type EventRepository interface {
	AppendNext(ctx context.Context, event events.Envelope) (events.Envelope, error)
}

type Publisher interface {
	Publish(ctx context.Context, event events.Envelope)
}
