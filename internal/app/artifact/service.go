package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	contenttype "github.com/wailsapp/mimetype"
	appcitation "github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

const (
	maxArtifactBytes = int64(1 << 30)
	maxPreviewBytes  = int64(256 << 10)
	maxImagePreview  = int64(20 << 20)
)

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
}

type RecoveryResult struct {
	TemporaryFilesRemoved  int `json:"temporaryFilesRemoved"`
	OrphanObjectsRemoved   int `json:"orphanObjectsRemoved"`
	ToolArtifactsRecovered int `json:"toolArtifactsRecovered"`
	ToolArtifactsFailed    int `json:"toolArtifactsFailed"`
}

type Service struct {
	repository Repository
	projects   ProjectLoader
	now        func() time.Time
	newID      func() (string, error)
	mu         sync.Mutex
}

func NewService(repository Repository, projects ProjectLoader) *Service {
	return &Service{repository: repository, projects: projects, now: func() time.Time { return time.Now().UTC() }, newID: id.New}
}

func (s *Service) SaveAssistantAnswer(ctx context.Context, projectID, messageID, name, artifactID string) (SaveResult, error) {
	projectID, messageID = strings.TrimSpace(projectID), strings.TrimSpace(messageID)
	if projectID == "" || messageID == "" {
		return SaveResult{}, fmt.Errorf("project and assistant message are required")
	}
	source, err := s.repository.AssistantSource(ctx, projectID, messageID)
	if err != nil {
		return SaveResult{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultAssistantName(source.ConversationTitle)
	}
	fileName := ensureExtension(safeName(name), ".md")
	sourceKey := "assistant:" + source.MessageID
	if target := strings.TrimSpace(artifactID); target != "" {
		sourceKey += ":artifact:" + target
	}
	provenance := Provenance{
		SchemaVersion: 1, ProjectID: projectID, SourceKind: SourceAssistantMessage,
		ConversationID: source.ConversationID, ConversationTitle: source.ConversationTitle,
		RunID: source.RunID, MessageID: source.MessageID,
		ModelProfileID: source.ModelProfileID, ModelProfileName: source.ModelProfileName,
		ModelID: source.ModelID, APIProtocol: source.APIProtocol, Skills: nonNilSkills(source.Skills),
	}
	lineage := []Lineage{
		{RelationKind: "run", SourceIDSnapshot: source.RunID, SourceRunID: source.RunID, Label: "生成回答"},
		{RelationKind: "message", SourceIDSnapshot: source.MessageID, SourceMessageID: source.MessageID, Label: "助手最终回答"},
	}
	return s.saveBytes(ctx, saveCommand{
		ProjectID: projectID, ArtifactID: artifactID, Name: strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		Kind: KindDocument, FileName: fileName, MIMEType: "text/markdown; charset=utf-8",
		SourceKind: SourceAssistantMessage, SourceKey: sourceKey,
		Provenance: provenance, Lineage: lineage, Citations: source.Citations,
	}, []byte(source.Text))
}

// PublishWorkflowReport is the narrow trusted entry used by SciAide's fixed
// research report tool. Citation identity must be verified before this method
// is called; this layer freezes provenance and creates deterministic exports.
func (s *Service) PublishWorkflowReport(ctx context.Context, cmd WorkflowReportCommand) (WorkflowReportResult, error) {
	cmd.ProjectID, cmd.WorkflowRunID, cmd.ToolCallID = strings.TrimSpace(cmd.ProjectID), strings.TrimSpace(cmd.WorkflowRunID), strings.TrimSpace(cmd.ToolCallID)
	cmd.ToolName, cmd.ToolVersion, cmd.OperationKey = strings.TrimSpace(cmd.ToolName), strings.TrimSpace(cmd.ToolVersion), strings.TrimSpace(cmd.OperationKey)
	cmd.Name, cmd.Markdown = strings.TrimSpace(cmd.Name), strings.TrimSpace(cmd.Markdown)
	if cmd.ProjectID == "" || cmd.WorkflowRunID == "" || cmd.ToolCallID == "" || cmd.ToolName == "" || cmd.ToolVersion == "" || cmd.Name == "" || cmd.Markdown == "" {
		return WorkflowReportResult{}, fmt.Errorf("Workflow report source, name, and Markdown are required")
	}
	if len([]rune(cmd.Markdown)) > 2_000_000 || len(cmd.Citations) > 256 || len(cmd.SourceToolCallIDs) > 32 || len(cmd.SourceWorkspaceFiles) > 64 {
		return WorkflowReportResult{}, fmt.Errorf("Workflow report exceeds size limits")
	}
	lineage := []Lineage{
		{RelationKind: "workflow_run", SourceIDSnapshot: cmd.WorkflowRunID, SourceWorkflowRunID: cmd.WorkflowRunID, Label: "科研 Workflow Run"},
		{RelationKind: "tool_call", SourceIDSnapshot: cmd.ToolCallID, SourceToolCallID: cmd.ToolCallID, Label: cmd.ToolName},
	}
	seenCalls := map[string]struct{}{cmd.ToolCallID: {}}
	for _, callID := range cmd.SourceToolCallIDs {
		callID = strings.TrimSpace(callID)
		if callID == "" {
			continue
		}
		if _, exists := seenCalls[callID]; exists {
			continue
		}
		seenCalls[callID] = struct{}{}
		lineage = append(lineage, Lineage{RelationKind: "tool_call", SourceIDSnapshot: callID, SourceToolCallID: callID, Label: "上游科研步骤"})
	}
	seenFiles := map[string]struct{}{}
	for _, path := range cmd.SourceWorkspaceFiles {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		if _, exists := seenFiles[path]; exists {
			continue
		}
		seenFiles[path] = struct{}{}
		lineage = append(lineage, Lineage{RelationKind: "workspace_file", SourceIDSnapshot: path, Label: filepath.Base(path)})
	}
	provenance := Provenance{
		SchemaVersion: 1, ProjectID: cmd.ProjectID, SourceKind: SourceTool,
		WorkflowRunID: cmd.WorkflowRunID, ToolCallID: cmd.ToolCallID, ToolName: cmd.ToolName, ToolVersion: cmd.ToolVersion,
		Skills: []SkillSnapshot{}, Extra: map[string]string{"workflowReport": "true"},
	}
	fileName := ensureExtension(safeName(cmd.Name), ".md")
	sourceKey := "workflow-report:" + cmd.ToolCallID
	if cmd.OperationKey != "" {
		if len(cmd.OperationKey) > 256 || strings.IndexByte(cmd.OperationKey, 0) >= 0 {
			return WorkflowReportResult{}, fmt.Errorf("Workflow report operation key is invalid")
		}
		sourceKey = "workflow-report-operation:" + cmd.OperationKey
	}
	saved, err := s.saveBytes(ctx, saveCommand{
		ProjectID: cmd.ProjectID, Name: cmd.Name, Kind: KindDocument, FileName: fileName, MIMEType: "text/markdown; charset=utf-8",
		SourceKind: SourceTool, SourceKey: sourceKey, Provenance: provenance, Lineage: lineage, Citations: cmd.Citations,
	}, []byte(cmd.Markdown+"\n"))
	if err != nil {
		return WorkflowReportResult{}, err
	}
	docx, err := s.CreateExport(ctx, ExportCommand{ProjectID: cmd.ProjectID, VersionID: saved.Version.ID, Format: ExportDOCX, CitationStyle: CitationGB7714})
	if err != nil {
		return WorkflowReportResult{}, fmt.Errorf("create Workflow report DOCX: %w", err)
	}
	pdf, err := s.CreateExport(ctx, ExportCommand{ProjectID: cmd.ProjectID, VersionID: saved.Version.ID, Format: ExportPDF, CitationStyle: CitationGB7714})
	if err != nil {
		return WorkflowReportResult{}, fmt.Errorf("create Workflow report PDF: %w", err)
	}
	return WorkflowReportResult{Artifact: saved.Artifact, Version: saved.Version, DOCX: docx.Export, PDF: pdf.Export, Created: saved.Created}, nil
}

func (s *Service) RegisterWorkspaceFile(ctx context.Context, cmd RegisterWorkspaceCommand) (SaveResult, error) {
	cmd.ProjectID, cmd.Path, cmd.ArtifactID, cmd.Name = strings.TrimSpace(cmd.ProjectID), strings.TrimSpace(cmd.Path), strings.TrimSpace(cmd.ArtifactID), strings.TrimSpace(cmd.Name)
	if cmd.ProjectID == "" || cmd.Path == "" {
		return SaveResult{}, fmt.Errorf("project and Workspace file are required")
	}
	selected, err := s.projects.Get(ctx, cmd.ProjectID)
	if err != nil {
		return SaveResult{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return SaveResult{}, fmt.Errorf("project Artifact storage is unavailable: %w", err)
	}
	relative, err := workspaceRelativePath(selected.WorkspacePath, cmd.Path)
	if err != nil {
		return SaveResult{}, err
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return SaveResult{}, err
	}
	defer guard.Close()
	input, clean, err := guard.OpenFile(relative)
	if err != nil {
		return SaveResult{}, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxArtifactBytes {
		return SaveResult{}, fmt.Errorf("Artifact source must be a regular file no larger than 1 GiB")
	}
	name := cmd.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean))
	}
	mimeType, kind, err := inspectSource(input, filepath.Ext(clean))
	if err != nil {
		return SaveResult{}, err
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return SaveResult{}, fmt.Errorf("rewind Artifact source: %w", err)
	}
	modified := info.ModTime().UTC()
	provenance := Provenance{
		SchemaVersion: 1, ProjectID: cmd.ProjectID, SourceKind: SourceWorkspaceFile,
		WorkspaceRelativePath: filepath.ToSlash(clean), WorkspaceModifiedAt: &modified,
		Skills: []SkillSnapshot{},
	}
	lineage := []Lineage{{RelationKind: "workspace_file", SourceIDSnapshot: filepath.ToSlash(clean), Label: filepath.Base(clean)}}
	return s.saveReader(ctx, saveCommand{
		ProjectID: cmd.ProjectID, ArtifactID: cmd.ArtifactID, Name: name, Kind: kind,
		FileName: safeName(filepath.Base(clean)), MIMEType: mimeType, SourceKind: SourceWorkspaceFile,
		Provenance: provenance, Lineage: lineage,
	}, input, info.Size(), func(digest string) string {
		key := "workspace:" + cmd.ProjectID + ":"
		if target := strings.TrimSpace(cmd.ArtifactID); target != "" {
			key += "artifact:" + target + ":"
		}
		return key + filepath.ToSlash(clean) + ":" + digest
	})
}

