package attachment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

// Discussion inspection accepts tables as inert material. Evidence promotion
// remains document-only; data adoption has a separate confirmation contract.
func (s *Service) ReadDiscussionMaterial(ctx context.Context, projectID, conversationID, attachmentID string) (json.RawMessage, error) {
	refs, err := s.ResolveForConversation(ctx, projectID, conversationID, []string{attachmentID})
	if err != nil {
		return nil, err
	}
	if refs[0].Format != document.FormatCSV && refs[0].Format != document.FormatXLSX {
		return s.ReadRevisionMaterial(ctx, projectID, conversationID, attachmentID)
	}
	v, parsed, err := s.Parsed(ctx, projectID, attachmentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"attachmentId": v.ID, "name": v.OriginalName, "sha256": v.SHA256, "format": v.Format,
		"truncated": parsed.Truncated, "units": parsed.Units, "metadata": parsed.Metadata, "adopted": false})
}

// Only returns an owned table handle, not an arbitrary private-storage path.
// The workflow stager verifies the actual bytes against this import record.
func (s *Service) OpenRevisionData(ctx context.Context, projectID, conversationID, attachmentID string) (Attachment, *os.File, error) {
	refs, err := s.ResolveForConversation(ctx, projectID, conversationID, []string{attachmentID})
	if err != nil {
		return Attachment{}, nil, err
	}
	if refs[0].Format != document.FormatCSV && refs[0].Format != document.FormatXLSX {
		return Attachment{}, nil, fmt.Errorf("数据续接当前支持 CSV、TSV、XLSX；文献请使用补证入口")
	}
	return s.openConversationMaterial(ctx, projectID, attachmentID)
}

// Source reuse is an inert reference, not permission to run the uploaded file.
// Return an owned handle so workflow never needs access to private paths.
func (s *Service) OpenRevisionSource(ctx context.Context, projectID, conversationID, attachmentID string) (Attachment, *os.File, error) {
	refs, err := s.ResolveForConversation(ctx, projectID, conversationID, []string{attachmentID})
	if err != nil {
		return Attachment{}, nil, err
	}
	if refs[0].Format != document.FormatText || !strings.EqualFold(filepath.Ext(refs[0].OriginalName), ".py") {
		return Attachment{}, nil, fmt.Errorf("当前源码复用接入单文件 Python；其他语言仍可作为未执行材料阅读")
	}
	return s.openConversationMaterial(ctx, projectID, attachmentID)
}

func (s *Service) openConversationMaterial(ctx context.Context, projectID, attachmentID string) (Attachment, *os.File, error) {
	v, err := s.Get(ctx, attachmentID)
	if err != nil {
		return Attachment{}, nil, err
	}
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return Attachment{}, nil, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return Attachment{}, nil, err
	}
	guard, err := pathguard.Open(project.PrivateDataPath(selected))
	if err != nil {
		return Attachment{}, nil, err
	}
	defer guard.Close()
	file, _, err := guard.OpenFile(v.StorageRelativePath)
	if err != nil {
		return Attachment{}, nil, err
	}
	return v, file, nil
}
