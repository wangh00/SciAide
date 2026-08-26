package opensciskill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
	"gopkg.in/yaml.v3"
)

//go:embed all:defaultskills defaultskills.manifest.json defaultskills.upstream.manifest.json capability-audit.v1.json
var bundled embed.FS

const (
	DefaultRoot          = "defaultskills"
	DefaultReadRunes     = 7_000
	MaxReadRunes         = 7_500
	MaxCatalogEntries    = 1_024
	MaxSkillMarkdownSize = 2 * 1024 * 1024
	MaxResourceBytes     = 256 * 1024
	MaxMaterializeBytes  = 2 * 1024 * 1024
)

type Origin string

const (
	OriginDefault   Origin = "default"
	OriginInstalled Origin = "installed"
	OriginUser      Origin = "user"
	OriginProject   Origin = "project"
)

var originPriority = map[Origin]int{
	OriginDefault: 0, OriginInstalled: 1, OriginUser: 2, OriginProject: 3,
}

type Info struct {
	Name                   string     `json:"name"`
	Description            string     `json:"description"`
	Category               string     `json:"category,omitempty"`
	Tags                   []string   `json:"tags"`
	RoutingAliases         []string   `json:"routingAliases"`
	Origin                 Origin     `json:"origin"`
	Entry                  bool       `json:"entry"`
	Enabled                bool       `json:"enabled"`
	AllowedTools           []string   `json:"allowedTools"`
	ContentHash            string     `json:"contentHash"`
	PackageHash            string     `json:"packageHash"`
	FileCount              int        `json:"fileCount"`
	ReferenceCount         int        `json:"referenceCount"`
	AssetCount             int        `json:"assetCount"`
	ScriptCount            int        `json:"scriptCount"`
	InstructionRunes       int        `json:"instructionRunes"`
	Overridden             []Origin   `json:"overridden"`
	Namespace              string     `json:"namespace,omitempty"`
	ReviewVerdict          string     `json:"reviewVerdict,omitempty"`
	RepoURL                string     `json:"repoUrl,omitempty"`
	PinnedSHA              string     `json:"pinnedSha,omitempty"`
	InstalledAt            string     `json:"installedAt,omitempty"`
	Capability             Capability `json:"capability"`
	CapabilityReason       string     `json:"capabilityReason"`
	CapabilityAuditVersion string     `json:"capabilityAuditVersion,omitempty"`
	RequiredTools          []string   `json:"requiredTools"`
	MissingTools           []string   `json:"missingTools"`
	PythonPackages         []string   `json:"pythonPackages"`
	CLIDependencies        []string   `json:"cliDependencies"`
	ExternalServices       []string   `json:"externalServices"`
	CapabilityLimitations  []string   `json:"capabilityLimitations"`

	location string
	root     string
	bundled  bool
}

type Category struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Snapshot struct {
	Skills       []Info             `json:"skills"`
	Categories   []Category         `json:"categories"`
	Diagnostics  []string           `json:"diagnostics"`
	Capabilities map[Capability]int `json:"capabilities"`
	DefaultCount int                `json:"defaultCount"`
	EnabledCount int                `json:"enabledCount"`
}

type PolicyBatchResult struct {
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Total     int `json:"total"`
}

type Chunk struct {
	Name       string               `json:"name"`
	Origin     Origin               `json:"origin"`
	Mode       string               `json:"mode"`
	Section    string               `json:"section,omitempty"`
	Content    string               `json:"content"`
	Offset     int                  `json:"offset"`
	NextOffset int                  `json:"nextOffset"`
	TotalRunes int                  `json:"totalRunes"`
	Truncated  bool                 `json:"truncated"`
	Sections   []InstructionSection `json:"sections"`
}

type Resource struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	BytesRead     int    `json:"bytesRead"`
	OriginalBytes int64  `json:"originalBytes"`
	Truncated     bool   `json:"truncated"`
}

type ResourceEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Text      bool   `json:"text"`
	MediaType string `json:"mediaType,omitempty"`
}

type ResourceList struct {
	Name        string          `json:"name"`
	PackageHash string          `json:"packageHash"`
	Resources   []ResourceEntry `json:"resources"`
}