// RegisterToolArtifacts turns only explicit Workspace paths into Artifacts.
// Existing ArtifactRef IDs from document/search tools remain ordinary source
// references and are deliberately ignored.
func (s *Service) RegisterToolArtifacts(ctx context.Context, callID string) ([]SaveResult, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, fmt.Errorf("tool call id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registerToolArtifactsLocked(ctx, callID)
}

// BindToolArtifacts freezes the exact Workspace bytes named by a successful
// ToolResult before that result is persisted. Registration may be retried, but
// it may never silently adopt newer bytes from the same path.
func (s *Service) BindToolArtifacts(ctx context.Context, projectID string, call tool.Call, references []tool.ArtifactRef) ([]tool.ArtifactRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	selected, err := s.projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return nil, err
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	root, err := os.OpenRoot(project.PrivateDataPath(selected))
	if err != nil {
		return nil, fmt.Errorf("open project data root: %w", err)
	}
	defer root.Close()
	bound := append([]tool.ArtifactRef(nil), references...)
	for index := range bound {
		reference := &bound[index]
		if strings.TrimSpace(reference.WorkspacePath) == "" {
			reference.SizeBytes, reference.SHA256 = 0, ""
			continue
		}
		expectedSHA256 := strings.TrimSpace(reference.SHA256)
		if expectedSHA256 != "" && (len(expectedSHA256) != 64 || strings.ToLower(expectedSHA256) != expectedSHA256) {
			return nil, fmt.Errorf("declared Workspace path contains an invalid SHA256")
		}
		relative, err := workspaceRelativePath(selected.WorkspacePath, reference.WorkspacePath)
		if err != nil {
			return nil, err
		}
		if !workspacePermissionCovers(selected.WorkspacePath, call.Permissions, relative) {
			return nil, fmt.Errorf("declared Workspace path is outside the tool permission scope")
		}
		input, clean, err := guard.OpenFile(relative)
		if err != nil {
			return nil, err
		}
		info, statErr := input.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxArtifactBytes {
			input.Close()
			return nil, fmt.Errorf("declared Workspace path is not a supported regular file")
		}
		mimeType, _, inspectErr := inspectSource(input, filepath.Ext(clean))
		if inspectErr != nil {
			input.Close()
			return nil, inspectErr
		}
		if value := strings.TrimSpace(reference.MIMEType); value != "" && !sameMIME(value, mimeType) {
			input.Close()
			return nil, fmt.Errorf("declared MIME type does not match the file")
		}
		if _, err := input.Seek(0, io.SeekStart); err != nil {
			input.Close()
			return nil, err
		}
		_, digest, written, freezeErr := s.publishContentObject(root, input, info.Size(), expectedSHA256)
		closeErr := input.Close()
		if freezeErr != nil || closeErr != nil {
			if errors.Is(freezeErr, errArtifactSHA256Mismatch) {
				return nil, fmt.Errorf("declared Workspace path no longer matches the tool-computed SHA256")
			}
			return nil, fmt.Errorf("declared Workspace path changed while its byte identity was being frozen")
		}
		reference.WorkspacePath = filepath.ToSlash(clean)
		reference.SizeBytes = written
		reference.SHA256 = digest
		if strings.TrimSpace(reference.MIMEType) == "" {
			reference.MIMEType = mimeType
		}
	}
	return bound, nil
}

func (s *Service) registerToolArtifactsLocked(ctx context.Context, callID string) ([]SaveResult, error) {
	source, err := s.repository.ToolSource(ctx, callID)
	if err != nil {
		return nil, err
	}
	if !canReadWorkspace(source.Permissions) {
		return nil, fmt.Errorf("tool did not declare Workspace read or write permission for Artifact registration")
	}
	results := make([]SaveResult, 0)
	errorsByArtifact := make([]error, 0)
	for ordinal, reference := range source.Artifacts {
		if strings.TrimSpace(reference.WorkspacePath) == "" {
			continue
		}
		result, err := s.registerToolFileLocked(ctx, source, reference, ordinal)
		if err != nil {
			errorsByArtifact = append(errorsByArtifact, fmt.Errorf("register Artifact %d from %s: %w", ordinal+1, source.ToolName, err))
			continue
		}
		results = append(results, result)
	}
	return results, errors.Join(errorsByArtifact...)
}

// RegisterToolArtifactsForExecutor adapts the detailed result to the Tool
// Executor's failure-aware observer contract.
func (s *Service) RegisterToolArtifactsForExecutor(ctx context.Context, callID string) error {
	_, err := s.RegisterToolArtifacts(ctx, callID)
	return err
}

func (s *Service) registerToolFileLocked(ctx context.Context, source ToolSource, reference tool.ArtifactRef, ordinal int) (SaveResult, error) {
	selected, err := s.projects.Get(ctx, source.ProjectID)
	if err != nil {
		return SaveResult{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return SaveResult{}, err
	}
	relative, err := workspaceRelativePath(selected.WorkspacePath, reference.WorkspacePath)
	if err != nil {
		return SaveResult{}, err
	}
	if !workspacePermissionCovers(selected.WorkspacePath, source.Permissions, relative) {
		return SaveResult{}, fmt.Errorf("declared Workspace path is outside the tool permission scope")
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return SaveResult{}, err
	}
	defer guard.Close()
	clean, err := guard.Relative(relative)
	if err != nil {
		return SaveResult{}, err
	}
	var input *os.File
	var privateRoot *os.Root
	if reference.SHA256 != "" {
		if len(reference.SHA256) != 64 || strings.ToLower(reference.SHA256) != reference.SHA256 || reference.SizeBytes < 0 {
			return SaveResult{}, fmt.Errorf("declared Workspace path no longer matches the ToolResult byte identity")
		}
		privateRoot, err = os.OpenRoot(project.PrivateDataPath(selected))
		if err != nil {
			return SaveResult{}, fmt.Errorf("open project data root: %w", err)
		}
		defer privateRoot.Close()
		objectRelative := filepath.Join("artifacts", "objects", reference.SHA256[:2], reference.SHA256)
		input, err = privateRoot.Open(objectRelative)
		if err != nil {
			return SaveResult{}, fmt.Errorf("open frozen ToolResult object: %w", err)
		}
	} else {
		input, clean, err = guard.OpenFile(relative)
		if err != nil {
			return SaveResult{}, err
		}
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxArtifactBytes {
		return SaveResult{}, fmt.Errorf("declared Workspace path is not a supported regular file")
	}
	mimeType, kind, err := inspectSource(input, filepath.Ext(clean))
	if err != nil {
		return SaveResult{}, err
	}
	if value := strings.TrimSpace(reference.MIMEType); value != "" && !sameMIME(value, mimeType) {
		return SaveResult{}, fmt.Errorf("declared MIME type does not match the file")
	}
	if reference.SHA256 != "" && reference.SizeBytes != info.Size() {
		return SaveResult{}, fmt.Errorf("declared Workspace path no longer matches the ToolResult byte identity")
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return SaveResult{}, err
	}
	name := strings.TrimSpace(reference.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean))
	}
	provenance := Provenance{
		SchemaVersion: 1, ProjectID: source.ProjectID, SourceKind: SourceTool,
		ConversationID: source.ConversationID, ConversationTitle: source.ConversationTitle,
		RunID: source.RunID, ToolCallID: source.CallID, ToolName: source.ToolName, ToolVersion: source.ToolVersion,
		ModelProfileID: source.ModelProfileID, ModelProfileName: source.ModelProfileName,
		ModelID: source.ModelID, APIProtocol: source.APIProtocol,
		WorkspaceRelativePath: filepath.ToSlash(clean), Skills: nonNilSkills(source.Skills),
	}
	lineage := []Lineage{
		{RelationKind: "run", SourceIDSnapshot: source.RunID, SourceRunID: source.RunID, Label: "工具所在 Run"},
		{RelationKind: "tool_call", SourceIDSnapshot: source.CallID, SourceToolCallID: source.CallID, Label: source.ToolName},
		{RelationKind: "workspace_file", SourceIDSnapshot: filepath.ToSlash(clean), Label: filepath.Base(clean)},
	}
	if tool.NormalizeSubjectKind(source.SubjectKind) == tool.SubjectWorkflowRun {
		provenance.RunID = ""
		provenance.WorkflowRunID = source.RunID
		lineage[0].RelationKind = "workflow_run"
		lineage[0].SourceRunID = ""
		lineage[0].SourceWorkflowRunID = source.RunID
		lineage[0].Label = "工具所在 Workflow Run"
	}
	citations := citationsFromTool(source.Citations, source)
	return s.saveReaderLocked(ctx, saveCommand{
		ProjectID: source.ProjectID, Name: name, Kind: kind, FileName: safeName(filepath.Base(clean)),
		MIMEType: mimeType, SourceKind: SourceTool, SourceKey: fmt.Sprintf("tool:%s:%d", source.CallID, ordinal),
		Provenance: provenance, Lineage: lineage, Citations: citations,
		ExpectedSHA256: reference.SHA256,
	}, input, info.Size(), nil)
}

func (s *Service) List(ctx context.Context, projectID string, includeTrashed bool) ([]Artifact, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	return s.repository.List(ctx, projectID, includeTrashed)
}

func (s *Service) Get(ctx context.Context, projectID, artifactID string) (Detail, error) {
	return s.repository.Get(ctx, strings.TrimSpace(projectID), strings.TrimSpace(artifactID))
}

func (s *Service) Rename(ctx context.Context, projectID, artifactID, name string) (Artifact, error) {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return Artifact{}, err
	}
	return s.repository.Rename(ctx, strings.TrimSpace(projectID), strings.TrimSpace(artifactID), name, s.now())
}

