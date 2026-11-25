package workflow

import (
	"context"
	"errors"
	"testing"
)

type discussionSessionFixture struct {
	RuntimeRepository
	entry       ResearchDiscussionEntry
	entryFound  bool
	entryErr    error
	latestFound bool
	latestErr   error
	detail      RunDetail
	detailErr   error
}

func (f discussionSessionFixture) ResearchDiscussionEntryForConversation(context.Context, string) (ResearchDiscussionEntry, bool, error) {
	return f.entry, f.entryFound, f.entryErr
}

func (f discussionSessionFixture) ResearchDiscussionEntryForTask(context.Context, string, string) (ResearchDiscussionEntry, bool, error) {
	return f.entry, f.entryFound, f.entryErr
}

func (f discussionSessionFixture) FindLatestTaskRun(context.Context, string, string) (Run, bool, error) {
	return Run{ID: "planner"}, f.latestFound, f.latestErr
}

func (f discussionSessionFixture) GetRun(context.Context, string, string) (RunDetail, error) {
	return f.detail, f.detailErr
}

func TestResearchTaskSessionSeparatesBindingAndCandidateReads(t *testing.T) {
	entry := ResearchDiscussionEntry{ProjectID: "project", ResearchTaskID: "task", ConversationID: "discussion", CanExplore: true}
	detail := RunDetail{Run: Run{ID: "planner", ProjectID: "project", ResearchTaskID: "task"}}
	failure := errors.New("temporary read failure")
	for _, tc := range []struct {
		name                 string
		configure            func(*discussionSessionFixture)
		missing, unavailable bool
		current              bool
		wantErr              error
	}{
		{name: "ordinary chat", configure: func(f *discussionSessionFixture) { f.entryFound = false }, missing: true},
		{name: "owner read failed", configure: func(f *discussionSessionFixture) { f.entryErr = failure }, missing: true, wantErr: failure},
		{name: "no candidate", configure: func(f *discussionSessionFixture) { f.latestFound = false }},
		{name: "latest lookup failed", configure: func(f *discussionSessionFixture) { f.latestErr = failure }, unavailable: true},
		{name: "candidate detail failed", configure: func(f *discussionSessionFixture) { f.detailErr = failure }, unavailable: true},
		{name: "candidate from another task", configure: func(f *discussionSessionFixture) { f.detail.Run.ResearchTaskID = "instead" }, unavailable: true},
		{name: "candidate readable", configure: func(f *discussionSessionFixture) {}, current: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := discussionSessionFixture{entry: entry, entryFound: true, latestFound: true, detail: detail}
			tc.configure(&repo)
			service := RuntimeService{repository: repo}
			session, err := service.ResearchTaskSessionByConversation(context.Background(), entry.ConversationID)
			if !errors.Is(err, tc.wantErr) || (session == nil) != tc.missing {
				t.Fatalf("unexpected session binding: %+v, %v", session, err)
			}
			if session == nil {
				return
			}
			byTask, err := service.ResearchTaskSession(context.Background(), entry.ProjectID, entry.ResearchTaskID)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range []ResearchTaskSession{*session, byTask} {
				if got.Entry.ProjectID != entry.ProjectID || got.Entry.ResearchTaskID != entry.ResearchTaskID || got.Entry.ConversationID != entry.ConversationID || !got.Entry.CanExplore || got.CurrentRunUnavailable != tc.unavailable || (got.CurrentRun != nil) != tc.current {
					t.Fatalf("observed wrong task session: %+v", got)
				}
			}
		})
	}
}
