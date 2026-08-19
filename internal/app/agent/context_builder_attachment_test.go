package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func TestContextBuilderIncludesAttachmentReferenceAsUntrustedData(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"attachmentId": "paper", "originalName": "paper.pdf", "format": "pdf", "unitCount": 3})
	if err != nil {
		t.Fatal(err)
	}
	messages := []conversation.Message{{ID: "user", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "media", Payload: payload}}}}
	request, err := NewContextBuilder(10_000).Build(context.Background(), messages, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, "paper.pdf") || !strings.Contains(request.Messages[1].Content, "untrusted research data") {
		t.Fatalf("request messages = %#v", request.Messages)
	}
}