func (s *Service) Trash(ctx context.Context, projectID, artifactID string) (Artifact, error) {
	return s.repository.SetStatus(ctx, strings.TrimSpace(projectID), strings.TrimSpace(artifactID), StatusTrashed, s.now())
}

func (s *Service) Restore(ctx context.Context, projectID, artifactID string) (Artifact, error) {
	return s.repository.SetStatus(ctx, strings.TrimSpace(projectID), strings.TrimSpace(artifactID), StatusActive, s.now())
}

func (s *Service) Preview(ctx context.Context, projectID, versionID string) (Preview, error) {
	version, blob, selected, file, err := s.openVersion(ctx, projectID, versionID)
	if err != nil {
		return Preview{}, err
	}
	if structuredPreviewSupported(version) {
		snapshot, cleanup, snapshotErr := s.verifiedSnapshot(ctx, selected, version, file, "preview-source-")
		if snapshotErr != nil {
			return Preview{}, snapshotErr
		}
		defer cleanup()
		structured, truncated, previewErr := buildStructuredPreview(ctx, snapshot, version)
		if previewErr != nil {
			return Preview{}, previewErr
		}
		return Preview{VersionID: version.ID, Kind: "document", MIMEType: version.MIMEType, Truncated: truncated, Document: &structured}, nil
	}
	defer file.Close()
	limit := maxPreviewBytes
	if strings.HasPrefix(version.MIMEType, "image/") {
		limit = maxImagePreview
	}
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return Preview{}, fmt.Errorf("read Artifact preview: %w", err)
	}
	result := Preview{VersionID: version.ID, MIMEType: version.MIMEType, Truncated: int64(len(contents)) > limit}
	if result.Truncated {
		contents = contents[:limit]
	}
	switch {
	case isTextMIME(version.MIMEType):
		if result.Truncated {
			contents = trimIncompleteUTF8(contents)
		}
		if !utf8.Valid(contents) {
			return Preview{}, fmt.Errorf("Artifact preview is not valid UTF-8")
		}
		result.Kind, result.Text = "text", string(contents)
	case isInlineRasterMIME(version.MIMEType):
		if result.Truncated {
			return Preview{}, fmt.Errorf("image is too large for inline preview")
		}
		result.Kind, result.Data = "image", base64.StdEncoding.EncodeToString(contents)
	default:
		result.Kind = "binary"
	}
	if int64(len(contents)) > blob.SizeBytes {
		return Preview{}, fmt.Errorf("Artifact preview exceeded recorded size")
	}
	return result, nil
}