type MaterializedResource struct {
	Name        string `json:"name"`
	SourcePath  string `json:"sourcePath"`
	PackageHash string `json:"packageHash"`
	Contents    []byte `json:"-"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

type SourceEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Text      bool   `json:"text"`
	MediaType string `json:"mediaType,omitempty"`
}

type SourceTree struct {
	Name        string        `json:"name"`
	Origin      Origin        `json:"origin"`
	PackageHash string        `json:"packageHash"`
	Entries     []SourceEntry `json:"entries"`
}

type SourceFile struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	MediaType     string `json:"mediaType,omitempty"`
	Text          bool   `json:"text"`
	Content       string `json:"content"`
	OriginalBytes int64  `json:"originalBytes"`
}

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type Service struct {
	db            *sql.DB
	projects      ProjectLoader
	installedRoot string
	userRoot      string
	stagingRoot   string
	archiveRoot   string
	auditor       *capabilityAuditor
	registry      tool.Registry

	mu    sync.RWMutex
	cache map[string]Snapshot
}

func NewService(db *sql.DB, projects ProjectLoader, dataRoot string) (*Service, error) {
	if db == nil || projects == nil || strings.TrimSpace(dataRoot) == "" {
		return nil, fmt.Errorf("OpenScience Skill service is not configured")
	}
	installed := filepath.Join(dataRoot, "installed-skills")
	user := filepath.Join(dataRoot, "user-skills")
	staging := filepath.Join(dataRoot, "skill-install-staging")
	archive := filepath.Join(dataRoot, "skill-archives")
	for _, value := range []string{installed, user, staging, archive} {
		if err := os.MkdirAll(value, 0o700); err != nil {
			return nil, fmt.Errorf("create Skill source directory: %w", err)
		}
	}
	auditor, err := loadCapabilityAuditor()
	if err != nil {
		return nil, err
	}
	return &Service{db: db, projects: projects, installedRoot: installed, userRoot: user, stagingRoot: staging, archiveRoot: archive, auditor: auditor, cache: map[string]Snapshot{}}, nil
}

// SetToolRegistry binds capability declarations to the tools exposed by the
// current process. Catalog results are recomputed against it on every read so
// a stopped MCP server or removed builtin cannot leave a stale availability
// claim in the UI or routing prompt.
func (s *Service) SetToolRegistry(registry tool.Registry) error {
	if registry == nil {
		return fmt.Errorf("Tool registry is required")
	}
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
	return nil
}

func (s *Service) Roots() (installed, user string) { return s.installedRoot, s.userRoot }

func (s *Service) Invalidate() {
	s.mu.Lock()
	s.cache = map[string]Snapshot{}
	s.mu.Unlock()
}

func (s *Service) Catalog(ctx context.Context, projectID string) (Snapshot, error) {
	projectID = strings.TrimSpace(projectID)
	s.mu.RLock()
	value, ok := s.cache[projectID]
	s.mu.RUnlock()
	if ok {
		return s.runtimeSnapshot(ctx, cloneSnapshot(value)), nil
	}
	value, err := s.discover(ctx, projectID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	s.cache[projectID] = value
	s.mu.Unlock()
	return s.runtimeSnapshot(ctx, cloneSnapshot(value)), nil
}

func (s *Service) runtimeSnapshot(ctx context.Context, value Snapshot) Snapshot {
	s.mu.RLock()
	registry := s.registry
	s.mu.RUnlock()
	value.Capabilities = make(map[Capability]int)
	value.EnabledCount = 0
	for index := range value.Skills {
		s.auditor.applyRuntime(ctx, registry, &value.Skills[index])
		value.Capabilities[value.Skills[index].Capability]++
		if value.Skills[index].Enabled {
			value.EnabledCount++
		}
	}
	return value
}

func (s *Service) SetEnabled(ctx context.Context, projectID, name string, enabled bool) (Info, error) {
	projectID = strings.TrimSpace(projectID)
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Info{}, fmt.Errorf("valid Skill name is required")
	}
	if _, err := s.resolve(ctx, projectID, name); err != nil {
		return Info{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO skill_policies(skill_name,enabled,updated_at) VALUES (?,?,?)
		ON CONFLICT(skill_name) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, name, enabled, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Info{}, fmt.Errorf("save Skill policy: %w", err)
	}
	s.Invalidate()
	snapshot, err := s.Catalog(ctx, projectID)
	if err != nil {
		return Info{}, err
	}
	for _, item := range snapshot.Skills {
		if item.Name == name {
			return item, nil
		}
	}
	return Info{}, fmt.Errorf("Skill %q not found", name)
}

func (s *Service) SetAllEnabled(ctx context.Context, projectID string, enabled bool) (PolicyBatchResult, error) {
	projectID = strings.TrimSpace(projectID)
	snapshot, err := s.Catalog(ctx, projectID)
	if err != nil {
		return PolicyBatchResult{}, err
	}
	result := PolicyBatchResult{Total: len(snapshot.Skills)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range snapshot.Skills {
		if item.Enabled == enabled {
			result.Unchanged++
		} else {
			result.Changed++
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO skill_policies(skill_name,enabled,updated_at) VALUES (?,?,?)
			ON CONFLICT(skill_name) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, item.Name, enabled, now); err != nil {
			return result, fmt.Errorf("save Skill policy: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	s.Invalidate()
	return result, nil
}

func (s *Service) WriteUser(ctx context.Context, name, content string) (Info, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Info{}, fmt.Errorf("Skill name must match [A-Za-z0-9][A-Za-z0-9_-]{0,63}")
	}
	if len(content) == 0 || len(content) > MaxSkillMarkdownSize || !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return Info{}, fmt.Errorf("Skill Markdown is empty, invalid, or too large")
	}
	parsed, _, err := parseMarkdown([]byte(content))
	if err != nil {
		return Info{}, err
	}
	if parsed.Name != name {
		return Info{}, fmt.Errorf("Skill frontmatter name %q does not match %q", parsed.Name, name)
	}
	if rejection := rejectSkillContent(parsed.Description, content); rejection != "" {
		return Info{}, fmt.Errorf("Skill rejected by local safety review: %s", rejection)
	}
	dir := filepath.Join(s.userRoot, name)
	if current, statErr := os.Lstat(dir); statErr == nil && (!current.IsDir() || current.Mode()&os.ModeSymlink != 0) {
		return Info{}, fmt.Errorf("user Skill directory is unsafe")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return Info{}, statErr
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Info{}, err
	}
	temporary, err := os.CreateTemp(s.userRoot, ".skill-*.tmp")
	if err != nil {
		return Info{}, err
	}
	tmp := temporary.Name()
	defer os.Remove(tmp)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.WriteString(content)
	}
	closeErr := temporary.Close()
	if err != nil {
		return Info{}, err
	}
	if closeErr != nil {
		return Info{}, closeErr
	}
	if err := filepublish.Replace(tmp, filepath.Join(dir, "SKILL.md")); err != nil {
		return Info{}, err
	}
	s.Invalidate()
	snapshot, err := s.Catalog(ctx, "")
	if err != nil {
		return Info{}, err
	}
	for _, item := range snapshot.Skills {
		if item.Name == name && item.Origin == OriginUser {
			return item, nil
		}
	}
	return Info{}, fmt.Errorf("saved Skill was not discovered")
}

func (s *Service) DeleteUser(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return fmt.Errorf("valid Skill name is required")
	}
	target := filepath.Join(s.userRoot, name)
	if err := ensureDirectChild(s.userRoot, target); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return fmt.Errorf("user Skill %q not found", name)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("user Skill directory is unsafe")
	}
	archive, err := s.archivePath("user-removed", name)
	if err != nil {
		return err
	}
	if err := os.Rename(target, archive); err != nil {
		return err
	}
	s.Invalidate()
	return nil
}

func (s *Service) ReadUser(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return "", fmt.Errorf("valid Skill name is required")
	}
	target := filepath.Join(s.userRoot, name)
	if err := ensureDirectChild(s.userRoot, target); err != nil {
		return "", err
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("user Skill %q not found or unsafe", name)
	}
	contents, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("read user Skill: %w", err)
	}
	if len(contents) > MaxSkillMarkdownSize {
		return "", fmt.Errorf("user Skill exceeds size limit")
	}
	meta, _, err := parseMarkdown(contents)
	if err != nil || meta.Name != name {
		return "", fmt.Errorf("user Skill is invalid")
	}
	return string(contents), nil
}

func (s *Service) Browse(ctx context.Context, projectID, category string) ([]Info, error) {
	snapshot, err := s.Catalog(ctx, projectID)
	if err != nil {
		return nil, err
	}
	category = strings.ToLower(strings.TrimSpace(category))
	result := make([]Info, 0)
	for _, item := range snapshot.Skills {
		if item.Enabled && item.Entry && item.Capability != CapabilityUnavailable && strings.ToLower(normalizedCategory(item.Category)) == category {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Service) Load(ctx context.Context, projectID, name string, offset, limit int) (Info, Chunk, string, error) {
	info, err := s.resolve(ctx, projectID, name)
	if err != nil {
		// Project and user sources can be edited outside SciAide. Refresh once
		// before rejecting a name that may have been added since discovery.
		s.Invalidate()
		if info, err = s.resolve(ctx, projectID, name); err != nil {
			return Info{}, Chunk{}, "", err
		}
	}
	if !info.Enabled {
		return Info{}, Chunk{}, "", fmt.Errorf("Skill %q is disabled", info.Name)
	}
	if info.Capability == CapabilityUnavailable {
		return Info{}, Chunk{}, "", fmt.Errorf("Skill %q is currently unavailable: %s", info.Name, info.CapabilityReason)
	}
	body, err := s.verifiedInstructionBody(info)
	if err != nil {
		// A known disk package may have changed while its catalog entry was
		// cached. Re-discover it so a new Run never combines current text with
		// stale provenance hashes.
		s.Invalidate()
		if info, err = s.resolve(ctx, projectID, name); err != nil {
			return Info{}, Chunk{}, "", err
		}
		if !info.Enabled {
			return Info{}, Chunk{}, "", fmt.Errorf("Skill %q is disabled", info.Name)
		}
		if info.Capability == CapabilityUnavailable {
			return Info{}, Chunk{}, "", fmt.Errorf("Skill %q is currently unavailable: %s", info.Name, info.CapabilityReason)
		}
		if body, err = s.verifiedInstructionBody(info); err != nil {
			return Info{}, Chunk{}, "", err
		}
	}
	chunk, err := sliceRunes(info.Name, info.Origin, body, offset, limit)
	return info, chunk, body, err
}

// LoadForRun implements lazy semantic routing without weakening Run
// reproducibility. The first load snapshots the complete instruction body;
// continuation calls page through that snapshot even if a source later moves.
func (s *Service) LoadForRun(ctx context.Context, runID, projectID, toolCallID, name string, offset, limit int) (Info, Chunk, bool, error) {
	name = strings.TrimSpace(name)
	if persisted, err := s.GetRunSkill(ctx, runID, name); err == nil {
		if persisted.ProjectID != strings.TrimSpace(projectID) {
			return Info{}, Chunk{}, false, fmt.Errorf("loaded Skill belongs to another project")
		}
		chunk, err := sliceRunes(persisted.Name, persisted.Origin, persisted.Instructions, offset, limit)
		info := Info{Name: persisted.Name, Origin: persisted.Origin, Category: persisted.Category, ContentHash: persisted.ContentHash, PackageHash: persisted.PackageHash, InstructionRunes: len([]rune(persisted.Instructions)), Enabled: true, Entry: true}
		persisted.CapabilityAudit.apply(&info)
		return info, chunk, false, err
	} else if !errors.Is(err, ErrRunSkillNotFound) {
		return Info{}, Chunk{}, false, err
	}
	info, chunk, body, err := s.Load(ctx, projectID, name, offset, limit)
	if err != nil {
		return Info{}, Chunk{}, false, err
	}
	_, created, err := s.RecordRunSkill(ctx, runID, projectID, toolCallID, info, body)
	return info, chunk, created, err
}

// LoadStructuredForRun returns a section index for long Markdown Skills and
// loads individual sections from the same complete immutable Run snapshot.
func (s *Service) LoadStructuredForRun(ctx context.Context, runID, projectID, toolCallID, name, section string, offset, limit int) (Info, Chunk, bool, error) {
	info, _, created, err := s.LoadForRun(ctx, runID, projectID, toolCallID, name, 0, 1)
	if err != nil {
		return Info{}, Chunk{}, false, err
	}
	loaded, err := s.GetRunSkill(ctx, runID, info.Name)
	if err != nil {
		return Info{}, Chunk{}, false, err
	}
	if info.AllowedTools == nil {
		if current, resolveErr := s.resolveExact(ctx, loaded.ProjectID, loaded); resolveErr == nil {
			info.AllowedTools = current.AllowedTools
		}
	}
	chunk, err := structuredInstructionChunk(info.Name, info.Origin, loaded.Instructions, strings.TrimSpace(section), offset, limit)
	return info, chunk, created, err
}

func (s *Service) ReadResource(ctx context.Context, runID, name, resourcePath string, offset, maxBytes int) (Resource, error) {
	loaded, err := s.GetRunSkill(ctx, runID, name)
	if err != nil {
		return Resource{}, err
	}
	if err := ValidateResourcePath(resourcePath); err != nil {
		return Resource{}, err
	}
	if offset < 0 {
		return Resource{}, fmt.Errorf("resource offset must not be negative")
	}
	if maxBytes == 0 {
		maxBytes = 128 * 1024
	}
	if maxBytes < 1 || maxBytes > MaxResourceBytes {
		return Resource{}, fmt.Errorf("resource read limit is invalid")
	}
	info, err := s.resolveExact(ctx, loaded.ProjectID, loaded)
	if err != nil {
		return Resource{}, err
	}
	contents, err := s.readFile(info, resourcePath, MaxSkillMarkdownSize)
	if err != nil {
		return Resource{}, err
	}
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return Resource{}, fmt.Errorf("Skill resource is not UTF-8 text")
	}
	if offset > len(contents) {
		return Resource{}, fmt.Errorf("resource offset exceeds content length")
	}
	end := min(len(contents), offset+maxBytes)
	for end > offset && end < len(contents) && (contents[end]&0xc0) == 0x80 {
		end--
	}
	return Resource{Name: name, Path: resourcePath, Content: string(contents[offset:end]), BytesRead: end - offset, OriginalBytes: int64(len(contents)), Truncated: end < len(contents)}, nil
}

func (s *Service) ListResources(ctx context.Context, runID, name string) (ResourceList, error) {
	loaded, err := s.GetRunSkill(ctx, runID, strings.TrimSpace(name))
	if err != nil {
		return ResourceList{}, err
	}
	info, err := s.resolveExact(ctx, loaded.ProjectID, loaded)
	if err != nil {
		return ResourceList{}, err
	}
	resources, err := s.listPackageResources(info)
	if err != nil {
		return ResourceList{}, err
	}
	return ResourceList{Name: loaded.Name, PackageHash: loaded.PackageHash, Resources: resources}, nil
}

// MaterializeResource resolves immutable bytes from a Skill already loaded in
// this Run. Writing remains the caller's responsibility so Workspace policy
// and approval stay in the ordinary Tool pipeline.
func (s *Service) MaterializeResource(ctx context.Context, runID, name, resourcePath string) (MaterializedResource, error) {
	loaded, err := s.GetRunSkill(ctx, runID, strings.TrimSpace(name))
	if err != nil {
		return MaterializedResource{}, err
	}
	if err := ValidateResourcePath(resourcePath); err != nil {
		return MaterializedResource{}, err
	}
	info, err := s.resolveExact(ctx, loaded.ProjectID, loaded)
	if err != nil {
		return MaterializedResource{}, err
	}
	contents, err := s.readFile(info, resourcePath, MaxMaterializeBytes)
	if err != nil {
		return MaterializedResource{}, err
	}
	digest := sha256.Sum256(contents)
	return MaterializedResource{Name: loaded.Name, SourcePath: resourcePath, PackageHash: loaded.PackageHash,
		Contents: contents, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(contents))}, nil
}

func (s *Service) ListSource(ctx context.Context, projectID, name string) (SourceTree, error) {
	info, err := s.resolve(ctx, strings.TrimSpace(projectID), strings.TrimSpace(name))
	if err != nil {
		return SourceTree{}, err
	}
	entries, err := s.listPackageSource(info)
	if err != nil {
		return SourceTree{}, err
	}
	if _, err := s.verifiedInstructionBody(info); err != nil {
		return SourceTree{}, err
	}
	return SourceTree{Name: info.Name, Origin: info.Origin, PackageHash: info.PackageHash, Entries: entries}, nil
}

func (s *Service) ReadSourceFile(ctx context.Context, projectID, name, sourcePath string) (SourceFile, error) {
	info, err := s.resolve(ctx, strings.TrimSpace(projectID), strings.TrimSpace(name))
	if err != nil {
		return SourceFile{}, err
	}
	if err := ValidateResourcePath(sourcePath); err != nil {
		return SourceFile{}, err
	}
	entries, err := s.listPackageSource(info)
	if err != nil {
		return SourceFile{}, err
	}
	var selected *SourceEntry
	for index := range entries {
		if entries[index].Kind == "file" && entries[index].Path == sourcePath {
			selected = &entries[index]
			break
		}
	}
	if selected == nil {
		return SourceFile{}, fmt.Errorf("Skill source file %q not found", sourcePath)
	}
	result := SourceFile{Name: info.Name, Path: sourcePath, MediaType: selected.MediaType, Text: selected.Text, OriginalBytes: selected.Size}
	if !selected.Text {
		if _, err := s.verifiedInstructionBody(info); err != nil {
			return SourceFile{}, err
		}
		return result, nil
	}
	contents, err := s.readFile(info, sourcePath, MaxSkillMarkdownSize)
	if err != nil {
		return SourceFile{}, err
	}
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		if _, err := s.verifiedInstructionBody(info); err != nil {
			return SourceFile{}, err
		}
		result.Text = false
		result.Content = ""
		result.MediaType = "application/octet-stream"
		return result, nil
	}
	if _, err := s.verifiedInstructionBody(info); err != nil {
		return SourceFile{}, err
	}
	result.Content = string(contents)
	return result, nil
}

func (s *Service) resolve(ctx context.Context, projectID, name string) (Info, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Info{}, fmt.Errorf("valid Skill name is required")
	}
	snapshot, err := s.Catalog(ctx, projectID)
	if err != nil {
		return Info{}, err
	}
	for _, item := range snapshot.Skills {
		if item.Name == name {
			return item, nil
		}
	}
	return Info{}, fmt.Errorf("Skill %q not found", name)
}

func (s *Service) discover(ctx context.Context, projectID string) (Snapshot, error) {
	policies, err := s.policies(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	candidates := make([]Info, 0, 384)
	diagnostics := make([]string, 0)
	add := func(values []Info, issues []string) {
		candidates = append(candidates, values...)
		diagnostics = append(diagnostics, issues...)
	}
	values, issues := scanFS(bundled, DefaultRoot, OriginDefault, true)
	add(values, issues)
	defaultCount := len(values)
	values, issues = scanDisk(s.installedRoot, OriginInstalled, false)
	values, installedIssues := s.enrichInstalled(values)
	issues = append(issues, installedIssues...)
	add(values, issues)
	values, issues = scanDisk(s.userRoot, OriginUser, false)
	add(values, issues)
	if projectID != "" {
		value, err := s.projects.Get(ctx, projectID)
		if err != nil {
			return Snapshot{}, fmt.Errorf("load project for Skill discovery: %w", err)
		}
		if err := project.VerifyPrivateDataLayout(value); err != nil {
			return Snapshot{}, err
		}
		for _, root := range projectSkillRoots(value.WorkspacePath) {
			values, issues = scanDisk(root, OriginProject, true)
			add(values, issues)
		}
		for _, root := range configuredSkillPaths(value.WorkspacePath) {
			values, issues = scanDisk(root, OriginProject, false)
			add(values, issues)
		}
	}

	selected := map[string]Info{}
	shadowed := map[string][]Origin{}
	for _, item := range candidates {
		item.Enabled = policies[item.Name]
		if _, exists := policies[item.Name]; !exists {
			item.Enabled = true
		}
		current, exists := selected[item.Name]
		if !exists || originPriority[item.Origin] >= originPriority[current.Origin] {
			if exists {
				shadowed[item.Name] = append(shadowed[item.Name], current.Origin)
			}
			selected[item.Name] = item
		} else {
			shadowed[item.Name] = append(shadowed[item.Name], item.Origin)
		}
	}
	result := Snapshot{Skills: make([]Info, 0, len(selected)), Diagnostics: diagnostics, Capabilities: map[Capability]int{}, DefaultCount: defaultCount}
	categories := map[string]int{}
	for name, item := range selected {
		s.auditor.applyBase(&item)
		item.Overridden = uniqueOrigins(shadowed[name])
		result.Skills = append(result.Skills, item)
		categories[normalizedCategory(item.Category)]++
		result.Capabilities[item.Capability]++
		if item.Enabled {
			result.EnabledCount++
		}
	}
	sort.Slice(result.Skills, func(i, j int) bool {
		if result.Skills[i].Category == result.Skills[j].Category {
			return result.Skills[i].Name < result.Skills[j].Name
		}
		return normalizedCategory(result.Skills[i].Category) < normalizedCategory(result.Skills[j].Category)
	})
	for name, count := range categories {
		result.Categories = append(result.Categories, Category{Name: name, Count: count})
	}
	sort.Slice(result.Categories, func(i, j int) bool {
		if result.Categories[i].Count == result.Categories[j].Count {
			return result.Categories[i].Name < result.Categories[j].Name
		}
		return result.Categories[i].Count > result.Categories[j].Count
	})
	if len(result.Skills) > MaxCatalogEntries {
		return Snapshot{}, fmt.Errorf("Skill catalog exceeds supported size")
	}
	return result, nil
}

func (s *Service) policies(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT skill_name,enabled FROM skill_policies ORDER BY skill_name`)
	if err != nil {
		return nil, fmt.Errorf("load Skill policies: %w", err)
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			return nil, err
		}
		result[name] = enabled
	}
	return result, rows.Err()
}

