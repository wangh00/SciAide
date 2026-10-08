package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const ReadKnowledgeName = "builtin.knowledge.read"

type IndexedEvidenceReader interface {
	ReadIndexedEvidence(context.Context, string, string, string, string, int) ([]knowledge.EvidenceChunk, bool, error)
}
type ReadKnowledge struct{ reader IndexedEvidenceReader }

func NewReadKnowledge(reader IndexedEvidenceReader) *ReadKnowledge {
	return &ReadKnowledge{reader: reader}
}
func (*ReadKnowledge) Definition(context.Context) (tool.Definition, error) {
	return documentReadDefinition(ReadKnowledgeName, "Read an indexed selected document or exact page/section in source order, issuing verified citations. Offset counts chunks. Empty results never prove the paper did not discuss a field.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachmentId"],"properties":{"attachmentId":{"type":"string","minLength":1,"maxLength":128},"locator":{"type":"string","maxLength":512},"offset":{"type":"integer","minimum":0}}}`), json.RawMessage(`{"type":"object","required":["attachmentId","offset","nextOffset","hasMore","excerpts"],"properties":{"attachmentId":{"type":"string"},"offset":{"type":"integer"},"nextOffset":{"type":"integer"},"hasMore":{"type":"boolean"},"excerpts":{"type":"array"}}}`)), nil
}
func (t *ReadKnowledge) Invoke(ctx context.Context, inv tool.Invocation) (tool.Result, error) {
	var a struct {
		AttachmentID string `json:"attachmentId"`
		Locator      string `json:"locator"`
		Offset       int    `json:"offset"`
	}
	if err := json.Unmarshal(inv.Arguments, &a); err != nil {
		return tool.Result{}, err
	}
	if t.reader == nil {
		return tool.Result{}, fmt.Errorf("indexed reader not configured")
	}
	values, more, err := t.reader.ReadIndexedEvidence(ctx, inv.ProjectID, inv.ResearchTaskID, a.AttachmentID, a.Locator, a.Offset)
	if err != nil {
		return tool.Result{}, tool.NewUserFacingError("核读所选原文未完成：" + err.Error())
	}
	refs := []tool.CitationRef{}
	var body strings.Builder
	for _, v := range values {
		ref := tool.CitationRef{ID: v.AttachmentID, Kind: citation.KindKnowledgeChunk, ProjectID: inv.ProjectID, IndexVersionID: v.IndexVersionID, DocumentID: v.DocumentID, AttachmentID: v.AttachmentID, ChunkID: v.ChunkID, SourceName: v.SourceName, MIMEType: v.MIMEType, Locator: v.Locator, Title: v.Title, Quote: v.Content, QuoteSHA256: v.ContentSHA256, SourceStart: v.SourceStart, SourceEnd: v.SourceEnd}
		ref.Reference = citation.KnowledgeReference(inv.RunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
		block := fmt.Sprintf("%s %s | %s\n%s\n\n", ref.Reference, v.SourceName, v.Locator, v.Content)
		encoded, _ := json.Marshal(body.String() + block)
		// Reserve room for the resource envelope/continuation. Never let the
		// generic head/tail projection silently omit the middle of a read chunk.
		if len([]rune(string(encoded))) > 7000 {
			if len(refs) == 0 {
				return tool.Result{}, tool.NewUserFacingError("当前索引原文块超出单次完整核读预算；请重新索引材料后再核读，不会把截断内容记为已读完。")
			}
			more = true
			break
		}
		refs = append(refs, ref)
		body.WriteString(block)
	}
	data, _ := json.Marshal(map[string]any{"attachmentId": a.AttachmentID, "locator": a.Locator, "offset": a.Offset, "nextOffset": a.Offset + len(refs), "hasMore": more, "excerpts": refs})
	return tool.Result{Status: tool.ResultSuccess, Text: body.String(), Structured: data, Citations: refs, Truncated: more}, nil
}