func (s *Service) CheckIntegrity(ctx context.Context, projectID, versionID string) (IntegrityResult, error) {
	version, _, err := s.repository.GetVersion(ctx, strings.TrimSpace(projectID), strings.TrimSpace(versionID))
	now := s.now()
	result := IntegrityResult{ArtifactID: version.ArtifactID, VersionID: versionID, ExpectedSize: version.SizeBytes, ExpectedSHA256: version.SHA256, CheckedAt: now}
	if err != nil {
		return result, err
	}
	_, _, _, file, err := s.openVersion(ctx, projectID, versionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Status, result.Message = IntegrityMissing, "Artifact 对象文件不存在"
			return result, nil
		}
		return result, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxArtifactBytes+1))
	if err != nil {
		return result, fmt.Errorf("verify Artifact: %w", err)
	}
	result.ActualSize = written
	result.ActualSHA256 = hex.EncodeToString(hash.Sum(nil))
	if written == version.SizeBytes && result.ActualSHA256 == version.SHA256 {
		result.Status = IntegrityVerified
	} else {
		result.Status, result.Message = IntegrityMismatch, "Artifact 对象的大小或 SHA256 与不可变版本记录不一致"
	}
	return result, nil
}

func (s *Service) Download(ctx context.Context, projectID, versionID, destination string) error {
	version, _, _, input, err := s.openVersion(ctx, projectID, versionID)
	if err != nil {
		return err
	}
	defer input.Close()
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return fmt.Errorf("download destination is required")
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("destination already exists: %s", info.Name())
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	tempID, err := s.newID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(abs), ".sciaide-download-"+tempID)
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, maxArtifactBytes+1))
	syncErr, closeErr := output.Sync(), output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		if copyErr != nil {
			return fmt.Errorf("copy Artifact download: %w", copyErr)
		}
		if syncErr != nil {
			return fmt.Errorf("sync Artifact download: %w", syncErr)
		}
		return fmt.Errorf("close Artifact download: %w", closeErr)
	}
	if written != version.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != version.SHA256 {
		_ = os.Remove(temporary)
		return fmt.Errorf("Artifact integrity check failed before download")
	}
	if err := filepublish.NoReplace(temporary, abs); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish Artifact download: %w", err)
	}
	return nil
}

