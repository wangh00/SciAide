package sqlite

import (
	"context"
	"testing"
)

func TestUIProjectionKeepsStageHumanAnswersAndFullAuditPrompt(t *testing.T) {
	db, _ := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var cid string
	if err := db.QueryRow(`SELECT conversation_id FROM runs WHERE id='chat'`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	var prompt string
	if err := db.QueryRow(`SELECT user_message_id FROM runs WHERE id='chat'`).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO message_parts(id,message_id,ordinal,part_type,text_content,created_at) VALUES ('large-prompt','` + prompt + `',0,'text','complete frozen prompt','2026-10-09T00:00:00Z')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) SELECT 'human-answer',conversation_id,id,'user','complete','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z' FROM runs WHERE id='chat'`,
		`INSERT INTO message_parts(id,message_id,ordinal,part_type,text_content,created_at) VALUES ('answer-part','human-answer',0,'text','keep this human answer','2026-10-09T00:00:00Z')`,
	} {
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	r := NewConversationRepository(db)
	full, err := r.ListMessagesForRun(ctx, cid, "chat")
	if err != nil || len(full) != 3 {
		t.Fatal("audit history changed", len(full), err)
	}
	ui, err := r.ListVisibleMessages(ctx, cid, 200)
	if err != nil || len(ui) != 2 {
		t.Fatal("prompt not excluded", len(ui), err)
	}
	for _, m := range ui {
		if m.ID == prompt {
			t.Fatal("hidden prompt loaded")
		}
		if m.ID == "human-answer" && (m.Internal || m.Parts[0].Text != "keep this human answer") {
			t.Fatal("human answer hidden/changed", m)
		}
	}
	for _, m := range full {
		if m.ID == prompt && (len(m.Parts) != 1 || m.Parts[0].Text != "complete frozen prompt") {
			t.Fatal("frozen prompt modified")
		}
	}
}

func TestUIRevisionObservesLocalAndExternalWritesAndRejectsWrongScope(t *testing.T) {
	db, path := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := NewRunRepository(db)
	a, err := r.UIRevision(ctx, "chat", "")
	if err != nil || a == "" {
		t.Fatal(a, err)
	}
	b, _ := r.UIRevision(ctx, "chat", "")
	if a != b {
		t.Fatal("read changed revision")
	}
	if _, err := db.Exec(`UPDATE messages SET updated_at='2026-10-13' WHERE run_id='chat'`); err != nil {
		t.Fatal(err)
	}
	b, _ = r.UIRevision(ctx, "chat", "")
	if a == b {
		t.Fatal("local write missed")
	}
	other, err := OpenExisting(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(`UPDATE runs SET input_tokens=123 WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	c, _ := r.UIRevision(ctx, "chat", "")
	if c == b {
		t.Fatal("external write missed")
	}
	w := NewWorkflowRuntimeRepository(db)
	if _, err := w.UIRevision(ctx, "wrong-project", "wr"); err == nil {
		t.Fatal("scope bypassed")
	}
	var pid string
	_ = db.QueryRow(`SELECT project_id FROM workflow_runs WHERE id='wr'`).Scan(&pid)
	if _, err := db.Exec(`UPDATE workflow_runs SET status='running' WHERE id='wr'`); err != nil {
		t.Fatal(err)
	}
	if v, err := w.UIRevision(ctx, pid, "wr"); err != nil || v != "" {
		t.Fatal("live tails could be cached", v, err)
	}
}
