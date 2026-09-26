package wails

import (
	"fmt"

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
	return f.service.ListDocumentsForProject(f.lifecycle.Context(), projectID)
}

func (f *KnowledgeFacade) ListTaskDocuments(projectID, taskID string) ([]knowledge.Document, error) {
	return f.service.ListDocumentsForTask(f.lifecycle.Context(), projectID, taskID)
}

func (f *KnowledgeFacade) ListReferenceMaterials(projectID, taskID string) ([]attachment.Attachment, error) {
	values, err := f.ListMaterials(projectID, taskID)
	if err != nil {
		return nil, err
	}
	result := []attachment.Attachment{}
	for _, v := range values {
		if !v.Reusable && !(taskID != "" && v.ScopeKind == attachment.ScopeTask && v.ResearchTaskID == taskID) {
			continue
		}
		a := v.Attachment
		a.OriginalName = v.Title
		result = append(result, a)
	}
	return result, nil
}

func (f *KnowledgeFacade) CollectMaterial(projectID, taskID, id string) (attachment.Material, error) {
	m, err := f.attachments.CollectMaterial(f.lifecycle.Context(), projectID, taskID, id)
	if err != nil {
		return m, err
	}
	if err = f.service.Enqueue(f.lifecycle.Context(), m.Attachment); err != nil {
		return m, fmt.Errorf("资料已收藏，但索引准备失败：%w", err)
	}
	return m, nil
}

func (f *KnowledgeFacade) ListMaterials(projectID, taskID string) ([]attachment.Material, error) {
	values, err := f.attachments.ListMaterials(f.lifecycle.Context(), projectID, taskID)
	if err != nil {
		return nil, err
	}
	docs, err := f.service.ListDocumentsForProject(f.lifecycle.Context(), projectID)
	if err != nil {
		// Original materials remain manageable even if their derived index fails.
		return values, nil
	}
	indexed := make(map[string]knowledge.Document, len(docs))
	for _, d := range docs {
		indexed[d.AttachmentID] = d
	}
	for i := range values {
		if d, ok := indexed[values[i].ID]; ok {
			values[i].IndexStatus = string(d.Status)
			count := d.ChunkCount
			values[i].IndexChunks = &count
			values[i].ParseSummary = d.Diagnostic.Summary
		}
	}
	return values, nil
}

func (f *KnowledgeFacade) ListLibraryMaterials(projectID string) ([]attachment.Material, error) {
	values, err := f.ListMaterials(projectID, "")
	if err != nil {
		return nil, err
	}
	result := []attachment.Material{}
	for _, value := range values {
		if value.Reusable {
			result = append(result, value)
		}
	}
	return result, nil
}
func (f *KnowledgeFacade) ClearTaskMetadata(projectID, taskID string, ids []string) (attachment.MaterialClearResult, error) {
	return f.attachments.ClearTaskMetadata(f.lifecycle.Context(), projectID, taskID, ids)
}
func (f *KnowledgeFacade) ReadMaterial(projectID, taskID, id string, offset int) (attachment.MaterialPage, error) {
	return f.attachments.ReadMaterial(f.lifecycle.Context(), projectID, taskID, id, offset)
}
func (f *KnowledgeFacade) SaveMaterial(projectID, taskID, id, title, notes string) (attachment.Material, error) {
	return f.attachments.SaveMaterial(f.lifecycle.Context(), projectID, taskID, id, title, notes, false)
}
func (f *KnowledgeFacade) ArchiveMaterial(projectID, taskID, id string) error {
	m, err := f.attachments.GetMaterial(f.lifecycle.Context(), projectID, taskID, id)
	if err != nil {
		return err
	}
	_, err = f.attachments.SaveMaterial(f.lifecycle.Context(), projectID, taskID, id, m.Title, m.Notes, true)
	return err
}
func (f *KnowledgeFacade) DownloadMaterial(projectID, taskID, id string) error {
	m, err := f.attachments.GetMaterial(f.lifecycle.Context(), projectID, taskID, id)
	if err != nil {
		return err
	}
	path, err := runtime.SaveFileDialog(f.lifecycle.Context(), runtime.SaveDialogOptions{Title: "保存资料原文件", DefaultFilename: m.OriginalName})
	if err != nil || path == "" {
		return err
	}
	return f.attachments.ExportMaterial(f.lifecycle.Context(), projectID, taskID, id, path)
}