func (s *Service) Recover(ctx context.Context) (RecoveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := RecoveryResult{}
	callIDs, err := s.repository.RecoverableToolCallIDs(ctx)
	if err != nil {
		return result, err
	}
	for _, callID := range callIDs {
		registered, err := s.registerToolArtifactsLocked(ctx, callID)
		for _, item := range registered {
			if item.Created {
				result.ToolArtifactsRecovered++
			}
		}
		if err != nil {
			result.ToolArtifactsFailed++
		}
	}
	projects, err := s.projects.List(ctx)
	if err != nil {
		return result, err
	}
	for _, selected := range projects {
		if err := project.VerifyPrivateDataLayout(selected); err != nil {
			continue
		}
		referenced, err := s.repository.BlobPaths(ctx, selected.ID)
		if err != nil {
			return result, err
		}
		rootPath := project.PrivateDataPath(selected)
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			continue
		}
		if temporaryDirectory, openErr := root.Open("tmp"); openErr == nil {
			entries, _ := temporaryDirectory.ReadDir(-1)
			_ = temporaryDirectory.Close()
			for _, entry := range entries {
				if artifactTemporaryName(entry.Name()) {
					if err := root.Remove(filepath.Join("tmp", entry.Name())); err == nil {
						result.TemporaryFilesRemoved++
					}
				}
			}
		}
		_ = root.Close()
		if result.ToolArtifactsFailed > 0 {
			continue
		}
		objects := filepath.Join(rootPath, "artifacts", "objects")
		walkErr := filepath.WalkDir(objects, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, relErr := filepath.Rel(rootPath, path)
			if relErr != nil {
				return nil
			}
			if _, ok := referenced[filepath.ToSlash(relative)]; !ok {
				if os.Remove(path) == nil {
					result.OrphanObjectsRemoved++
				}
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, os.ErrNotExist) {
			return result, fmt.Errorf("scan Artifact objects for project %s: %w", selected.ID, walkErr)
		}
	}
	return result, nil
}