type frontmatter struct {
	Name           string   `yaml:"name"`
	Description    string   `yaml:"description"`
	Category       string   `yaml:"category"`
	Tags           []string `yaml:"tags"`
	Entry          *bool    `yaml:"entry"`
	Disabled       bool     `yaml:"disabled"`
	AllowedTools   []string `yaml:"allowed-tools"`
	RoutingAliases []string `yaml:"routing-aliases"`
}

func parseMarkdown(contents []byte) (frontmatter, string, error) {
	if len(contents) == 0 || len(contents) > MaxSkillMarkdownSize || !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return frontmatter{}, "", fmt.Errorf("invalid SKILL.md")
	}
	text := strings.TrimPrefix(string(contents), "\ufeff")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return frontmatter{}, "", fmt.Errorf("SKILL.md frontmatter is required")
	}
	separator := "\n---\n"
	start := 4
	if strings.HasPrefix(text, "---\r\n") {
		separator, start = "\r\n---\r\n", 5
	}
	end := strings.Index(text[start:], separator)
	if end < 0 {
		return frontmatter{}, "", fmt.Errorf("SKILL.md frontmatter is not terminated")
	}
	var meta frontmatter
	if err := yaml.Unmarshal([]byte(text[start:start+end]), &meta); err != nil {
		return frontmatter{}, "", fmt.Errorf("parse Skill frontmatter: %w", err)
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	meta.Category = strings.TrimSpace(meta.Category)
	meta.Tags = cleanStrings(meta.Tags)
	meta.AllowedTools = cleanStrings(meta.AllowedTools)
	meta.RoutingAliases = cleanStrings(meta.RoutingAliases)
	if len(meta.RoutingAliases) > 64 {
		return frontmatter{}, "", fmt.Errorf("Skill routing-aliases exceeds 64 entries")
	}
	for _, alias := range meta.RoutingAliases {
		if len([]rune(alias)) > 120 {
			return frontmatter{}, "", fmt.Errorf("Skill routing alias exceeds 120 characters")
		}
	}
	if !ValidName(meta.Name) || meta.Description == "" {
		return frontmatter{}, "", fmt.Errorf("Skill frontmatter must contain a valid name and description")
	}
	body := strings.TrimSpace(text[start+end+len(separator):])
	if body == "" {
		return frontmatter{}, "", fmt.Errorf("Skill instruction body is required")
	}
	return meta, body, nil
}

