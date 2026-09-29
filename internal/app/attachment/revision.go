package attachment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/id"
)

func (s *Service) ReadRevisionMaterial(ctx context.Context, projectID, conversationID, attachmentID string) (json.RawMessage, error) {
	if _, err := s.RevisionMaterials(ctx, projectID, conversationID, []string{attachmentID}); err != nil {
		return nil, err
	}
	v, parsed, err := s.Parsed(ctx, projectID, attachmentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"attachmentId": v.ID, "name": v.OriginalName, "sha256": v.SHA256, "truncated": parsed.Truncated, "units": parsed.Units})
}

// RevisionMaterials validates the files explicitly attached to discussion by
// the user. Merely reading them does not promote them to research evidence.
func (s *Service) RevisionMaterials(ctx context.Context, projectID, conversationID string, ids []string) ([]Attachment, error) {
	if len(ids) > 16 {
		return nil, fmt.Errorf("一次返修最多补充 16 份文献资料")
	}
	refs, err := s.ResolveForConversation(ctx, projectID, conversationID, ids)
	if err != nil {
		return nil, err
	}
	if len(refs) != len(ids) {
		return nil, fmt.Errorf("补充资料不能重复")
	}
	values := make([]Attachment, 0, len(refs))
	for _, ref := range refs {
		switch ref.Format {
		case document.FormatPDF, document.FormatDOCX, document.FormatText, document.FormatMarkdown:
		default:
			return nil, fmt.Errorf("返修补证仅支持 PDF、DOCX、TXT、Markdown；不能通过补证替换研究数据")
		}
		v, _, err := s.Parsed(ctx, projectID, ref.AttachmentID)
		if err != nil {
			return nil, err
		}
		if v.ScopeKind == ScopeProjectShared {
			if _, err := s.SelectReferenceMaterials(ctx, projectID, "", []string{v.ID}); err != nil {
				return nil, err
			}
		}
		values = append(values, v)
	}
	return values, nil
}

// Called only after explicit revision confirmation. Reuse immutable bytes and
// preserve the conversation owner; never broaden the original file's scope.
func (s *Service) PromoteRevisionMaterial(ctx context.Context, projectID, conversationID, taskID, attachmentID, sha string) (Attachment, error) {
	if err := s.validateTask(ctx, projectID, taskID); err != nil {
		return Attachment{}, err
	}
	values, err := s.RevisionMaterials(ctx, projectID, conversationID, []string{attachmentID})
	if err != nil {
		return Attachment{}, err
	}
	v := values[0]
	if v.SHA256 != sha {
		return Attachment{}, fmt.Errorf("补充资料已变化，请重新生成返修方案")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, ok := s.repository.(ScopedHashRepository)
	if !ok {
		return Attachment{}, fmt.Errorf("task-scoped material storage unavailable")
	}
	if existing, found, err := repo.FindByHashInScope(ctx, projectID, sha, ScopeTask, taskID); err != nil || found {
		return existing, err
	}
	v.ID, err = id.New()
	if err != nil {
		return Attachment{}, err
	}
	v.ScopeKind, v.ResearchTaskID, v.SourceKind = ScopeTask, taskID, SourceUserImport
	v.CreatedAt, v.UpdatedAt = s.now(), s.now()
	if err := s.repository.Create(ctx, v); err != nil {
		return Attachment{}, err
	}
	return v, nil
}
