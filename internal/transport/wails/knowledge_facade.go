package wails

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/knowledge"
)

type KnowledgeFacade struct {
	lifecycle   *LifecycleContext
	attachments *attachment.Service
	service     *knowledge.Service
	embeddings  *embedding.Service
}

func NewKnowledgeFacade(lifecycle *LifecycleContext, attachments *attachment.Service, service *knowledge.Service, embeddings *embedding.Service) *KnowledgeFacade {
	return &KnowledgeFacade{lifecycle: lifecycle, attachments: attachments, service: service, embeddings: embeddings}
}

func (f *KnowledgeFacade) GetEmbeddingConfig() (embedding.Config, error) {
	return f.embeddings.Get(f.lifecycle.Context())
}

func (f *KnowledgeFacade) SaveEmbeddingConfig(projectID string, command embedding.SaveCommand) (embedding.Config, error) {
	value, err := f.embeddings.Save(f.lifecycle.Context(), command)
	if err != nil {
		return embedding.Config{}, err
	}
	// Ensure the selected project starts a shadow rebuild immediately. Other
	// projects migrate lazily when they are opened or searched.
	if err := f.service.RefreshProject(f.lifecycle.Context(), projectID); err != nil {
		return embedding.Config{}, err
	}
	return value, nil
}

func (f *KnowledgeFacade) ListDocuments(projectID string) ([]knowledge.Document, error) {
	return f.service.ListDocuments(f.lifecycle.Context(), projectID)
}

func (f *KnowledgeFacade) ChooseAndImportDocuments(projectID string) (attachment.ImportBatch, error) {
	paths, err := runtime.OpenMultipleFilesDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title: "添加到项目知识库",
		Filters: []runtime.FileFilter{{
			DisplayName: "科研文档 (*.pdf;*.docx;*.xlsx;*.txt;*.md;*.csv;*.tsv)",
			Pattern:     "*.pdf;*.docx;*.xlsx;*.txt;*.md;*.markdown;*.csv;*.tsv",
		}},
	})
	if err != nil || len(paths) == 0 {
		return attachment.ImportBatch{Attachments: []attachment.Attachment{}, Errors: []attachment.ImportError{}}, err
	}
	result, err := f.attachments.ImportPaths(f.lifecycle.Context(), projectID, paths)
	if err != nil {
		return result, err
	}
	for _, value := range result.Attachments {
		if value.Status != attachment.StatusReady {
			continue
		}
		if err := f.service.Enqueue(f.lifecycle.Context(), value); err != nil {
			result.Errors = append(result.Errors, attachment.ImportError{Path: value.OriginalName, Message: err.Error()})
		}
	}
	return result, nil
}

func (f *KnowledgeFacade) RemoveDocument(projectID, documentID string) (knowledge.Document, error) {
	return f.service.RemoveDocument(f.lifecycle.Context(), projectID, documentID)
}
