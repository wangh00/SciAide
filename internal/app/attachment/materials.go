package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/id"
)

type MaterialMetadata struct {
	Title             string `json:"title"`
	Notes             string `json:"notes"`
	Archived          bool   `json:"archived"`
	Collected         bool   `json:"collected"`
	StoredContentKind string `json:"-"`
	OriginTaskTitle   string `json:"originTaskTitle,omitempty"`
}
type Material struct {
	Attachment
	MaterialMetadata
	ContentKind  string `json:"contentKind"`
	IndexStatus  string `json:"indexStatus,omitempty"`
	Reusable     bool   `json:"reusable"`
	IndexChunks  *int   `json:"indexChunks,omitempty"`
	ParseSummary string `json:"parseSummary,omitempty"`
}

type MaterialClearResult struct {
	RemovedIDs []string `json:"removedIds"`
	Errors     []string `json:"errors"`
}

// ClearTaskMetadata only hides the explicitly reviewed automatic Markdown
// attachments. Shared collections and immutable historical evidence stay intact.
func (s *Service) ClearTaskMetadata(ctx context.Context, projectID, taskID string, ids []string) (MaterialClearResult, error) {
	result := MaterialClearResult{RemovedIDs: []string{}, Errors: []string{}}
	if taskID == "" {
		return result, fmt.Errorf("请选择具体科研任务")
	}
	if err := s.validateTask(ctx, projectID, taskID); err != nil {
		return result, err
	}
	if len(ids) == 0 || len(ids) > 10000 {
		return result, fmt.Errorf("清理资料数量无效")
	}
	values := make([]Material, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		m, err := s.GetMaterial(ctx, projectID, taskID, id)
		if err != nil {
			return result, err
		}
		if seen[id] || m.ScopeKind != ScopeTask || m.ResearchTaskID != taskID || m.SourceKind != SourceResearchImport || string(m.Format) != "markdown" {
			return result, fmt.Errorf("只能清理当前任务自动导入的 Markdown 材料")
		}
		seen[id] = true
		values = append(values, m)
	}
	for _, m := range values {
		if _, err := s.SaveMaterial(ctx, projectID, taskID, m.ID, m.Title, m.Notes, true); err != nil {
			result.Errors = append(result.Errors, m.Title+"："+err.Error())
		} else {
			result.RemovedIDs = append(result.RemovedIDs, m.ID)
		}
	}
	return result, nil
}

type MaterialPage struct {
	Text       string `json:"text"`
	NextOffset int    `json:"nextOffset"`
	TotalRunes int    `json:"totalRunes"`
	Truncated  bool   `json:"truncated"`
}
type materialRepository interface {
	MaterialMetadata(context.Context, string) (MaterialMetadata, error)
	SaveMaterialMetadata(context.Context, string, MaterialMetadata) error
}

func (s *Service) RestoreMaterial(ctx context.Context, projectID, taskID, id string) (Material, error) {
	m, err := s.GetMaterial(ctx, projectID, taskID, id)
	if err != nil || !m.Archived {
		return m, err
	}
	return s.SaveMaterial(ctx, projectID, taskID, id, m.Title, m.Notes, false)
}

func (s *Service) MaterialArchived(ctx context.Context, id string) (bool, error) {
	if r, ok := s.repository.(materialRepository); ok {
		m, err := r.MaterialMetadata(ctx, id)
		return m.Archived, err
	}
	return false, nil
}

func (s *Service) MaterialHiddenFromDefault(ctx context.Context, id string) (bool, error) {
	v, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	var meta MaterialMetadata
	if r, ok := s.repository.(materialRepository); ok {
		meta, err = r.MaterialMetadata(ctx, id)
		if err != nil {
			return false, err
		}
	}
	return meta.Archived || (v.ScopeKind == ScopeProjectShared && v.SourceKind != SourceUserImport && !meta.Collected), nil
}

// New selections reject removed library entries. Historical frozen runs keep
// using ReferenceMaterials, which verifies ownership without rewriting history.
func (s *Service) SelectReferenceMaterials(ctx context.Context, projectID, taskID string, ids []string) ([]Attachment, error) {
	values, err := s.ReferenceMaterials(ctx, projectID, taskID, ids)
	if err != nil {
		return nil, err
	}
	for _, v := range values {
		m, e := s.material(ctx, v)
		if e != nil {
			return nil, e
		}
		if m.Archived {
			return nil, fmt.Errorf("资料已移出资料库：%s", m.Title)
		}
		if v.ScopeKind == ScopeProjectShared && !m.Reusable {
			return nil, fmt.Errorf("请先将该研究材料收藏到资料库：%s", m.Title)
		}
	}
	return values, nil
}