func scanFS(root fs.FS, base string, origin Origin, embedded bool) ([]Info, []string) {
	result := []Info{}
	diagnostics := []string{}
	err := fs.WalkDir(root, base, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			diagnostics = append(diagnostics, walkErr.Error())
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		contents, err := fs.ReadFile(root, file)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", file, err))
			return nil
		}
		meta, body, err := parseMarkdown(contents)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", file, err))
			return nil
		}
		if meta.Disabled || (!embedded && rejectSkillContent(meta.Description, body) != "") || (embedded && rejectSkillDescription(meta.Description) != "") {
			return nil
		}
		info, err := inspectPackageFS(root, path.Dir(file), file, origin, meta, body, embedded)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", file, err))
			return nil
		}
		result = append(result, info)
		return nil
	})
	if err != nil {
		diagnostics = append(diagnostics, err.Error())
	}
	return result, diagnostics
}

func scanDisk(root string, origin Origin, directOpenScience bool) ([]Info, []string) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.IsDir() {
		return nil, []string{fmt.Sprintf("%s: Skill source is unavailable", root)}
	}
	base := os.DirFS(root)
	values, diagnostics := scanFS(base, ".", origin, false)
	filtered := values[:0]
	for _, item := range values {
		rel := filepath.ToSlash(item.root)
		parts := strings.Split(strings.TrimPrefix(rel, "./"), "/")
		if directOpenScience && (len(parts) < 2 || parts[0] != "skill" && parts[0] != "skills") {
			continue
		}
		item.root = filepath.Join(root, filepath.FromSlash(item.root))
		item.location = filepath.Join(root, filepath.FromSlash(item.location))
		filtered = append(filtered, item)
	}
	return filtered, diagnostics
}

