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
		Title: "选择科研文档或图片",
		Filters: []runtime.FileFilter{{
			DisplayName: "科研文档与图片 (*.pdf;*.docx;*.xlsx;*.txt;*.md;*.csv;*.tsv;*.jpg;*.png;*.webp)",
			Pattern:     "*.pdf;*.docx;*.xlsx;*.txt;*.md;*.markdown;*.csv;*.tsv;*.jpg;*.jpeg;*.png;*.webp",
		}},
	})
	if err != nil || len(paths) == 0 {
		return attachment.ImportBatch{Attachments: []attachment.Attachment{}, Errors: []attachment.ImportError{}}, err
	}
	return f.service.ImportPaths(f.lifecycle.Context(), projectID, paths)
}

func (f *AttachmentFacade) ListProjectAttachments(projectID string) ([]attachment.Attachment, error) {
	return f.service.ListForProject(f.lifecycle.Context(), projectID)
}

func (f *AttachmentFacade) ListTaskAttachments(projectID, taskID string) ([]attachment.Attachment, error) {
	return f.service.ListForTask(f.lifecycle.Context(), projectID, taskID)
}

func (f *AttachmentFacade) ImportDocumentPaths(projectID string, paths []string) (attachment.ImportBatch, error) {
	return f.service.ImportPaths(f.lifecycle.Context(), projectID, paths)
}

func (f *AttachmentFacade) ImportTaskDocumentPaths(projectID, taskID string, paths []string) (attachment.ImportBatch, error) {
	return f.service.ImportPathsForTask(f.lifecycle.Context(), projectID, paths, taskID)
}

func (f *AttachmentFacade) ChooseAndImportConversationDocuments(projectID, conversationID string) (attachment.ImportBatch, error) {
	paths, err := runtime.OpenMultipleFilesDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title:   "添加到当前对话",
		Filters: []runtime.FileFilter{{DisplayName: "科研文档与图片 (*.pdf;*.docx;*.xlsx;*.txt;*.md;*.csv;*.tsv;*.jpg;*.png;*.webp)", Pattern: "*.pdf;*.docx;*.xlsx;*.txt;*.md;*.markdown;*.csv;*.tsv;*.jpg;*.jpeg;*.png;*.webp"}},
	})
	if err != nil || len(paths) == 0 {
		return attachment.ImportBatch{Attachments: []attachment.Attachment{}, Errors: []attachment.ImportError{}}, err
	}
	return f.service.ImportPathsForConversation(f.lifecycle.Context(), projectID, paths, conversationID)
}

func (f *AttachmentFacade) ImportConversationDocumentPaths(projectID, conversationID string, paths []string) (attachment.ImportBatch, error) {
	return f.service.ImportPathsForConversation(f.lifecycle.Context(), projectID, paths, conversationID)
}