func (s *Service) material(ctx context.Context, v Attachment) (Material, error) {
	m := Material{Attachment: v, ContentKind: "user_file"}
	if r, ok := s.repository.(materialRepository); ok {
		meta, err := r.MaterialMetadata(ctx, v.ID)
		if err != nil {
			return m, err
		}
		m.MaterialMetadata = meta
	}
	if m.Title == "" && v.SourceKind == SourceResearchImport {
		if r, ok := s.repository.(interface {
			MaterialTitle(context.Context, string) (string, error)
		}); ok {
			var err error
			m.Title, err = r.MaterialTitle(ctx, v.ID)
			if err != nil {
				return m, err
			}
		}
	}
	if m.Title == "" {
		// Older discovery records can be deleted while their material remains.
		// Read only the generated metadata heading, never present it as verified metadata.
		if v.SourceKind == SourceResearchImport && strings.HasSuffix(v.OriginalName, "-metadata.md") {
			p, err := s.projects.Get(ctx, v.ProjectID)
			if err == nil {
				root, e := os.OpenRoot(project.PrivateDataPath(p))
				if e == nil {
					f, e := root.Open(filepath.FromSlash(v.StorageRelativePath))
					if e == nil {
						data, _ := io.ReadAll(io.LimitReader(f, 8192))
						_ = f.Close()
						first, _, _ := strings.Cut(string(data), "\n")
						if strings.HasPrefix(first, "# ") {
							m.Title = strings.TrimSpace(strings.TrimPrefix(first, "# "))
						}
					}
					_ = root.Close()
				}
			}
		}
	}
	if m.Title == "" {
		m.Title = v.OriginalName
	}
	if v.SourceKind == SourceResearchImport {
		m.ContentKind = "research_material"
		if r, ok := s.repository.(interface {
			MaterialContentKind(context.Context, string) (string, error)
		}); ok {
			kind, err := r.MaterialContentKind(ctx, v.ID)
			if err != nil {
				return m, err
			}
			if kind != "" {
				m.ContentKind = kind
			}
		}
	}
	if m.StoredContentKind != "" {
		m.ContentKind = m.StoredContentKind
	}
	if m.ContentKind == "research_material" && strings.HasSuffix(v.OriginalName, "-metadata.md") {
		m.ContentKind = "metadata_abstract"
	}
	m.Reusable = !m.Archived && v.ScopeKind == ScopeProjectShared && (v.SourceKind == SourceUserImport || m.Collected)
	return m, nil
}