func inspectPackageFS(root fs.FS, packageRoot, location string, origin Origin, meta frontmatter, body string, embedded bool) (Info, error) {
	digest := sha256.New()
	fileCount, references, assets, scripts := 0, 0, 0, 0
	err := fs.WalkDir(root, packageRoot, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(root, file)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(file, strings.TrimSuffix(packageRoot, "/")+"/")
		if rel == file || rel == "" || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("package path escapes root")
		}
		fileCount++
		first := strings.Split(rel, "/")[0]
		switch first {
		case "references":
			references++
		case "assets":
			assets++
		case "scripts":
			scripts++
		}
		digest.Write([]byte(rel))
		digest.Write([]byte{0})
		digest.Write(data)
		digest.Write([]byte{0})
		return nil
	})
	if err != nil {
		return Info{}, err
	}
	contentHash := sha256.Sum256([]byte(body))
	entry := true
	if meta.Entry != nil {
		entry = *meta.Entry
	}
	return Info{
		Name: meta.Name, Description: meta.Description, Category: meta.Category,
		Tags: meta.Tags, RoutingAliases: meta.RoutingAliases, Origin: origin, Entry: entry, AllowedTools: meta.AllowedTools,
		Capability: CapabilityUnreviewed, CapabilityReason: "能力审计尚未应用",
		RequiredTools: []string{}, MissingTools: []string{}, PythonPackages: []string{}, CLIDependencies: []string{}, ExternalServices: []string{}, CapabilityLimitations: []string{},
		ContentHash: hex.EncodeToString(contentHash[:]), PackageHash: hex.EncodeToString(digest.Sum(nil)),
		FileCount: fileCount, ReferenceCount: references, AssetCount: assets, ScriptCount: scripts,
		InstructionRunes: len([]rune(body)), location: location, root: packageRoot, bundled: embedded,
	}, nil
}

