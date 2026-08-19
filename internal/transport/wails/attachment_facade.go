package wails

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/attachment"
)

type AttachmentFacade struct {
	lifecycle *LifecycleContext
	service   *attachment.Service
}

func NewAttachmentFacade(lifecycle *LifecycleContext, service *attachment.Service) *AttachmentFacade {
	return &AttachmentFacade{lifecycle: lifecycle, service: service}
}

func (f *AttachmentFacade) ChooseAndImportDocuments(projectID string) (attachment.ImportBatch, error) {
	paths, err := runtime.OpenMultipleFilesDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title: "选择科研文档",
		Filters: []runtime.FileFilter{{
			DisplayName: "科研文档 (*.pdf;*.docx;*.xlsx;*.txt;*.md;*.csv;*.tsv)",
			Pattern:     "*.pdf;*.docx;*.xlsx;*.txt;*.md;*.markdown;*.csv;*.tsv",
		}},
	})
	if err != nil || len(paths) == 0 {
		return attachment.ImportBatch{Attachments: []attachment.Attachment{}, Errors: []attachment.ImportError{}}, err
	}
	return f.service.ImportPaths(f.lifecycle.Context(), projectID, paths)
}

func (f *AttachmentFacade) ListProjectAttachments(projectID string) ([]attachment.Attachment, error) {
	return f.service.List(f.lifecycle.Context(), projectID)
}

func (f *AttachmentFacade) ImportDocumentPaths(projectID string, paths []string) (attachment.ImportBatch, error) {
	return f.service.ImportPaths(f.lifecycle.Context(), projectID, paths)
}
