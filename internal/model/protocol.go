package model

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role          `json:"role"`
	Content string        `json:"content,omitempty"`
	Parts   []ContentPart `json:"parts,omitempty"`
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
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
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
	EventTextDelta EventType = "text_delta"
	EventToolCall  EventType = "tool_call"
	EventUsage     EventType = "usage"
	EventDone      EventType = "done"
)

type Event struct {
	Type     EventType       `json:"type"`
	Text     string          `json:"text,omitempty"`
	ToolCall *ToolCall       `json:"toolCall,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

type Stream interface {
	Recv() (Event, error)
	Close() error
}

type ChatModel interface {
	Capabilities(ctx context.Context) (Capabilities, error)
	Stream(ctx context.Context, request ChatRequest) (Stream, error)
}