func (s *Service) readFile(info Info, relative string, maximum int64) ([]byte, error) {
	if info.bundled {
		value, err := fs.ReadFile(bundled, path.Join(info.root, relative))
		if err != nil {
			return nil, err
		}
		if int64(len(value)) > maximum {
			return nil, fmt.Errorf("Skill resource exceeds size limit")
		}
		return value, nil
	}
	root, err := os.OpenRoot(filepath.FromSlash(info.root))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	metadata, err := file.Stat()
	if err != nil || !metadata.Mode().IsRegular() || metadata.Size() > maximum {
		return nil, fmt.Errorf("Skill resource is unsafe or too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("Skill resource exceeds size limit")
	}
	return data, nil
}

func (s *Service) verifiedInstructionBody(info Info) (string, error) {
	contents, err := s.readFile(info, "SKILL.md", MaxSkillMarkdownSize)
	if err != nil {
		return "", err
	}
	meta, body, err := parseMarkdown(contents)
	if err != nil {
		return "", err
	}
	if info.bundled {
		return body, nil
	}
	root := filepath.Clean(info.root)
	packageName := filepath.Base(root)
	current, err := inspectPackageFS(os.DirFS(filepath.Dir(root)), filepath.ToSlash(packageName), path.Join(filepath.ToSlash(packageName), "SKILL.md"), info.Origin, meta, body, false)
	if err != nil {
		return "", err
	}
	if current.Name != info.Name || current.ContentHash != info.ContentHash || current.PackageHash != info.PackageHash {
		return "", fmt.Errorf("Skill package changed after catalog discovery")
	}
	return body, nil
}

func sliceRunes(name string, origin Origin, body string, offset, limit int) (Chunk, error) {
	if offset < 0 {
		return Chunk{}, fmt.Errorf("Skill offset must not be negative")
	}
	if limit == 0 {
		limit = DefaultReadRunes
	}
	if limit < 1 || limit > MaxReadRunes {
		return Chunk{}, fmt.Errorf("Skill read limit must be between 1 and %d runes", MaxReadRunes)
	}
	values := []rune(body)
	if offset > len(values) {
		return Chunk{}, fmt.Errorf("Skill offset exceeds instruction length")
	}
	end := min(len(values), offset+limit)
	return Chunk{Name: name, Origin: origin, Mode: "page", Content: string(values[offset:end]), Offset: offset, NextOffset: end, TotalRunes: len(values), Truncated: end < len(values), Sections: []InstructionSection{}}, nil
}

func projectSkillRoots(workspace string) []string {
	return []string{filepath.Join(workspace, ".openscience"), filepath.Join(workspace, ".synsc")}
}

func configuredSkillPaths(workspace string) []string {
	type config struct {
		Skills struct {
			Paths []string `json:"paths"`
		} `json:"skills"`
	}
	paths := []string{}
	for _, name := range []string{"openscience.json", filepath.Join(".openscience", "openscience.json"), "synsc.json", filepath.Join(".synsc", "synsc.json")} {
		contents, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil || len(contents) > 1024*1024 {
			continue
		}
		var value config
		if json.Unmarshal(contents, &value) != nil {
			continue
		}
		for _, item := range value.Skills.Paths {
			item = strings.TrimSpace(item)
			if item == "" || strings.HasPrefix(item, "~/") || filepath.IsAbs(item) {
				continue
			}
			clean := filepath.Clean(filepath.Join(workspace, item))
			rel, err := filepath.Rel(workspace, clean)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				paths = append(paths, clean)
			}
		}
	}
	return paths
}