// CollectMaterial creates a separate shared ownership row over immutable bytes.
// It never changes the original task attachment or its frozen citations.
func (s *Service) CollectMaterial(ctx context.Context, projectID, taskID, attachmentID string) (Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.GetMaterial(ctx, projectID, taskID, attachmentID)
	if err != nil {
		return Material{}, err
	}
	if m.Status != StatusReady {
		return Material{}, fmt.Errorf("资料尚未解析完成")
	}
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return Material{}, err
	}
	if err = verifyStoredObject(filepath.Join(project.PrivateDataPath(p), filepath.FromSlash(m.StorageRelativePath)), m.SizeBytes, m.SHA256); err != nil {
		return Material{}, err
	}
	r, ok := s.repository.(interface {
		CollectMaterial(context.Context, Attachment, MaterialMetadata, string) (Attachment, error)
	})
	if !ok {
		return Material{}, fmt.Errorf("资料收藏存储未配置")
	}
	newID, err := id.New()
	if err != nil {
		return Material{}, err
	}
	meta := m.MaterialMetadata
	meta.Collected = true
	meta.Archived = false
	meta.StoredContentKind = m.ContentKind
	v, err := r.CollectMaterial(ctx, m.Attachment, meta, newID)
	if err != nil {
		return Material{}, err
	}
	return s.material(ctx, v)
}
func (s *Service) ListMaterials(ctx context.Context, projectID, taskID string) ([]Material, error) {
	var values []Attachment
	var err error
	if taskID != "" {
		values, err = s.ListForTask(ctx, projectID, taskID)
	} else {
		if _, err = s.projects.Get(ctx, projectID); err != nil {
			return nil, err
		}
		values, err = s.ListForProject(ctx, projectID)
	}
	if err != nil {
		return nil, err
	}
	result := []Material{}
	for _, v := range values {
		m, e := s.material(ctx, v)
		if e != nil {
			return nil, e
		}
		if !m.Archived {
			result = append(result, m)
		}
	}
	return result, nil
}
func (s *Service) GetMaterial(ctx context.Context, projectID, taskID, id string) (Material, error) {
	v, err := s.Get(ctx, id)
	if err != nil {
		return Material{}, err
	}
	if v.ProjectID != projectID || v.ScopeKind == ScopeConversation {
		return Material{}, fmt.Errorf("资料不属于当前项目")
	}
	if _, err = s.projects.Get(ctx, projectID); err != nil {
		return Material{}, err
	}
	if taskID != "" {
		if err = s.validateTask(ctx, projectID, taskID); err != nil {
			return Material{}, err
		}
		if v.ScopeKind != ScopeProjectShared && !(v.ScopeKind == ScopeTask && v.ResearchTaskID == taskID) {
			return Material{}, fmt.Errorf("资料不属于当前任务")
		}
	}
	return s.material(ctx, v)
}
func (s *Service) SaveMaterial(ctx context.Context, projectID, taskID, id, title, notes string, archive bool) (Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.GetMaterial(ctx, projectID, taskID, id)
	if err != nil {
		return m, err
	}
	if taskID != "" && m.ScopeKind != ScopeTask {
		return m, fmt.Errorf("共享资料请在项目资料库管理")
	}
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 500 || len([]rune(notes)) > 8000 {
		return m, fmt.Errorf("标题需要 1–500 字，备注最多 8000 字")
	}
	r, ok := s.repository.(materialRepository)
	if !ok {
		return m, fmt.Errorf("资料管理存储未配置")
	}
	m.Title, m.Notes, m.Archived = title, notes, archive
	err = r.SaveMaterialMetadata(ctx, id, m.MaterialMetadata)
	return m, err
}
func (s *Service) ReadMaterial(ctx context.Context, projectID, taskID, id string, offset int) (MaterialPage, error) {
	m, err := s.GetMaterial(ctx, projectID, taskID, id)
	if err != nil {
		return MaterialPage{}, err
	}
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return MaterialPage{}, err
	}
	if err = verifyStoredObject(filepath.Join(project.PrivateDataPath(p), filepath.FromSlash(m.StorageRelativePath)), m.SizeBytes, m.SHA256); err != nil {
		return MaterialPage{}, err
	}
	_, parsed, err := s.Parsed(ctx, projectID, m.ID)
	if err != nil {
		return MaterialPage{}, err
	}
	total := 0
	for _, u := range parsed.Units {
		total += utf8.RuneCountInString(u.Locator) + utf8.RuneCountInString(u.Content) + 3
	}
	if offset < 0 || offset > total {
		return MaterialPage{}, fmt.Errorf("正文位置无效")
	}
	end := offset + 12000
	if end > total {
		end = total
	}
	var b strings.Builder
	position := 0
	for _, u := range parsed.Units {
		for _, part := range []string{u.Locator, "\n", u.Content, "\n\n"} {
			length := utf8.RuneCountInString(part)
			if position+length > offset && position < end {
				for _, r := range part {
					if position >= offset && position < end {
						b.WriteRune(r)
					}
					position++
				}
			} else {
				position += length
			}
		}
		if position >= end {
			break
		}
	}
	next := end
	if end == total {
		next = -1
	}
	return MaterialPage{Text: b.String(), NextOffset: next, TotalRunes: total, Truncated: parsed.Truncated}, nil
}
func (s *Service) ExportMaterial(ctx context.Context, projectID, taskID, id, destination string) error {
	m, err := s.GetMaterial(ctx, projectID, taskID, id)
	if err != nil {
		return err
	}
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(project.PrivateDataPath(p))
	if err != nil {
		return err
	}
	defer root.Close()
	source, err := root.Open(filepath.FromSlash(m.StorageRelativePath))
	if err != nil {
		return err
	}
	defer source.Close()
	h := sha256.New()
	if _, err = io.Copy(h, source); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return fmt.Errorf("原文件哈希不匹配")
	}
	if _, err = source.Seek(0, 0); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h.Reset()
	var copied int64
	copied, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(source, m.SizeBytes+1))
	if err == nil && (copied != m.SizeBytes || hex.EncodeToString(h.Sum(nil)) != m.SHA256) {
		err = fmt.Errorf("原文件在导出时发生变化")
	}
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return closeErr
	}
	return nil
}
