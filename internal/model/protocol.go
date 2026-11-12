package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/modelcap"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	// Host-only identity of a tool view rewrapped as user evidence. The prefix
	// must still match the FINAL content before literal page coverage is used.
	EvidenceToolCallID string `json:"-"`
	EvidencePrefix     string `json:"-"`
	// Host-authored instruction text; never set from provider/user content.
	HostToolReferences bool `json:"-"`
	// Host lifecycle/menu state, never inferred from message text.
	ContextTail bool          `json:"-"`
	Role        Role          `json:"role"`
	Content     string        `json:"content,omitempty"`
	Parts       []ContentPart `json:"parts,omitempty"`
	ToolCalls   []ToolCall    `json:"toolCalls,omitempty"`
	ToolCallID  string        `json:"toolCallId,omitempty"`
}

type ContentPart struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	MediaType    string          `json:"mediaType,omitempty"`
	Data         string          `json:"data,omitempty"`
	Name         string          `json:"name,omitempty"`
	AttachmentID string          `json:"attachmentId,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

func ImageDataURL(part ContentPart) (string, error) {
	if part.Type != "input_image" {
		return "", fmt.Errorf("content part is not an input image")
	}
	mediaType := strings.ToLower(strings.TrimSpace(part.MediaType))
	if mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp" {
		return "", fmt.Errorf("unsupported image media type %q", part.MediaType)
	}
	if part.Data == "" || base64.StdEncoding.DecodedLen(len(part.Data)) > 20<<20 {
		return "", fmt.Errorf("image data is empty or exceeds 20 MiB")
	}
	if _, err := base64.StdEncoding.DecodeString(part.Data); err != nil {
		return "", fmt.Errorf("image data is not valid base64")
	}
	return "data:" + mediaType + ";base64," + part.Data, nil
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type ChatRequest struct {
	// Host-only snapshot of admitted completed tool evidence. Not wire data.
	CompletedToolEvidence []CompletedToolEvidence `json:"-"`
	// Display-only names from a preceding exchange, never additional callable tools.
	ToolReferenceNames []string `json:"-"`
	ForcedTool         string   `json:"forcedTool,omitempty"`
	// RequireTool requests one of the offered controls without forcing which one.
	// Used for local repair vs. full replacement, never for exploratory IO.
	RequireTool bool `json:"requireTool,omitempty"`
	// DisableTools reserves a structured submission phase while retaining history schemas.
	DisableTools     bool             `json:"disableTools,omitempty"`
	Messages         []Message        `json:"messages"`
	Tools            []ToolDefinition `json:"tools,omitempty"`
	ProviderTurns    []ProviderTurn   `json:"providerTurns,omitempty"`
	PromptCacheKey   string           `json:"promptCacheKey,omitempty"`
	DisableStreaming bool             `json:"disableStreaming,omitempty"`
	// Host-only inactivity ceiling for recovery; never sent to the provider.
	StreamInactivityTimeout time.Duration `json:"-"`
	// Connection establishment and the first idle lease share this deadline.
	// Effective streaming progress renews only the inactivity lease, so this
	// never becomes a total deadline for a healthy long-running response.
	StreamRecoveryDeadline  time.Time               `json:"-"`
	RequestedReasoningLevel modelcap.ReasoningLevel `json:"requestedReasoningLevel,omitempty"`
	ResolvedReasoningLevel  modelcap.ReasoningLevel `json:"resolvedReasoningLevel,omitempty"`
}

type CompletedToolEvidence struct {
	SourceRunID string          `json:"sourceRunId"`
	CallID      string          `json:"callId"`
	ToolName    string          `json:"toolName"`
	Arguments   json.RawMessage `json:"arguments"`
	Result      string          `json:"result"`
}

// ProviderItem is an immutable, provider-native assistant content item. It is
// deliberately kept separate from the user-visible Message text: signatures,
// encrypted reasoning and redacted blocks are protocol state that must be
// replayed byte-for-byte in meaning, but must not be exposed by chat snapshots.
type ProviderItem struct {
	Ordinal int             `json:"ordinal"`
	ItemID  string          `json:"itemId,omitempty"`
	Type    string          `json:"type"`
	Phase   string          `json:"phase,omitempty"`
	CallID  string          `json:"callId,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// ProviderTurn groups the ordered native items emitted by one model request
