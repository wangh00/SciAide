package bootstrap

import (
	"testing"

	"github.com/wangh00/SciAide/internal/modelcap"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestNewChatDefaultsHighAndKeepsExplicitOverride(t *testing.T) {
	a, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p, err := a.ProjectFacade.CreateProject(w.CreateProjectRequest{Name: "Reasoning default QA"})
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []modelcap.ReasoningLevel{"", modelcap.ReasoningMedium, ""} {
		c, err := a.ConversationFacade.CreateConversation(w.CreateConversationRequest{ProjectID: p.ID, Title: "New chat", ReasoningLevel: requested})
		if err != nil {
			t.Fatal(err)
		}
		want := requested
		if want == "" {
			want = modelcap.ReasoningHigh
		}
		stored, err := a.ConversationFacade.GetConversation(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.ReasoningLevel != want || stored.ReasoningLevel != want {
			t.Fatalf("requested %q: created=%q stored=%q want=%q", requested, c.ReasoningLevel, stored.ReasoningLevel, want)
		}
	}
}