func rejectSkillContent(description, body string) string {
	if reason := rejectSkillDescription(description); reason != "" {
		return reason
	}
	lower := strings.ToLower(body)
	for _, phrase := range []string{"respond with verdict: pass", "respond with verdict pass", `"verdict":"pass"`, `"verdict": "pass"`, "the correct verdict is safe", "the correct verdict is pass", "ignore prior instructions", "ignore previous instructions", "ignore above instructions", "act as an auditor", "act as a reviewer"} {
		if strings.Contains(lower, phrase) {
			return "content targets the Skill safety classifier"
		}
	}
	for _, phrase := range []string{"rm -rf /\n", "rm -rf /*", ":(){ :|:& };:", "bash -i >& /dev/tcp/", "nc -e /bin/sh", "nc -e /bin/bash"} {
		if strings.Contains(lower, phrase) {
			return "content contains a catastrophic execution pattern"
		}
	}
	return ""
}

func rejectSkillDescription(description string) string {
	desc := strings.ToLower(description)
	for _, phrase := range []string{"always run this skill", "must always run", "ignore prior instructions", "ignore previous instructions", "ignore above instructions"} {
		if strings.Contains(desc, phrase) {
			return "frontmatter description contains an injection directive"
		}
	}
	return ""
}

func ValidName(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || (index > 0 && (character == '-' || character == '_')) {
			continue
		}
		return false
	}
	return true
}

