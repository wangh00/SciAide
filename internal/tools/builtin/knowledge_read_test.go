package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/document"
)

type indexedReaderFixture struct {
	calls  int
	task   string
	offset int
	stress bool
}

func (f *indexedReaderFixture) ReadIndexedEvidence(_ context.Context, _, task, a, locator string, offset int) ([]knowledge.EvidenceChunk, bool, error) {
	f.calls++
	f.task = task
	f.offset = offset
	text := strings.Repeat("原文块中有真实的结果。", 100)
	if f.stress {
		text = strings.Repeat("\"\n", 1600)
	}
	chunk := knowledge.EvidenceChunk{IndexVersionID: "index", DocumentID: "doc", AttachmentID: a, ChunkID: "first", SourceName: "selected.pdf", MIMEType: "application/pdf", Locator: locator, Content: text, ContentSHA256: citation.QuoteSHA256(text), SourceEnd: len([]rune(text))}
	if locator == "" {
		chunk.Locator = "page:2"
	}
	if offset > 0 {
		chunk.ChunkID = "next"
	}
	if f.stress {
		other := chunk
		other.ChunkID = "second"
		return []knowledge.EvidenceChunk{chunk, other}, false, nil
	}
	return []knowledge.EvidenceChunk{chunk}, offset == 0, nil
}

func TestIndexedResourceNeverClaimsSilentlyTruncatedChunkWasFullyRead(t *testing.T) {
	r, repo, scope, req := resourceFixture(t)
	reader := &indexedReaderFixture{stress: true}
	loader := indexedDocumentFixture{documentFixture{attachment: attachment.Attachment{ID: "selected", OriginalName: "selected.pdf", Status: attachment.StatusReady, Format: document.FormatPDF}}}
	reg := r.registry.(tool.MutableRegistry)
	for _, impl := range []tool.Tool{NewListAttachments(loader), NewReadKnowledge(reader)} {
		if err := reg.Register(context.Background(), impl); err != nil {
			t.Fatal(err)
		}
	}
	req.Definitions, _ = reg.Definitions(context.Background())
	req.SelectedOnly, req.SelectedAttachmentIDs = true, []string{"selected"}
	if _, err := r.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	a := resourceActionByLabel(t, repo, "核读可引用原文")
	result, err := invokeResource(t, r, scope, a.ID)
	if err != nil || len(result.Citations) != 1 || !result.Truncated {
		t.Fatal("whole-chunk budget/pagination not preserved", result, err)
	}
	snapshot := tool.BuildModelContextSnapshot("read", "", result)
	if strings.Contains(snapshot, "tool result truncated") {
		t.Fatal("model silently lost source content")
	}
	var model struct {
		Text       string `json:"text"`
		Structured struct {
			Next int  `json:"nextOffset"`
			More bool `json:"hasMore"`
		} `json:"structured"`
	}
	if json.Unmarshal([]byte(snapshot), &model) != nil || model.Structured.Next != 1 || !model.Structured.More || !strings.Contains(model.Text, result.Citations[0].Quote) {
		t.Fatal("read range exceeds actual model-visible content")
	}
}

type indexedDocumentFixture struct{ documentFixture }

func (f indexedDocumentFixture) ListForTask(context.Context, string, string) ([]attachment.Attachment, error) {
	return []attachment.Attachment{f.attachment, {ID: "private", OriginalName: "private-unselected.pdf", Status: attachment.StatusReady, Format: document.FormatPDF}}, nil
}
func (f indexedDocumentFixture) ParsedForTask(context.Context, string, string, string) (attachment.Attachment, document.Parsed, error) {
	return f.attachment, f.parsed, nil
}

func TestSelectedIndexedResourceHasRealCitationsPagesAndNoPrivateActions(t *testing.T) {
	r, repo, scope, req := resourceFixture(t)
	reader := &indexedReaderFixture{}
	loader := indexedDocumentFixture{documentFixture{attachment: attachment.Attachment{ID: "selected", OriginalName: "selected.pdf", Status: attachment.StatusReady, Format: document.FormatPDF}, parsed: document.Parsed{Format: document.FormatPDF, Units: []document.Unit{{Index: 1, Locator: "page:2", Kind: "page", Content: "original"}}}}}
	reg := r.registry.(tool.MutableRegistry)
	for _, impl := range []tool.Tool{NewListAttachments(loader), NewInspectDocument(loader), NewReadKnowledge(reader)} {
		if err := reg.Register(context.Background(), impl); err != nil {
			t.Fatal(err)
		}
	}
	req.Definitions, _ = reg.Definitions(context.Background())
	req.SelectedOnly = true
	req.SelectedAttachmentIDs = []string{"selected"}
	view, err := r.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Context, `"attachmentCount":1`) || strings.Contains(view.Context, "private-unselected.pdf") {
		t.Fatal(view.Context)
	}
	for _, action := range repo.actions {
		if strings.Contains(action.Seed.Label, "private") {
			t.Fatal("unselected document was exposed")
		}
	}
	a := resourceActionByLabel(t, repo, "查看资料结构")
	if _, err = invokeResource(t, r, scope, a.ID); err != nil {
		t.Fatal(err)
	}
	a = resourceActionByLabel(t, repo, "核读可引用原文 · page:2")
	result, err := invokeResource(t, r, scope, a.ID)
	if err != nil || len(result.Citations) != 1 || reader.task != "task" {
		t.Fatal(result, err)
	}
	if result.Citations[0].ID != result.Citations[0].AttachmentID || result.Citations[0].ID == "" {
		t.Fatal("indexed reader omitted attachment snapshot ID")
	}
	if snapshot := tool.BuildModelContextSnapshot("read", "", result); strings.Contains(snapshot, "tool result truncated") || !strings.Contains(snapshot, result.Citations[0].Reference) {
		t.Fatal("read result loses evidence", snapshot)
	}
	bound := citation.Resolve("run", "message", result.Citations[0].Reference, []tool.Call{{ID: "read", RunID: "run", ToolName: "builtin.resource.open", Status: tool.CallCompleted, Result: &result}}, time.Now())
	if len(bound) != 1 {
		t.Fatal("resource read is not a real message citation")
	}
	var page struct {
		Actions []struct {
			ID string `json:"actionId"`
		}
	}
	_ = json.Unmarshal(result.Structured, &page)
	if len(page.Actions) != 1 {
		t.Fatal("missing page continuation")
	}
	if _, err = invokeResource(t, r, scope, page.Actions[0].ID); err != nil || reader.offset != 1 {
		t.Fatal("pagination offset did not advance", err)
	}
	if _, err = invokeResource(t, r, scope, "res_"+strings.Repeat("0", 32)); err == nil || reader.calls != 2 {
		t.Fatal("guessed action invoked source")
	}
}
