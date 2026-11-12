package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/model"
)

func TestBaseSystemPromptKeepsProviderIdentityAndHostRules(t *testing.T) {
	r, err := NewContextBuilder(0).Build(context.Background(), nil, "", nil, nil)
	if err != nil || len(r.Messages) == 0 || r.Messages[0].Role != model.RoleSystem {
		t.Fatal("host rules lost system role", err)
	}
	text := strings.ToLower(r.Messages[0].Content)
	for _, required := range []string{"sciaide", "model identity", "provider policies", "rather than authority", "security and approval", "do not invent results"} {
		if !strings.Contains(text, required) {
			t.Fatal("identity-neutral integration lost required host boundary", required)
		}
	}
	if strings.Contains(text, "you are sciaide") {
		t.Fatal("host application name still replaces the model's identity")
	}
}