func artifactTemporaryName(name string) bool {
	for _, prefix := range []string{"artifact-", "export-source-", "export-validate-", "preview-source-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

type saveCommand struct {
	ProjectID      string
	ArtifactID     string
	Name           string
	Kind           Kind
	FileName       string
	MIMEType       string
	SourceKind     SourceKind
	SourceKey      string
	Provenance     Provenance
	Lineage        []Lineage
	Citations      []Citation
	ExpectedSHA256 string
}

func (s *Service) saveBytes(ctx context.Context, cmd saveCommand, contents []byte) (SaveResult, error) {
	return s.saveReader(ctx, cmd, bytes.NewReader(contents), int64(len(contents)), nil)
}

func (s *Service) saveReader(ctx context.Context, cmd saveCommand, input io.Reader, expectedSize int64, sourceKey func(string) string) (SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveReaderLocked(ctx, cmd, input, expectedSize, sourceKey)
}

func (s *Service) saveReaderLocked(ctx context.Context, cmd saveCommand, input io.Reader, expectedSize int64, sourceKey func(string) string) (SaveResult, error) {
	if err := validName(strings.TrimSpace(cmd.Name)); err != nil {
		return SaveResult{}, err
	}
	cmd.Provenance.Extra = cloneStringMap(cmd.Provenance.Extra)
	cmd.Provenance.Extra["artifactNameSnapshot"] = strings.TrimSpace(cmd.Name)
	if expectedSize < 0 || expectedSize > maxArtifactBytes {
		return SaveResult{}, fmt.Errorf("Artifact exceeds the 1 GiB limit")
	}
	selected, err := s.projects.Get(ctx, strings.TrimSpace(cmd.ProjectID))
	if err != nil {
		return SaveResult{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return SaveResult{}, fmt.Errorf("project Artifact storage is unavailable: %w", err)
	}
	if artifactID := strings.TrimSpace(cmd.ArtifactID); artifactID != "" {
		detail, err := s.repository.Get(ctx, selected.ID, artifactID)
		if err != nil {
			return SaveResult{}, err
		}
		if detail.Artifact.Status != StatusActive {
			return SaveResult{}, fmt.Errorf("restore the Artifact before adding a version")
		}
		if detail.Artifact.Kind != cmd.Kind {
			return SaveResult{}, fmt.Errorf("new version kind does not match the Artifact")
		}
		if current := strings.TrimSpace(detail.Artifact.CurrentVersionID); current != "" {
			cmd.Lineage = append(cmd.Lineage, Lineage{
				RelationKind:            "artifact_version",
				SourceIDSnapshot:        current,
				SourceArtifactVersionID: current,
				Label:                   "上一版本",
			})
		}
	}
	root, err := os.OpenRoot(project.PrivateDataPath(selected))
	if err != nil {
		return SaveResult{}, fmt.Errorf("open project data root: %w", err)
	}
	defer root.Close()
	objectRelative, digest, written, err := s.publishContentObject(root, input, expectedSize, cmd.ExpectedSHA256)
	if err != nil {
		return SaveResult{}, err
	}
	if sourceKey != nil {
		cmd.SourceKey = sourceKey(digest)
	}
	artifactID := strings.TrimSpace(cmd.ArtifactID)
	if artifactID == "" {
		artifactID, err = s.newID()
		if err != nil {
			return SaveResult{}, err
		}
	}
	blobID, versionID := "", ""
	if blobID, err = s.newID(); err != nil {
		return SaveResult{}, err
	}
	if versionID, err = s.newID(); err != nil {
		return SaveResult{}, err
	}
	now := s.now()
	for index := range cmd.Lineage {
		cmd.Lineage[index].ID, err = s.newID()
		if err != nil {
			return SaveResult{}, fmt.Errorf("generate Artifact lineage id: %w", err)
		}
		cmd.Lineage[index].Ordinal = index
		cmd.Lineage[index].CreatedAt = now
		if len(cmd.Lineage[index].Metadata) == 0 {
			cmd.Lineage[index].Metadata = json.RawMessage(`{}`)
		}
	}
	for index := range cmd.Citations {
		cmd.Citations[index].ID, err = s.newID()
		if err != nil {
			return SaveResult{}, fmt.Errorf("generate Artifact citation id: %w", err)
		}
		cmd.Citations[index].Ordinal = index
		cmd.Citations[index].CreatedAt = now
	}
	version := Version{
		ID: versionID, ArtifactID: artifactID, BlobID: blobID, FileName: safeName(cmd.FileName),
		MIMEType: normalizeMIME(cmd.MIMEType), SizeBytes: written, SHA256: digest,
		SourceKind: cmd.SourceKind, SourceKey: cmd.SourceKey, Provenance: cmd.Provenance,
		Lineage: cmd.Lineage, Citations: cmd.Citations, CreatedAt: now,
	}
	return s.repository.CreateVersion(ctx, CreateVersionRecord{
		ArtifactID: artifactID, ProjectID: selected.ID, Name: strings.TrimSpace(cmd.Name), Kind: cmd.Kind,
		Blob:    BlobRecord{ID: blobID, ProjectID: selected.ID, SHA256: digest, SizeBytes: written, MIMEType: version.MIMEType, StorageRelativePath: filepath.ToSlash(objectRelative), CreatedAt: now},
		Version: version,
	})
}

var errArtifactSHA256Mismatch = errors.New("Artifact source no longer matches its frozen ToolResult SHA256")

func (s *Service) publishContentObject(root *os.Root, input io.Reader, expectedSize int64, expectedSHA256 string) (string, string, int64, error) {
	if expectedSize < 0 || expectedSize > maxArtifactBytes {
		return "", "", 0, fmt.Errorf("Artifact exceeds the 1 GiB limit")
	}
	if err := root.MkdirAll("tmp", 0o700); err != nil {
		return "", "", 0, err
	}
	tempID, err := s.newID()
	if err != nil {
		return "", "", 0, err
	}
	tempRelative := filepath.Join("tmp", "artifact-"+tempID)
	temporary, err := root.OpenFile(tempRelative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", 0, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(input, maxArtifactBytes+1))
	syncErr, closeErr := temporary.Sync(), temporary.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != expectedSize || written > maxArtifactBytes {
		_ = root.Remove(tempRelative)
		if copyErr != nil {
			return "", "", 0, fmt.Errorf("copy Artifact: %w", copyErr)
		}
		return "", "", 0, fmt.Errorf("Artifact source changed while it was being saved")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA256 != "" && digest != expectedSHA256 {
		_ = root.Remove(tempRelative)
		return "", "", 0, errArtifactSHA256Mismatch
	}
	objectRelative := filepath.Join("artifacts", "objects", digest[:2], digest)
	if err := root.MkdirAll(filepath.Dir(objectRelative), 0o700); err != nil {
		_ = root.Remove(tempRelative)
		return "", "", 0, err
	}
	objectPath := filepath.Join(root.Name(), objectRelative)
	temporaryPath := filepath.Join(root.Name(), tempRelative)
	if err := filepublish.NoReplace(temporaryPath, objectPath); err != nil {
		if verifyErr := verifyObject(root, objectRelative, written, digest); verifyErr != nil {
			_ = root.Remove(tempRelative)
			return "", "", 0, fmt.Errorf("publish Artifact object: %w", errors.Join(err, verifyErr))
		}
		_ = root.Remove(tempRelative)
	}
	if err := verifyObject(root, objectRelative, written, digest); err != nil {
		return "", "", 0, err
	}
	return objectRelative, digest, written, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func (s *Service) openVersion(ctx context.Context, projectID, versionID string) (Version, BlobRecord, project.Project, *os.File, error) {
	projectID, versionID = strings.TrimSpace(projectID), strings.TrimSpace(versionID)
	version, blob, err := s.repository.GetVersion(ctx, projectID, versionID)
	if err != nil {
		return Version{}, BlobRecord{}, project.Project{}, nil, err
	}
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return Version{}, BlobRecord{}, project.Project{}, nil, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return Version{}, BlobRecord{}, selected, nil, err
	}
	root, err := os.OpenRoot(project.PrivateDataPath(selected))
	if err != nil {
		return Version{}, BlobRecord{}, selected, nil, err
	}
	file, err := root.Open(filepath.FromSlash(blob.StorageRelativePath))
	_ = root.Close()
	if err != nil {
		return version, blob, selected, nil, fmt.Errorf("open Artifact object: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return version, blob, selected, nil, fmt.Errorf("Artifact object is not a regular file")
	}
	return version, blob, selected, file, nil
}

func workspaceRelativePath(workspace, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("Workspace file path is required")
	}
	relative := value
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		var err error
		relative, err = filepath.Rel(workspace, value)
		if err != nil {
			return "", fmt.Errorf("resolve Workspace file: %w", err)
		}
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("Artifact file must be inside the project Workspace")
	}
	first := clean
	if separator := strings.IndexRune(first, os.PathSeparator); separator >= 0 {
		first = first[:separator]
	}
	if strings.EqualFold(first, project.PrivateDirectoryName) {
		return "", fmt.Errorf("cannot register SciAide's private project data as an Artifact")
	}
	return clean, nil
}

func inspectSource(file *os.File, extension string) (string, Kind, error) {
	header := make([]byte, 3072)
	count, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", "", err
	}
	header = header[:count]
	value := normalizeMIME(contenttype.Detect(header).String())
	if sameMIME(value, "application/zip") {
		if archiveType, archiveErr := officeArchiveMIME(file); archiveErr != nil {
			return "", "", archiveErr
		} else if archiveType != "" {
			value = archiveType
		}
	}
	extension = strings.ToLower(extension)
	extensionType := mimeForExtension(extension)
	if canRefineDetectedMIME(value, extensionType, header) {
		value = extensionType
	}
	return value, kindFor(value, extension), nil
}

func kindFor(mimeType, extension string) Kind {
	extension = strings.ToLower(extension)
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return KindImage
	case isSpreadsheetMIME(mimeType), isTextMIME(mimeType) && (extension == ".csv" || extension == ".tsv"):
		return KindData
	case isTextMIME(mimeType) && isCodeExtension(extension):
		return KindCode
	case isTextMIME(mimeType), isDocumentMIME(mimeType):
		return KindDocument
	default:
		return KindOther
	}
}

func mimeForExtension(extension string) string {
	if value := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(extension))); value != "" {
		return normalizeMIME(value)
	}
	switch strings.ToLower(extension) {
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".go":
		return "text/x-go; charset=utf-8"
	case ".py":
		return "text/x-python; charset=utf-8"
	case ".r":
		return "text/x-r; charset=utf-8"
	case ".ts":
		return "text/typescript; charset=utf-8"
	case ".ipynb", ".json":
		return "application/json"
	default:
		return ""
	}
}