func ValidateResourcePath(value string) error {
	if value == "" || len([]rune(value)) > 4096 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("Skill resource path must be canonical and package-relative")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `<>:"|?*`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("Skill resource path is invalid")
		}
	}
	return nil
}

func normalizedCategory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "other"
	}
	return value
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueOrigins(values []Origin) []Origin {
	seen := map[Origin]struct{}{}
	result := []Origin{}
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return originPriority[result[i]] > originPriority[result[j]] })
	return result
}

func cloneSnapshot(value Snapshot) Snapshot {
	result := value
	result.Skills = append(make([]Info, 0, len(value.Skills)), value.Skills...)
	for index := range result.Skills {
		result.Skills[index].Tags = append(make([]string, 0, len(result.Skills[index].Tags)), result.Skills[index].Tags...)
		result.Skills[index].RoutingAliases = append(make([]string, 0, len(result.Skills[index].RoutingAliases)), result.Skills[index].RoutingAliases...)
		result.Skills[index].AllowedTools = append(make([]string, 0, len(result.Skills[index].AllowedTools)), result.Skills[index].AllowedTools...)
		result.Skills[index].Overridden = append(make([]Origin, 0, len(result.Skills[index].Overridden)), result.Skills[index].Overridden...)
		result.Skills[index].RequiredTools = cloneStrings(result.Skills[index].RequiredTools)
		result.Skills[index].MissingTools = cloneStrings(result.Skills[index].MissingTools)
		result.Skills[index].PythonPackages = cloneStrings(result.Skills[index].PythonPackages)
		result.Skills[index].CLIDependencies = cloneStrings(result.Skills[index].CLIDependencies)
		result.Skills[index].ExternalServices = cloneStrings(result.Skills[index].ExternalServices)
		result.Skills[index].CapabilityLimitations = cloneStrings(result.Skills[index].CapabilityLimitations)
	}
	result.Categories = append(make([]Category, 0, len(value.Categories)), value.Categories...)
	result.Diagnostics = append(make([]string, 0, len(value.Diagnostics)), value.Diagnostics...)
	result.Capabilities = make(map[Capability]int, len(value.Capabilities))
	for capability, count := range value.Capabilities {
		result.Capabilities[capability] = count
	}
	return result
}

func ensureDirectChild(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relative == "." || strings.Contains(relative, string(os.PathSeparator)) || relative == ".." {
		return fmt.Errorf("Skill path escapes its source root")
	}
	return nil
}
