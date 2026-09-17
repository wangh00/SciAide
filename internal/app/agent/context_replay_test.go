package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

// Optional local replay. SQLite is read-only and only the context builder is
// invoked: no application startup, model request, tool execution or migration.
func TestReadOnlyStoredResearchContextBudget(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_CONTEXT_REPLAY_DB"), os.Getenv("SCIAIDE_CONTEXT_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only context replay database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repository := sqlite.NewRunRepository(db)
	run, err := repository.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := sqlite.NewConversationRepository(db).ListMessages(ctx, run.ConversationID, -1)
	if err != nil {
		t.Fatal(err)
	}
	messages = workflowAIRunMessages(messages, runID)
	calls, err := sqlite.NewToolRepository(db).ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := repository.ListProviderTurns(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(turns)
	inputBefore, _ := json.Marshal(messages)
	// Preserve the observed fixed-state footprint. Its text is irrelevant to
	// this budget regression; exact frozen input and protocol come from storage.
	fixed := strings.Repeat("s", 54780-len([]rune(fixedSystemRules))-2)
	build := func(allow bool) (ContextBuildInfo, error) {
		request, info, err := NewContextBuilder(run.ContextBudgetTokens).buildWithResearchState(ctx, messages, run.AssistantMessageID, run.UserMessageID, nil, calls, skill.RunContext{}, "", fixed, "", ContextLimits{EffectiveTokens: run.ContextBudgetTokens, AutoCompactTokens: run.AutoCompactTokenLimit, AllowProtocolRollover: allow}, contextmemory.Checkpoint{}, turns...)
		if err == nil {
			found := false
			for _, m := range request.Messages {
				for _, original := range messages {
					found = found || original.ID == run.UserMessageID && m.Content == conversationText(original)
				}
			}
			if !found || len(request.ProviderTurns) != 0 || info.EstimatedTokens > run.ContextBudgetTokens {
				t.Fatal("replay lost exact stage input or exceeded budget")
			}
		}
		return info, err
	}
	if _, err := build(false); err == nil || !strings.Contains(err.Error(), "required=30447 available=19150") {
		t.Fatal("stored input no longer matches the reported failure", err)
	}
	info, err := build(true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := repository.ListProviderTurns(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(after)
	inputAfter, _ := json.Marshal(messages)
	if string(before) != string(encoded) || string(inputBefore) != string(inputAfter) {
		t.Fatal("read-only context replay changed audit or frozen input")
	}
	t.Logf("stored failure reproduced and recovered: calls=%d nativeTurns=%d estimated=%d limit=%d", len(calls), len(turns), info.EstimatedTokens, run.ContextBudgetTokens)
}