func canRefineDetectedMIME(detected, extensionType string, header []byte) bool {
	if extensionType == "" || !isTextMIME(extensionType) || !utf8.Valid(header) || bytes.IndexByte(header, 0) >= 0 {
		return false
	}
	detectedType, _, err := mime.ParseMediaType(detected)
	if err != nil {
		return false
	}
	return detectedType == "text/plain" || detectedType == "application/octet-stream"
}

func isTextMIME(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml") || mediaType == "application/yaml" || mediaType == "application/x-yaml" || mediaType == "application/javascript"
}

func isSpreadsheetMIME(value string) bool {
	mediaType, _, _ := mime.ParseMediaType(value)
	return strings.Contains(mediaType, "spreadsheet") || strings.Contains(mediaType, "excel") || mediaType == "text/csv" || mediaType == "text/tab-separated-values"
}

func isDocumentMIME(value string) bool {
	mediaType, _, _ := mime.ParseMediaType(value)
	return mediaType == "application/pdf" || mediaType == "application/msword" || mediaType == "application/rtf" || strings.Contains(mediaType, "wordprocessingml") || strings.Contains(mediaType, "presentationml") || strings.Contains(mediaType, "opendocument.text")
}

func officeArchiveMIME(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect ZIP Artifact: %w", err)
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return "", nil
	}
	if len(archive.File) > 100_000 {
		return "", nil
	}
	hasContentTypes, hasWord, hasWorkbook, hasPresentation := false, false, false, false
	for _, entry := range archive.File {
		name := strings.ToLower(filepath.ToSlash(entry.Name))
		switch name {
		case "[content_types].xml":
			hasContentTypes = true
		case "word/document.xml":
			hasWord = true
		case "xl/workbook.xml":
			hasWorkbook = true
		case "ppt/presentation.xml":
			hasPresentation = true
		}
	}
	if !hasContentTypes {
		return "", nil
	}
	switch {
	case hasWord && !hasWorkbook && !hasPresentation:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	case hasWorkbook && !hasWord && !hasPresentation:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	case hasPresentation && !hasWord && !hasWorkbook:
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
	default:
		return "", nil
	}
}

