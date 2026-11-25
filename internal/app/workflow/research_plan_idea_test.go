package workflow

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func TestResearchPlanningIdeaCopiesOnlyBoundHumanText(t *testing.T) {
	current := conversation.Message{ID: "current", Role: conversation.RoleUser, Status: conversation.MessageComplete,
		Parts: []conversation.MessagePart{{Type: "text", Text: "  原话：两天，不是“一周” 🧪\r\n"}, {Type: "attachment", Text: "not user text"}, {Type: "text", Text: "同等时长；只准备，不采纳。  "}}}
	history := []conversation.Message{
		{ID: "old", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "旧的一周目标"}}},
		current,
		{ID: "later", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "不属于本次冻结来源"}}},
	}
	got, err := researchPlanningIdea("", history, "current")
	want := current.Parts[0].Text + "\n" + current.Parts[2].Text
	if err != nil || got != want {
		t.Fatalf("host text was trimmed, synthesized or read from another message: got %q, err %v", got, err)
	}
}

func TestResearchPlanningIdeaRejectsUnavailableHumanSource(t *testing.T) {
	base := conversation.Message{ID: "current", Role: conversation.RoleUser, Status: conversation.MessageComplete,
		Parts: []conversation.MessagePart{{Type: "text", Text: "当前目标"}}}
	for _, name := range []string{"missing_id", "wrong_id", "internal", "assistant", "streaming", "empty", "only_attachment", "oversized"} {
		t.Run(name, func(t *testing.T) {
			message, humanID := base, "current"
			switch name {
			case "missing_id":
				humanID = ""
			case "wrong_id":
				humanID = "missing"
			case "internal":
				message.Internal = true
			case "assistant":
				message.Role = conversation.RoleAssistant
			case "streaming":
				message.Status = conversation.MessageStreaming
			case "empty":
				message.Parts = []conversation.MessagePart{{Type: "text", Text: " \t\n "}}
			case "only_attachment":
				message.Parts = []conversation.MessagePart{{Type: "attachment", Text: "not a text request"}}
			case "oversized":
				message.Parts = []conversation.MessagePart{{Type: "text", Text: strings.Repeat("新", maxResearchIdeaRunes+1)}}
			}
			old := base
			old.ID = "old"
			if got, err := researchPlanningIdea("", []conversation.Message{old, message}, humanID); err == nil || got != "" {
				t.Fatalf("invalid source accepted or replaced with older text: %q, %v", got, err)
			}
		})
	}
}

func TestResearchPlanningIdeaKeepsExplicitPathAndRuneLimit(t *testing.T) {
	history := []conversation.Message{{ID: "human", Role: conversation.RoleUser, Status: conversation.MessageComplete,
		Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("原", maxResearchIdeaRunes)}}}}
	if got, err := researchPlanningIdea("", history, "human"); err != nil || got != history[0].Parts[0].Text {
		t.Fatal("valid Unicode boundary was shortened or rejected", err)
	}
	if got, err := researchPlanningIdea("  explicit negotiated goal \n", history, "human"); err != nil || got != "explicit negotiated goal" {
		t.Fatal("existing explicit path changed", got, err)
	}
	for _, explicit := range []string{" \n\t ", strings.Repeat("大", maxResearchIdeaRunes+1)} {
		if got, err := researchPlanningIdea(explicit, history, "human"); err == nil || got != "" {
			t.Fatal("invalid explicit input silently fell back to human text")
		}
	}
}
