package workflow

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/document"
)

type routeDataLoaderFixture struct {
	StarterAttachmentLoader
	items map[string]attachment.Attachment
}

func (f routeDataLoaderFixture) Get(_ context.Context, id string) (attachment.Attachment, error) {
	value, ok := f.items[id]
	if !ok {
		return value, sql.ErrNoRows
	}
	return value, nil
}

func TestRouteDataCandidatesOnlyOfferSentOwnedTables(t *testing.T) {
	items := map[string]attachment.Attachment{}
	message := func(id, owner string, role conversation.Role, internal bool, status conversation.MessageStatus) conversation.Message {
		items[id] = attachment.Attachment{ID: id, ProjectID: "p", ScopeKind: attachment.ScopeConversation, ResearchTaskID: "conversation:" + owner, OriginalName: id + ".csv", Format: document.FormatCSV, Status: attachment.StatusReady, SHA256: strings.Repeat("a", 64), SizeBytes: 12}
		return conversation.Message{ConversationID: owner, Role: role, Status: status, Internal: internal, Parts: []conversation.MessagePart{{Type: "media", Payload: mustJSON(attachment.MessageReference{AttachmentID: id})}}}
	}
	history := []conversation.Message{
		message("first", "old-chat", conversation.RoleUser, false, conversation.MessageComplete),
		message("second", "new-chat", conversation.RoleUser, false, conversation.MessageComplete),
		message("assistant", "old-chat", conversation.RoleAssistant, false, conversation.MessageComplete),
		message("internal", "old-chat", conversation.RoleUser, true, conversation.MessageComplete),
		message("unfinished", "old-chat", conversation.RoleUser, false, conversation.MessageIncomplete),
		message("foreign", "old-chat", conversation.RoleUser, false, conversation.MessageComplete),
		message("wrong-owner", "old-chat", conversation.RoleUser, false, conversation.MessageComplete),
		message("notes", "old-chat", conversation.RoleUser, false, conversation.MessageComplete),
		message("failed", "old-chat", conversation.RoleUser, false, conversation.MessageComplete),
	}
	history = append(history, history[0])
	history = append(history, message("removed", "old-chat", conversation.RoleUser, false, conversation.MessageComplete))
	delete(items, "removed")
	v := items["foreign"]
	v.ProjectID = "other"
	items[v.ID] = v
	v = items["wrong-owner"]
	v.ResearchTaskID = "conversation:other"
	items[v.ID] = v
	v = items["notes"]
	v.OriginalName = "notes.md"
	v.Format = document.FormatText
	items[v.ID] = v
	v = items["failed"]
	v.Status = attachment.StatusFailed
	items[v.ID] = v
	items["not-sent"] = items["first"]
	pending := AdoptResearchRouteResult{StarterRunID: "planner", Workflow: SaveResult{Workflow: Workflow{ProjectID: "p"}, Version: Version{Definition: Definition{Inputs: []Port{{Name: "tables", Type: TypeArray, FileKind: "tabular"}, {Name: "workbook", Type: TypeString, FileKind: "xlsx"}}}}}}
	repo := &openingRecordFixture{details: map[string]RunDetail{"planner": {Run: Run{ID: "planner", ProjectID: "p", ConversationID: "new-chat"}}}, history: history}
	service := StarterService{runtime: &RuntimeService{repository: repo}, attachments: routeDataLoaderFixture{items: items}}
	got, err := service.routeDataCandidates(context.Background(), pending)
	if err != nil || len(got) != 2 || got[0].AttachmentID != "first" || got[0].conversationID != "old-chat" || got[1].AttachmentID != "second" || got[1].conversationID != "new-chat" || len(got[0].PortNames) != 1 || got[0].PortNames[0] != "tables" {
		t.Fatal("wrong table candidates", got, err)
	}
	pending.Workflow.Version.RuntimeInputs = []Port{{Name: "actual_input", Type: TypeString, FileKind: "delimited"}}
	got, err = service.routeDataCandidates(context.Background(), pending)
	if err != nil || len(got) != 2 || got[0].PortNames[0] != "actual_input" {
		t.Fatal("saved runtime input contract not used", got, err)
	}
	pending.Workflow.Version.RuntimeInputs = nil
	pending.Workflow.Version.Definition.Inputs = nil
	got, err = service.routeDataCandidates(context.Background(), pending)
	if err != nil || len(got) != 0 {
		t.Fatal("non-data route got tables", got, err)
	}
}

func TestRouteDataKindMatchingPreservesPortContract(t *testing.T) {
	for _, tc := range []struct {
		kind, name string
		format     document.Format
		want       bool
	}{
		{"tabular", "a.CSV", document.FormatCSV, true}, {"tabular", "a.tsv", document.FormatCSV, true}, {"tabular", "a.xlsx", document.FormatXLSX, true},
		{"delimited", "a.csv", document.FormatCSV, true}, {"delimited", "a.xlsx", document.FormatXLSX, false}, {"xlsx", "a.csv", document.FormatCSV, false},
		{"tabular", "a.csv", document.FormatText, false}, {"python_source", "a.py", document.FormatText, false},
	} {
		if got := routeDataKindMatches(tc.kind, attachment.Attachment{OriginalName: tc.name, Format: tc.format}); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
