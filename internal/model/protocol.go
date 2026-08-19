package model

import (
	"context"
	"encoding/json"

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
	Role       Role          `json:"role"`
	Content    string        `json:"content,omitempty"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"toolCalls,omitempty"`
	ToolCallID string        `json:"toolCallId,omitempty"`
}

type ContentPart struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type ChatRequest struct {
	Messages                []Message               `json:"messages"`
	Tools                   []ToolDefinition        `json:"tools,omitempty"`
	ProviderTurns           []ProviderTurn          `json:"providerTurns,omitempty"`
	RequestedReasoningLevel modelcap.ReasoningLevel `json:"requestedReasoningLevel,omitempty"`
	ResolvedReasoningLevel  modelcap.ReasoningLevel `json:"resolvedReasoningLevel,omitempty"`
}

// ProviderItem is an immutable, provider-native assistant content item. It is
// deliberately kept separate from the user-visible Message text: signatures,
// encrypted reasoning and redacted blocks are protocol state that must be
// replayed byte-for-byte in meaning, but must not be exposed by chat snapshots.
type ProviderItem struct {
	Ordinal int             `json:"ordinal"`
	Type    string          `json:"type"`
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
