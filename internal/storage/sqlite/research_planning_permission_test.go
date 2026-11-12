package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestResearchPlanningChatInheritsFrozenRunPermissionNotLiveConversation(t *testing.T) {
	for _, frozen := range []conversation.PermissionMode{conversation.PermissionPlan, conversation.PermissionFullAccess} {
		t.Run(string(frozen), func(t *testing.T) {
			ctx := context.Background()
			store, run := createToolFixture(t)
			defer store.Close()
			live := conversation.PermissionFullAccess
			if frozen == live {
				live = conversation.PermissionPlan
			}
			if _, e := store.DB().Exec(`UPDATE runs SET status='running',permission_mode=? WHERE id=?`, frozen, run.ID); e != nil {
				t.Fatal(e)
			}
			if _, e := store.DB().Exec(`UPDATE conversations SET permission_mode=? WHERE id=?`, live, run.ConversationID); e != nil {
				t.Fatal(e)
			}
			now := time.Now().UTC()
			call := tool.Call{ID: "plan-prepare", RunID: run.ID, ProviderCallID: "provider-prepare", ToolName: workflow.ResearchPlanPrepareTool, ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: tool.CallRunning, Risk: tool.RiskLow, Idempotent: true, CreatedAt: now, UpdatedAt: now}
			if e := NewToolRepository(store.DB()).Create(ctx, call); e != nil {
				t.Fatal(e)
			}
			value, humanID, e := NewWorkflowRuntimeRepository(store.DB()).ResearchPlanningChat(ctx, run.ID, call.ID)
			if e != nil || humanID != run.UserMessageID || value.PermissionMode != frozen {
				t.Fatal("planning authority drifted with mutable conversation", e, value.PermissionMode, humanID)
			}
		})
	}
}
