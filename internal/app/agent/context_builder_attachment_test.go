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

func TestContextBuilderImageMetadataDoesNotDescribePixelDelivery(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"attachmentId": "image", "originalName": "figure.jpg", "mimeType": "image/jpeg", "format": "image"})
	if err != nil {
		t.Fatal(err)
	}
	messages := []conversation.Message{{ID: "user", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "这是什么？"}, {Type: "media", Payload: payload}}}}
	request, err := NewContextBuilder(10_000).Build(context.Background(), messages, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := request.Messages[len(request.Messages)-1].Content
	if !strings.Contains(content, "[Attached image]") || strings.Contains(content, "metadata") || strings.Contains(content, "untrusted") || strings.Contains(content, "pixels") || strings.Contains(content, "model content block") {
		t.Fatalf("image context = %q", content)
	}
}
