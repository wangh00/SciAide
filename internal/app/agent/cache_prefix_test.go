package agent

import (
	"context"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/model"
	"reflect"
	"strings"
	"testing"
)

func TestResearchDynamicStatePreservesStablePrefixAndBudget(t *testing.T) {
	b := NewContextBuilder(100000)
	messages := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "frozen task"}}}}
	build := func(state string, limit int) (model.ChatRequest, ContextBuildInfo, error) {
		return b.buildWithResearchState(context.Background(), messages, "", "u", nil, nil, skill.RunContext{}, "", "fixed stage rules", state, ContextLimits{EffectiveTokens: limit, AutoCompactTokens: limit}, contextmemory.Checkpoint{})
	}
	a, ai, err := build("dynamic round 1", 100000)
	if err != nil {
		t.Fatal(err)
	}
	z, zi, err := build("dynamic round 2", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ai.StablePrefixMessages, zi.StablePrefixMessages) || a.Messages[0].Content != z.Messages[0].Content {
		t.Fatal("unstable prefix")
	}
	last := z.Messages[len(z.Messages)-1]
	if last.Role != model.RoleSystem || !last.HostToolReferences || last.Content != "dynamic round 2" {
		t.Fatal("dynamic state lost authority or position")
	}
	for _, m := range zi.StablePrefixMessages {
		if strings.Contains(m.Content, "dynamic round") {
			t.Fatal("dynamic state in compaction prefix")
		}
	}
	if _, _, err := build(strings.Repeat("x", 100001), 100000); err == nil {
		t.Fatal("dynamic state bypassed context budget")
	}
}