// Candidate uploads are staged in task scope, not indexed or shared until selected.
func (f *KnowledgeFacade) ChooseReferenceMaterials(projectID, taskID string) (attachment.ImportBatch, error) {
	paths, err := runtime.OpenMultipleFilesDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "添加参考资料", Filters: []runtime.FileFilter{{DisplayName: "研究资料", Pattern: "*.pdf;*.docx;*.txt;*.md;*.markdown"}}})
	if err != nil || len(paths) == 0 {
		return attachment.ImportBatch{Attachments: []attachment.Attachment{}, Errors: []attachment.ImportError{}}, err
	}
	var result attachment.ImportBatch
	if taskID != "" {
		result, err = f.attachments.ImportPathsForTask(f.lifecycle.Context(), projectID, paths, taskID)
	} else {
		result, err = f.attachments.ImportPaths(f.lifecycle.Context(), projectID, paths)
	}
	if err == nil {
		accepted := make([]attachment.Attachment, 0, len(result.Attachments))
		for _, v := range result.Attachments {
			restored, e := f.attachments.RestoreMaterial(f.lifecycle.Context(), projectID, taskID, v.ID)
			if e != nil {
				result.Errors = append(result.Errors, attachment.ImportError{Path: v.OriginalName, Message: e.Error()})
				continue
			}
			// Explicit local import can rediscover bytes previously saved by a
			// legacy automatic import. Make that explicit choice reusable too.
			if taskID == "" && !restored.Reusable && v.Status == attachment.StatusReady {
				if _, e = f.attachments.CollectMaterial(f.lifecycle.Context(), projectID, "", v.ID); e != nil {
					result.Errors = append(result.Errors, attachment.ImportError{Path: v.OriginalName, Message: e.Error()})
					continue
				}
			}
			accepted = append(accepted, v)
			if v.Status == attachment.StatusReady {
				if taskID != "" {
					continue
				}
				if e := f.service.Enqueue(f.lifecycle.Context(), v); e != nil {
					result.Errors = append(result.Errors, attachment.ImportError{Path: v.OriginalName, Message: e.Error()})
				}
			}
		}
		result.Attachments = accepted
	}
	return result, err
}

func (f *KnowledgeFacade) ChooseAndImportDocuments(projectID string) (attachment.ImportBatch, error) {
	paths, err := runtime.OpenMultipleFilesDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title: "添加到项目资料库",
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

func (f *KnowledgeFacade) ChooseAndImportTaskDocuments(projectID, taskID string) (attachment.ImportBatch, error) {
	paths, err := runtime.OpenMultipleFilesDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "导入当前科研任务资料", Filters: []runtime.FileFilter{{DisplayName: "科研文档 (*.pdf;*.docx;*.xlsx;*.txt;*.md;*.csv;*.tsv)", Pattern: "*.pdf;*.docx;*.xlsx;*.txt;*.md;*.markdown;*.csv;*.tsv"}}})
	if err != nil || len(paths) == 0 {
		return attachment.ImportBatch{Attachments: []attachment.Attachment{}, Errors: []attachment.ImportError{}}, err
	}
	result, err := f.attachments.ImportPathsForTask(f.lifecycle.Context(), projectID, paths, taskID)
	if err != nil {
		return result, err
	}
	for _, value := range result.Attachments {
		if value.Status == attachment.StatusReady {
			if enqueueErr := f.service.Enqueue(f.lifecycle.Context(), value); enqueueErr != nil {
				result.Errors = append(result.Errors, attachment.ImportError{Path: value.OriginalName, Message: enqueueErr.Error()})
			}
		}
	}
	return result, nil
}

func (f *KnowledgeFacade) RemoveDocument(projectID, documentID string) (knowledge.Document, error) {
	return f.service.RemoveDocument(f.lifecycle.Context(), projectID, documentID)
}

func (f *KnowledgeFacade) RemoveTaskDocument(projectID, taskID, documentID string) (knowledge.Document, error) {
	return f.service.RemoveDocumentForTask(f.lifecycle.Context(), projectID, taskID, documentID)
}

func (f *KnowledgeFacade) CancelDocument(projectID, documentID string) (knowledge.ImportJob, error) {
	return f.service.CancelDocument(f.lifecycle.Context(), projectID, documentID)
}

func (f *KnowledgeFacade) CancelTaskDocument(projectID, taskID, documentID string) (knowledge.ImportJob, error) {
	return f.service.CancelDocumentForTask(f.lifecycle.Context(), projectID, taskID, documentID)
}

func (f *KnowledgeFacade) RetryDocument(projectID, documentID string) (knowledge.ImportJob, error) {
	return f.service.RetryDocument(f.lifecycle.Context(), projectID, documentID)
}

func (f *KnowledgeFacade) RetryTaskDocument(projectID, taskID, documentID string) (knowledge.ImportJob, error) {
	return f.service.RetryDocumentForTask(f.lifecycle.Context(), projectID, taskID, documentID)
}

func (f *KnowledgeFacade) RebuildDocument(projectID, documentID string) (knowledge.ImportJob, error) {
	return f.service.RebuildDocument(f.lifecycle.Context(), projectID, documentID)
}

func (f *KnowledgeFacade) RebuildTaskDocument(projectID, taskID, documentID string) (knowledge.ImportJob, error) {
	return f.service.RebuildDocumentForTask(f.lifecycle.Context(), projectID, taskID, documentID)
}