// with the normalized tool results that follow that assistant turn. Protocol
// adapters consume only turns matching their own wire protocol.
type ProviderTurn struct {
	TurnIndex   int                  `json:"turnIndex"`
	Protocol    modelcap.APIProtocol `json:"protocol"`
	Items       []ProviderItem       `json:"items"`
	ToolResults []Message            `json:"toolResults,omitempty"`
}

type Capabilities struct {
	Streaming        bool `json:"streaming"`
	ToolCalling      bool `json:"toolCalling"`
	Vision           bool `json:"vision"`
	StructuredOutput bool `json:"structuredOutput"`
	Reasoning        bool `json:"reasoning"`
	MaxContextTokens int  `json:"maxContextTokens"`
}

type EventType string

const (
	EventTextDelta    EventType = "text_delta"
	EventToolCall     EventType = "tool_call"
	EventProviderItem EventType = "provider_item"
	EventUsage        EventType = "usage"
	EventDone         EventType = "done"
)

type Event struct {
	Type         EventType       `json:"type"`
	Text         string          `json:"text,omitempty"`
	ItemID       string          `json:"itemId,omitempty"`
	Ordinal      int             `json:"ordinal,omitempty"`
	Phase        string          `json:"phase,omitempty"`
	FinishReason string          `json:"finishReason,omitempty"`
	ToolCall     *ToolCall       `json:"toolCall,omitempty"`
	ProviderItem *ProviderItem   `json:"providerItem,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Usage struct {
	InputTokens          int  `json:"inputTokens"`
	FreshInputTokens     int  `json:"freshInputTokens"`
	OutputTokens         int  `json:"outputTokens"`
	ReasoningTokens      int  `json:"reasoningTokens"`
	CachedInputTokens    int  `json:"cachedInputTokens"`
	CacheWriteTokens     int  `json:"cacheWriteTokens"`
	CacheDetailsReported bool `json:"cacheDetailsReported"`
}

type Stream interface {
	Recv() (Event, error)
	Close() error
}

// StreamProgressReporter exposes only a host clock, never reasoning or partial
// tool arguments. It renews the inactivity watchdog; a failed attempt's entire
// elapsed time counts as lost work when deciding whether to reconnect again.
type StreamProgressReporter interface {
	LastProgressAt() time.Time
}

func LastStreamProgress(stream Stream) time.Time {
	if reporter, ok := stream.(StreamProgressReporter); ok {
		return reporter.LastProgressAt()
	}
	return time.Time{}
}

// Optional numeric-only telemetry. Incomplete function arguments remain
// private adapter buffers and are never promoted to executable ToolCalls.
type StreamPendingArgumentsReporter interface {
	PendingToolArgumentChars() int
}

func PendingToolArgumentChars(stream Stream) int {
	if reporter, ok := stream.(StreamPendingArgumentsReporter); ok {
		return max(0, reporter.PendingToolArgumentChars())
	}
	return 0
}

// ReasoningResolution reports the effort that was accepted when the streaming
// HTTP request was opened. Resolved is empty when provider-native defaults are
// used because the optional control is unsupported.
type ReasoningResolution struct {
	Requested modelcap.ReasoningLevel `json:"requested"`
	Resolved  modelcap.ReasoningLevel `json:"resolved,omitempty"`
}

type ReasoningResolutionReporter interface {
	ReasoningResolution() ReasoningResolution
}

type resolvedStream struct {
	Stream
	resolution ReasoningResolution
}

func (s *resolvedStream) ReasoningResolution() ReasoningResolution { return s.resolution }

func (s *resolvedStream) LastProgressAt() time.Time { return LastStreamProgress(s.Stream) }

func (s *resolvedStream) PendingToolArgumentChars() int { return PendingToolArgumentChars(s.Stream) }

func WithReasoningResolution(stream Stream, requested, resolved modelcap.ReasoningLevel) Stream {
	return &resolvedStream{Stream: stream, resolution: ReasoningResolution{Requested: requested, Resolved: resolved}}
}

type ChatModel interface {
	Capabilities(ctx context.Context) (Capabilities, error)
	Stream(ctx context.Context, request ChatRequest) (Stream, error)
}

// ResolvedChatModel carries model-level capabilities without making the agent
// layer depend on a concrete gateway or provider adapter.
type ResolvedChatModel struct {
	Model                    ChatModel
	SupportedReasoningLevels []modelcap.ReasoningLevel
	APIProtocol              modelcap.APIProtocol
	ContextBudget            modelcap.ContextBudget
}