func isInlineRasterMIME(value string) bool {
	mediaType, _, _ := mime.ParseMediaType(value)
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/avif":
		return true
	default:
		return false
	}
}

func trimIncompleteUTF8(value []byte) []byte {
	if utf8.Valid(value) {
		return value
	}
	for count := 1; count <= 3 && count < len(value); count++ {
		candidate := value[:len(value)-count]
		if utf8.Valid(candidate) {
			return candidate
		}
	}
	return value
}

func isCodeExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".go", ".py", ".r", ".js", ".ts", ".json", ".yaml", ".yml", ".ipynb":
		return true
	default:
		return false
	}
}

func normalizeMIME(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "application/octet-stream"
	}
	if len(value) > 255 {
		return "application/octet-stream"
	}
	return value
}

func sameMIME(left, right string) bool {
	leftType, _, leftErr := mime.ParseMediaType(left)
	rightType, _, rightErr := mime.ParseMediaType(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(leftType, rightType)
}

func validName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 200 || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("Artifact name must contain between 1 and 200 characters")
	}
	return nil
}

func safeName(value string) string {
	value = strings.TrimSpace(filepath.Base(value))
	value = strings.Map(func(character rune) rune {
		if character < 32 || strings.ContainsRune(`<>:"/\\|?*`, character) {
			return '_'
		}
		return character
	}, value)
	value = strings.Trim(value, ". ")
	if value == "" {
		value = "artifact"
	}
	if len([]rune(value)) > 240 {
		value = string([]rune(value)[:240])
	}
	return value
}

func ensureExtension(name, extension string) string {
	if strings.EqualFold(filepath.Ext(name), extension) {
		return name
	}
	return name + extension
}

func defaultAssistantName(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "研究回答"
	}
	return title + " - 研究回答"
}

func nonNilSkills(values []SkillSnapshot) []SkillSnapshot {
	if values == nil {
		return []SkillSnapshot{}
	}
	return values
}

func canReadWorkspace(values []tool.PermissionRequirement) bool {
	for _, value := range values {
		if value.Kind == tool.PermissionWorkspaceRead || value.Kind == tool.PermissionWorkspaceWrite {
			return true
		}
	}
	return false
}

func workspacePermissionCovers(workspace string, values []tool.PermissionRequirement, relative string) bool {
	for _, value := range values {
		if value.Kind != tool.PermissionWorkspaceRead && value.Kind != tool.PermissionWorkspaceWrite {
			continue
		}
		resource := strings.TrimSpace(value.Resource)
		if resource == "" || filepath.Clean(resource) == "." {
			return true
		}
		scope, err := workspaceRelativePath(workspace, resource)
		if err != nil {
			continue
		}
		descendant, err := filepath.Rel(scope, relative)
		if err == nil && descendant != ".." && !filepath.IsAbs(descendant) && !strings.HasPrefix(descendant, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func citationsFromTool(values []tool.CitationRef, source ToolSource) []Citation {
	if source.ToolName != appcitation.KnowledgeToolName || len(values) == 0 {
		return []Citation{}
	}
	markers := make([]string, 0, len(values))
	for _, value := range values {
		if value.ProjectID != source.ProjectID {
			continue
		}
		markers = append(markers, value.Reference)
	}
	resolved := appcitation.Resolve(source.RunID, "artifact:"+source.CallID, strings.Join(markers, " "), []tool.Call{{
		ID: source.CallID, RunID: source.RunID, ToolName: source.ToolName, Status: tool.CallCompleted,
		Result: &tool.Result{Status: tool.ResultSuccess, Citations: values},
	}}, time.Time{})
	result := make([]Citation, 0, len(resolved))
	for _, value := range resolved {
		if value.ProjectID != source.ProjectID {
			continue
		}
		result = append(result, Citation{
			Reference: value.Reference, SourceRunIDSnapshot: value.RunID,
			SourceToolCallIDSnapshot: value.ToolCallID, IndexVersionID: value.IndexVersionID, DocumentID: value.DocumentID,
			AttachmentID: value.AttachmentID, ChunkID: value.ChunkID, SourceName: value.SourceName, MIMEType: value.MIMEType,
			Locator: value.Locator, Title: value.Title, Quote: value.Quote, QuoteSHA256: value.QuoteSHA256,
			SourceStart: value.SourceStart, SourceEnd: value.SourceEnd,
			BibliographyIDSnapshot: value.BibliographyID, BibliographySnapshot: append(json.RawMessage(nil), value.Bibliography...), EvidenceLevel: value.EvidenceLevel,
		})
	}
	return result
}

func verifyObject(root *os.Root, relative string, expectedSize int64, expectedSHA256 string) error {
	file, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("verify existing Artifact object: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, expectedSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		if copyErr != nil {
			return fmt.Errorf("verify existing Artifact object: %w", copyErr)
		}
		return fmt.Errorf("close existing Artifact object: %w", closeErr)
	}
	if written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fmt.Errorf("existing Artifact object conflicts with its content address")
	}
	return nil
}
