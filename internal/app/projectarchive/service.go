package projectarchive

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
)

const (
	manifestPath         = "manifest.json"
	databasePath         = "project.sqlite"
	restoreMarkerName    = "restore.json"
	maxArchiveEntries    = 100_000
	maxManifestBytes     = 16 << 20
	maxDatabaseBytes     = 2 << 30
	maxArchiveFileBytes  = 1 << 30
	maxArchiveTotalBytes = 8 << 30
	maxArchiveCompressed = 8 << 30
	maxCompressionRatio  = 200
)

var excludedSecrets = []string{
	"API keys",
	"MCP secrets and server configuration",
	"vision fallback credentials",
	"embedding credentials and query vectors",
	"permission grants and pending approvals",
	"temporary files, Python environments, and rebuildable query caches",
}

type Service struct {
	repository  Repository
	projects    ProjectLoader
	managedRoot string
	stagingRoot string
	trashRoot   string
	version     string
	now         func() time.Time
	newID       func() (string, error)
	copyFile    func(string, string, int64, string) error
	publish     func(string, string) error
	mu          sync.Mutex
}

func NewService(repository Repository, projects ProjectLoader, managedRoot, stagingRoot, trashRoot, version string) (*Service, error) {
	if repository == nil || projects == nil || strings.TrimSpace(managedRoot) == "" || strings.TrimSpace(stagingRoot) == "" || strings.TrimSpace(trashRoot) == "" || strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("project archive dependencies are required")
	}
	return &Service{
		repository: repository, projects: projects, managedRoot: filepath.Clean(managedRoot),
		stagingRoot: filepath.Clean(stagingRoot), trashRoot: filepath.Clean(trashRoot), version: strings.TrimSpace(version),
		now: func() time.Time { return time.Now().UTC() }, newID: id.New,
		copyFile: copyRegularFile, publish: filepublish.NoReplace,
	}, nil
}

func (s *Service) Export(ctx context.Context, projectID, destination string) (ExportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID, destination = strings.TrimSpace(projectID), strings.TrimSpace(destination)
	if projectID == "" || destination == "" {
		return ExportResult{}, fmt.Errorf("project and archive destination are required")
	}
	if !strings.EqualFold(filepath.Ext(destination), Extension) {
		destination += Extension
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve archive destination: %w", err)
	}
	if info, err := os.Lstat(destination); err == nil {
		return ExportResult{}, fmt.Errorf("archive destination already exists: %s", info.Name())
	} else if !os.IsNotExist(err) {
		return ExportResult{}, fmt.Errorf("inspect archive destination: %w", err)
	}
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return ExportResult{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return ExportResult{}, fmt.Errorf("project archive source is unavailable: %w", err)
	}
	if err := os.MkdirAll(s.stagingRoot, 0o700); err != nil {
		return ExportResult{}, err
	}
	staging, err := os.MkdirTemp(s.stagingRoot, "export-")
	if err != nil {
		return ExportResult{}, fmt.Errorf("create archive staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	snapshotPath := filepath.Join(staging, databasePath)
	snapshot, err := s.repository.CreateSnapshot(ctx, projectID, snapshotPath)
	if err != nil {
		return ExportResult{}, err
	}
	if snapshot.Project.ID != selected.ID || filepath.Clean(snapshot.Project.WorkspacePath) != filepath.Clean(selected.WorkspacePath) {
		return ExportResult{}, fmt.Errorf("project changed while its archive was being prepared")
	}
	databaseEntry, err := fileDigest(snapshotPath, maxDatabaseBytes)
	if err != nil {
		return ExportResult{}, fmt.Errorf("verify archive database: %w", err)
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion, Application: "SciAide", ApplicationVersion: s.version,
		DatabaseSchemaVersion: snapshot.DatabaseSchemaVersion, CreatedAt: s.now(),
		SourceProject: ProjectSnapshot{ID: selected.ID, Name: selected.Name, Description: selected.Description, CreatedAt: selected.CreatedAt, UpdatedAt: selected.UpdatedAt},
		Database:      DatabaseEntry{Path: databasePath, SizeBytes: databaseEntry.size, SHA256: databaseEntry.hash},
		Files:         []FileEntry{}, SkillBindings: nonNilBindings(snapshot.SkillBindings), Stats: snapshot.Stats,
		Excluded: append([]string(nil), excludedSecrets...),
	}
	privateRoot := project.PrivateDataPath(selected)
	seen := map[string]struct{}{}
	type sourceEntry struct {
		entry FileEntry
		path  string
	}
	sources := make([]sourceEntry, 0, len(snapshot.Files))
	for _, source := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return ExportResult{}, err
		}
		relative, err := validateStorageRelativePath(source.StorageRelativePath, source.Kind)
		if err != nil {
			return ExportResult{}, err
		}
		if source.Kind == FileWorkflowInput && source.TaskID != "" {
			if err := validateTaskID(source.TaskID); err != nil {
				return ExportResult{}, err
			}
		}
		scopeKey := source.TaskID + "\x00" + relative
		if _, duplicate := seen[scopeKey]; duplicate {
			continue
		}
		absolute := strings.TrimSpace(source.SourcePath)
		if absolute == "" {
			root := privateRoot
			if source.Kind == FileWorkflowInput {
				root = selected.WorkspacePath
				if source.TaskID != "" {
					root, err = project.ResearchTaskWorkspacePath(selected, source.TaskID)
					if err != nil {
						return ExportResult{}, err
					}
				}
			}
			absolute = filepath.Join(root, filepath.FromSlash(relative))
		}
		digest, digestErr := fileDigest(absolute, maxArchiveFileBytes)
		if digestErr != nil {
			if !source.Required && errors.Is(digestErr, os.ErrNotExist) {
				continue
			}
			return ExportResult{}, fmt.Errorf("verify archived %s %q: %w", source.Kind, relative, digestErr)
		}
		if source.ExpectedSize >= 0 && digest.size != source.ExpectedSize {
			return ExportResult{}, fmt.Errorf("archived %s %q no longer matches its recorded size", source.Kind, relative)
		}
		if source.ExpectedSHA256 != "" && !strings.EqualFold(source.ExpectedSHA256, digest.hash) {
			return ExportResult{}, fmt.Errorf("archived %s %q no longer matches its recorded SHA256", source.Kind, relative)
		}
		seen[scopeKey] = struct{}{}
		archivePath := "files/" + relative
		if source.Kind == FileWorkflowInput && source.TaskID != "" {
			archivePath = "files/tasks/" + source.TaskID + "/" + relative
		}
		entry := FileEntry{Path: archivePath, StorageRelativePath: relative, Kind: source.Kind, TaskID: source.TaskID, SizeBytes: digest.size, SHA256: digest.hash}
		manifest.Files = append(manifest.Files, entry)
		sources = append(sources, sourceEntry{entry: entry, path: absolute})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	sort.Slice(sources, func(i, j int) bool { return sources[i].entry.Path < sources[j].entry.Path })
	if err := validateExportBudget(manifest, 0); err != nil {
		return ExportResult{}, err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ExportResult{}, err
	}
	manifestJSON = append(manifestJSON, '\n')
	if len(manifestJSON) > maxManifestBytes {
		return ExportResult{}, fmt.Errorf("project archive manifest exceeds its size limit")
	}
	if err := validateExportBudget(manifest, int64(len(manifestJSON))); err != nil {
		return ExportResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return ExportResult{}, fmt.Errorf("create archive destination directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".sciaide-project-*.tmp")
	if err != nil {
		return ExportResult{}, fmt.Errorf("create archive output: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	archive := zip.NewWriter(temporary)
	if err := writeZIPBytes(archive, manifestPath, manifestJSON); err != nil {
		_ = archive.Close()
		return ExportResult{}, err
	}
	if err := writeZIPFile(ctx, archive, databasePath, snapshotPath, manifest.Database.SizeBytes, manifest.Database.SHA256); err != nil {
		_ = archive.Close()
		return ExportResult{}, err
	}
	for _, source := range sources {
		if err := writeZIPFile(ctx, archive, source.entry.Path, source.path, source.entry.SizeBytes, source.entry.SHA256); err != nil {
			_ = archive.Close()
			return ExportResult{}, err
		}
	}
	if err := archive.Close(); err != nil {
		return ExportResult{}, fmt.Errorf("finalize project archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return ExportResult{}, fmt.Errorf("flush project archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return ExportResult{}, err
	}
	// ZIP headers also consume space. Reject oversized output before publishing
	// it, rather than leaving a seemingly successful but unrestorable archive.
	resultDigest, err := fileDigest(temporaryPath, maxArchiveCompressed)
	if err != nil {
		return ExportResult{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return ExportResult{}, fmt.Errorf("archive destination appeared while exporting")
	} else if !os.IsNotExist(err) {
		return ExportResult{}, err
	}
	if err := s.publish(temporaryPath, destination); err != nil {
		return ExportResult{}, fmt.Errorf("publish project archive: %w", err)
	}
	removeTemporary = false
	return ExportResult{Path: destination, SHA256: resultDigest.hash, SizeBytes: resultDigest.size, FileCount: len(manifest.Files), Manifest: manifest}, nil
}

func validateExportBudget(manifest Manifest, manifestBytes int64) error {
	if len(manifest.Files) > maxArchiveEntries-2 {
		return fmt.Errorf("project archive entry count exceeds its limit")
	}
	if manifestBytes < 0 || manifestBytes > maxManifestBytes || manifest.Database.SizeBytes < 0 || manifest.Database.SizeBytes > maxDatabaseBytes {
		return fmt.Errorf("project archive database or manifest exceeds its size limit")
	}
	total := manifestBytes + manifest.Database.SizeBytes
	for _, entry := range manifest.Files {
		if entry.SizeBytes < 0 || entry.SizeBytes > maxArchiveFileBytes {
			return fmt.Errorf("project archive entry exceeds its size limit")
		}
		total += entry.SizeBytes
		if total > maxArchiveTotalBytes {
			return fmt.Errorf("project archive exceeds its total extraction limit")
		}
	}
	return nil
}

func (s *Service) Restore(ctx context.Context, command RestoreCommand) (RestoreReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command.Path, command.Name = strings.TrimSpace(command.Path), strings.TrimSpace(command.Name)
	if command.Path == "" {
		return RestoreReport{}, fmt.Errorf("project archive path is required")
	}
	archivePath, err := filepath.Abs(command.Path)
	if err != nil {
		return RestoreReport{}, err
	}
	info, err := os.Lstat(archivePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > maxArchiveCompressed {
		return RestoreReport{}, fmt.Errorf("project archive must be a regular file no larger than 8 GiB")
	}
	if err := os.MkdirAll(s.stagingRoot, 0o700); err != nil {
		return RestoreReport{}, err
	}
	staging, err := os.MkdirTemp(s.stagingRoot, "restore-")
	if err != nil {
		return RestoreReport{}, fmt.Errorf("create restore staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	manifest, err := extractAndVerifyArchive(ctx, archivePath, staging)
	if err != nil {
		return RestoreReport{}, err
	}
	snapshotPath := filepath.Join(staging, filepath.FromSlash(manifest.Database.Path))
	validated, err := s.repository.ValidateSnapshot(ctx, snapshotPath, manifest.SourceProject.ID, manifest.DatabaseSchemaVersion)
	if err != nil {
		return RestoreReport{}, err
	}
	if !sameSnapshotManifest(validated, manifest) {
		return RestoreReport{}, fmt.Errorf("archive manifest does not match its project database")
	}
	projectID, err := s.newID()
	if err != nil {
		return RestoreReport{}, err
	}
	name := command.Name
	if name == "" {
		name = strings.TrimSpace(manifest.SourceProject.Name) + " (restored)"
	}
	if err := validateProjectName(name); err != nil {
		return RestoreReport{}, err
	}
	finalWorkspace := filepath.Join(s.managedRoot, projectID)
	if _, err := os.Lstat(finalWorkspace); err == nil {
		return RestoreReport{}, fmt.Errorf("restored project workspace already exists")
	} else if !os.IsNotExist(err) {
		return RestoreReport{}, err
	}
	workspace := filepath.Join(staging, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return RestoreReport{}, err
	}
	restored := project.Project{
		ID: projectID, Name: name, Description: manifest.SourceProject.Description,
		WorkspacePath: finalWorkspace, WorkspaceKind: project.WorkspaceManaged,
		CreatedAt: manifest.SourceProject.CreatedAt, UpdatedAt: s.now(),
	}
	if err := project.PrepareRestoredWorkspace(workspace, restored.ID); err != nil {
		return RestoreReport{}, fmt.Errorf("prepare restored workspace: %w", err)
	}
	privateRoot := project.PrivateDataPath(project.Project{ID: restored.ID, WorkspacePath: workspace})
	var restoredBytes int64
	// Rewrite identities before copying task-owned files so archive task IDs can
	// be mapped to the freshly allocated IDs in the restored SQLite graph.
	plan, err := s.repository.RewriteSnapshot(ctx, snapshotPath, restored)
	if err != nil {
		return RestoreReport{}, err
	}
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return RestoreReport{}, err
		}
		source := filepath.Join(staging, filepath.FromSlash(entry.Path))
		targetRoot := privateRoot
		if entry.Kind == FileWorkflowInput {
			targetRoot = workspace
			if entry.TaskID != "" {
				mapped := entry.TaskID
				if plan.TaskIDs != nil && plan.TaskIDs[mapped] != "" {
					mapped = plan.TaskIDs[mapped]
				}
				var pathErr error
				targetRoot, pathErr = project.ResearchTaskWorkspacePath(project.Project{ID: restored.ID, WorkspacePath: workspace}, mapped)
				if pathErr != nil {
					return RestoreReport{}, pathErr
				}
			}
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(entry.StorageRelativePath))
		if err := s.copyFile(source, target, entry.SizeBytes, entry.SHA256); err != nil {
			return RestoreReport{}, fmt.Errorf("stage restored file %q: %w", entry.Path, err)
		}
		restoredBytes += entry.SizeBytes
	}
	if err := s.repository.RewriteKnowledgeIndexes(ctx, privateRoot, plan); err != nil {
		return RestoreReport{}, err
	}
	marker := restoreMarker{SchemaVersion: 1, ProjectID: restored.ID, SourceProjectID: manifest.SourceProject.ID, StartedAt: s.now()}
	if err := writeRestoreMarker(privateRoot, marker); err != nil {
		return RestoreReport{}, err
	}
	if err := os.MkdirAll(s.managedRoot, 0o700); err != nil {
		return RestoreReport{}, err
	}
	if err := s.publish(workspace, finalWorkspace); err != nil {
		return RestoreReport{}, fmt.Errorf("publish restored workspace: %w", err)
	}
	published := true
	defer func() {
		if published {
			_ = os.RemoveAll(finalWorkspace)
		}
	}()
	merge, err := s.repository.MergeSnapshot(ctx, snapshotPath, manifest.SkillBindings)
	if err != nil {
		return RestoreReport{}, err
	}
	published = false
	_ = os.Remove(filepath.Join(finalWorkspace, project.PrivateDirectoryName, restoreMarkerName))
	return RestoreReport{
		Project: restored, SourceProjectID: manifest.SourceProject.ID, FilesRestored: len(manifest.Files), BytesRestored: restoredBytes,
		HistoricalProfiles: plan.HistoricalProfiles, RestoredSkillBindings: merge.RestoredSkillBindings,
		MissingSkillBindings: nonNilBindings(merge.MissingSkillBindings), SecretsRequireRebinding: plan.HistoricalProfiles > 0,
		Excluded: append([]string(nil), manifest.Excluded...),
	}, nil
}

func (s *Service) Recover(ctx context.Context) (RecoveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := RecoveryResult{}
	if entries, err := os.ReadDir(s.stagingRoot); err == nil {
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || (!strings.HasPrefix(entry.Name(), "restore-") && !strings.HasPrefix(entry.Name(), "export-")) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(s.stagingRoot, entry.Name())); err != nil {
				return result, err
			}
			result.StagingDirectoriesRemoved++
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	entries, err := os.ReadDir(s.managedRoot)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		workspace := filepath.Join(s.managedRoot, entry.Name())
		markerPath := filepath.Join(workspace, project.PrivateDirectoryName, restoreMarkerName)
		contents, err := os.ReadFile(markerPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		var marker restoreMarker
		if json.Unmarshal(contents, &marker) != nil || marker.SchemaVersion != 1 || marker.ProjectID != entry.Name() {
			continue
		}
		exists, err := s.repository.ProjectExists(ctx, marker.ProjectID)
		if err != nil {
			return result, err
		}
		if exists {
			if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
				return result, err
			}
			result.PublishedMarkersRemoved++
			continue
		}
		if err := project.VerifyPrivateDataLayout(project.Project{ID: marker.ProjectID, WorkspacePath: workspace}); err != nil {
			continue
		}
		if err := os.MkdirAll(s.trashRoot, 0o700); err != nil {
			return result, err
		}
		target := filepath.Join(s.trashRoot, "restore-orphan-"+s.now().Format("20060102T150405.000000000Z")+"-"+marker.ProjectID)
		if err := os.Rename(workspace, target); err != nil {
			return result, err
		}
		result.OrphanWorkspacesArchived++
	}
	return result, nil
}

type restoreMarker struct {
	SchemaVersion   int       `json:"schemaVersion"`
	ProjectID       string    `json:"projectId"`
	SourceProjectID string    `json:"sourceProjectId"`
	StartedAt       time.Time `json:"startedAt"`
}

type digestResult struct {
	size int64
	hash string
}

func fileDigest(filePath string, maximum int64) (digestResult, error) {
	lstat, err := os.Lstat(filePath)
	if err != nil {
		return digestResult{}, err
	}
	if !lstat.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 {
		return digestResult{}, fmt.Errorf("file is not a supported regular file")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return digestResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return digestResult{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return digestResult{}, fmt.Errorf("file is not a supported regular file")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || written != info.Size() || written > maximum {
		return digestResult{}, fmt.Errorf("file changed while hashing")
	}
	return digestResult{size: written, hash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func writeZIPBytes(writer *zip.Writer, name string, contents []byte) error {
	header := &zip.FileHeader{Name: name, Method: exportZIPMethod(int64(len(contents)))}
	header.SetMode(0o600)
	header.Modified = time.Unix(0, 0).UTC()
	output, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = output.Write(contents)
	return err
}

func writeZIPFile(ctx context.Context, writer *zip.Writer, name, source string, expectedSize int64, expectedHash string) error {
	lstat, err := os.Lstat(source)
	if err != nil || !lstat.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 || lstat.Size() != expectedSize {
		return fmt.Errorf("archive source %q changed before it could be written", name)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	header := &zip.FileHeader{Name: name, Method: exportZIPMethod(expectedSize)}
	header.SetMode(0o600)
	header.Modified = time.Unix(0, 0).UTC()
	output, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	buffer := make([]byte, 128*1024)
	hash := sha256.New()
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			written += int64(count)
			if written > expectedSize {
				return fmt.Errorf("archive source %q changed while it was written", name)
			}
			if _, err := hash.Write(buffer[:count]); err != nil {
				return err
			}
			if _, err := output.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			if written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
				return fmt.Errorf("archive source %q changed while it was written", name)
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// Store entries subject to the importer's compression-ratio check. This
// conservative policy guarantees round trips even for repetitive scientific
// data without buffering large files or weakening ZIP-bomb defenses.
func exportZIPMethod(size int64) uint16 {
	if size > 1<<20 {
		return zip.Store
	}
	return zip.Deflate
}

func extractAndVerifyArchive(ctx context.Context, archivePath, destination string) (Manifest, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open project archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) < 2 || len(reader.File) > maxArchiveEntries {
		return Manifest{}, fmt.Errorf("project archive entry count is invalid")
	}
	files := make(map[string]*zip.File, len(reader.File))
	foldedFiles := make(map[string]string, len(reader.File))
	var total uint64
	for _, entry := range reader.File {
		name, err := validateArchivePath(entry.Name)
		if err != nil {
			return Manifest{}, err
		}
		if _, duplicate := files[name]; duplicate {
			return Manifest{}, fmt.Errorf("project archive contains duplicate path %q", name)
		}
		folded := strings.ToLower(name)
		if previous, duplicate := foldedFiles[folded]; duplicate {
			return Manifest{}, fmt.Errorf("project archive contains case-colliding paths %q and %q", previous, name)
		}
		foldedFiles[folded] = name
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 || entry.FileInfo().IsDir() {
			return Manifest{}, fmt.Errorf("project archive contains a link or directory entry %q", name)
		}
		if entry.UncompressedSize64 > maxArchiveFileBytes && name != databasePath || name == databasePath && entry.UncompressedSize64 > maxDatabaseBytes {
			return Manifest{}, fmt.Errorf("project archive entry %q exceeds its size limit", name)
		}
		total += entry.UncompressedSize64
		if total > maxArchiveTotalBytes {
			return Manifest{}, fmt.Errorf("project archive exceeds its total extraction limit")
		}
		if entry.UncompressedSize64 > 1<<20 {
			if entry.CompressedSize64 == 0 || entry.UncompressedSize64/entry.CompressedSize64 > maxCompressionRatio {
				return Manifest{}, fmt.Errorf("project archive entry %q has an unsafe compression ratio", name)
			}
		}
		files[name] = entry
	}
	manifestFile := files[manifestPath]
	if manifestFile == nil || manifestFile.UncompressedSize64 > maxManifestBytes {
		return Manifest{}, fmt.Errorf("project archive manifest is missing or oversized")
	}
	manifestContents, err := readZIPEntry(manifestFile, maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifestContents)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode project archive manifest: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	expected := map[string]DatabaseEntry{manifest.Database.Path: manifest.Database}
	fileEntries := make(map[string]FileEntry, len(manifest.Files))
	foldedEntries := make(map[string]string, len(manifest.Files))
	for _, entry := range manifest.Files {
		if _, duplicate := fileEntries[entry.Path]; duplicate {
			return Manifest{}, fmt.Errorf("project archive manifest contains duplicate file %q", entry.Path)
		}
		folded := strings.ToLower(entry.Path)
		if previous, duplicate := foldedEntries[folded]; duplicate {
			return Manifest{}, fmt.Errorf("project archive manifest contains case-colliding files %q and %q", previous, entry.Path)
		}
		foldedEntries[folded] = entry.Path
		fileEntries[entry.Path] = entry
	}
	if files[manifest.Database.Path] == nil {
		return Manifest{}, fmt.Errorf("project archive database payload is missing")
	}
	for name := range fileEntries {
		if files[name] == nil {
			return Manifest{}, fmt.Errorf("project archive declared file %q is missing", name)
		}
	}
	if len(files) != len(fileEntries)+2 {
		return Manifest{}, fmt.Errorf("project archive contains unlisted entries")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	for name, zipEntry := range files {
		if name == manifestPath {
			continue
		}
		var size int64
		var hash string
		if database, ok := expected[name]; ok {
			size, hash = database.SizeBytes, database.SHA256
		} else if fileEntry, ok := fileEntries[name]; ok {
			size, hash = fileEntry.SizeBytes, fileEntry.SHA256
		} else {
			return Manifest{}, fmt.Errorf("project archive entry %q is not declared", name)
		}
		if zipEntry.UncompressedSize64 != uint64(size) {
			return Manifest{}, fmt.Errorf("project archive entry %q size does not match its manifest", name)
		}
		if err := extractZIPEntry(ctx, root, zipEntry, name, size, hash); err != nil {
			return Manifest{}, err
		}
	}
	if err := root.WriteFile(manifestPath, manifestContents, 0o600); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func extractZIPEntry(ctx context.Context, root *os.Root, entry *zip.File, name string, expectedSize int64, expectedHash string) error {
	if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
		return err
	}
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := root.OpenFile(filepath.FromSlash(name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(output, hash), 128*1024)
	var written int64
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			_ = output.Close()
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			written += int64(count)
			if written > expectedSize {
				_ = output.Close()
				return fmt.Errorf("project archive entry %q exceeds its declared size", name)
			}
			if _, err := buffered.Write(buffer[:count]); err != nil {
				_ = output.Close()
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = output.Close()
			return readErr
		}
	}
	flushErr := buffered.Flush()
	syncErr := output.Sync()
	closeErr := output.Close()
	if flushErr != nil || syncErr != nil || closeErr != nil || written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return fmt.Errorf("project archive entry %q failed size or SHA256 validation", name)
	}
	return nil
}

func readZIPEntry(entry *zip.File, maximum int64) ([]byte, error) {
	input, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	contents, err := io.ReadAll(io.LimitReader(input, maximum+1))
	if err != nil || int64(len(contents)) > maximum {
		return nil, fmt.Errorf("project archive entry is oversized")
	}
	return contents, nil
}

func validateManifest(value Manifest) error {
	if value.SchemaVersion != SchemaVersion || value.Application != "SciAide" || strings.TrimSpace(value.ApplicationVersion) == "" || value.DatabaseSchemaVersion < 1 || value.CreatedAt.IsZero() {
		return fmt.Errorf("unsupported or invalid project archive manifest")
	}
	if value.SourceProject.ID == "" || strings.TrimSpace(value.SourceProject.Name) == "" || value.SourceProject.CreatedAt.IsZero() || value.SourceProject.UpdatedAt.IsZero() {
		return fmt.Errorf("project archive source identity is invalid")
	}
	if value.Database.Path != databasePath || value.Database.SizeBytes < 1 || value.Database.SizeBytes > maxDatabaseBytes || !validSHA256(value.Database.SHA256) {
		return fmt.Errorf("project archive database declaration is invalid")
	}
	if len(value.Files) > maxArchiveEntries-2 || len(value.Excluded) == 0 {
		return fmt.Errorf("project archive manifest is unbounded or missing exclusions")
	}
	paths := make(map[string]struct{}, len(value.Files))
	storagePaths := make(map[string]struct{}, len(value.Files))
	for _, entry := range value.Files {
		archivePath, err := validateArchivePath(entry.Path)
		if err != nil || !strings.HasPrefix(archivePath, "files/") || entry.SizeBytes < 0 || entry.SizeBytes > maxArchiveFileBytes || !validSHA256(entry.SHA256) {
			return fmt.Errorf("project archive file declaration is invalid")
		}
		relative, err := validateStorageRelativePath(entry.StorageRelativePath, entry.Kind)
		if entry.TaskID != "" && (entry.Kind != FileWorkflowInput || validateTaskID(entry.TaskID) != nil) {
			return fmt.Errorf("project archive task file declaration is invalid")
		}
		if err != nil || archivePath != "files/"+relative {
			if entry.Kind != FileWorkflowInput || entry.TaskID == "" || archivePath != "files/tasks/"+entry.TaskID+"/"+relative || validateTaskID(entry.TaskID) != nil {
				return fmt.Errorf("project archive file path does not match its storage path")
			}
		}
		foldedArchive, foldedStorage := strings.ToLower(archivePath), strings.ToLower(entry.TaskID+"\x00"+relative)
		if _, duplicate := paths[foldedArchive]; duplicate {
			return fmt.Errorf("project archive manifest contains colliding file paths")
		}
		if _, duplicate := storagePaths[foldedStorage]; duplicate {
			return fmt.Errorf("project archive manifest contains colliding storage paths")
		}
		paths[foldedArchive], storagePaths[foldedStorage] = struct{}{}, struct{}{}
	}
	for _, binding := range value.SkillBindings {
		if strings.TrimSpace(binding.SkillID) == "" || strings.TrimSpace(binding.Version) == "" || !validSHA256(binding.ContentHash) || !validSHA256(binding.PackageHash) || binding.Priority < 0 || binding.Priority > 1000 {
			return fmt.Errorf("project archive Skill binding is invalid")
		}
	}
	return nil
}

func validateArchivePath(value string) (string, error) {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") || strings.Contains(value, ":") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("project archive contains an unsafe path")
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("project archive contains an unsafe path %q", value)
	}
	for _, component := range strings.Split(clean, "/") {
		if component == "" || len(component) > 255 || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") || windowsReservedComponent(component) {
			return "", fmt.Errorf("project archive path component is invalid")
		}
		for _, character := range component {
			if character < 0x20 || character == 0x7f {
				return "", fmt.Errorf("project archive path component is invalid")
			}
		}
	}
	return clean, nil
}

func windowsReservedComponent(value string) bool {
	base := value
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func validateStorageRelativePath(value string, kind FileKind) (string, error) {
	clean, err := validateArchivePath(filepath.ToSlash(strings.TrimSpace(value)))
	if err != nil {
		return "", err
	}
	wanted := ""
	switch kind {
	case FileAttachment:
		wanted = "attachments/objects/"
	case FileDocumentCache:
		wanted = "cache/documents/"
	case FileKnowledgeIndex:
		wanted = "cache/knowledge/"
	case FileArtifactObject:
		wanted = "artifacts/objects/"
	case FileWorkflowInput:
		wanted = "research-inputs/"
	default:
		return "", fmt.Errorf("project archive contains an unknown file kind")
	}
	if !strings.HasPrefix(clean, wanted) {
		return "", fmt.Errorf("project archive %s path is outside its private storage prefix", kind)
	}
	return clean, nil
}

func validateTaskID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || filepath.Base(value) != value || value == "." || value == ".." || strings.ContainsRune(value, 0) {
		return fmt.Errorf("project archive task identity is invalid")
	}
	return nil
}

func copyRegularFile(source, target string, expectedSize int64, expectedHash string) error {
	digest, err := fileDigest(source, maxArchiveFileBytes)
	if err != nil || digest.size != expectedSize || digest.hash != expectedHash {
		return fmt.Errorf("source failed manifest validation")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("restore target already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr, closeErr := output.Sync(), output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(target)
		if copyErr != nil {
			return copyErr
		}
		return fmt.Errorf("flush restored file")
	}
	return nil
}

func writeRestoreMarker(privateRoot string, marker restoreMarker) error {
	contents, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return os.WriteFile(filepath.Join(privateRoot, restoreMarkerName), contents, 0o600)
}

func validateProjectName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 200 || strings.ContainsRune(value, 0) {
		return fmt.Errorf("restored project name must contain between 1 and 200 characters")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("project archive manifest contains trailing JSON")
	}
	return nil
}

func nonNilBindings(values []SkillBinding) []SkillBinding {
	if values == nil {
		return []SkillBinding{}
	}
	return values
}

func sameSnapshotManifest(snapshot Snapshot, manifest Manifest) bool {
	if snapshot.Project.ID != manifest.SourceProject.ID || snapshot.Project.Name != manifest.SourceProject.Name ||
		snapshot.Project.Description != manifest.SourceProject.Description || snapshot.DatabaseSchemaVersion != manifest.DatabaseSchemaVersion ||
		snapshot.Stats != manifest.Stats || len(snapshot.SkillBindings) != len(manifest.SkillBindings) {
		return false
	}
	for index, binding := range snapshot.SkillBindings {
		if binding != manifest.SkillBindings[index] {
			return false
		}
	}
	declared := make(map[string]FileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		declared[entry.TaskID+"\x00"+entry.StorageRelativePath] = entry
	}
	for _, source := range snapshot.Files {
		entry, found := declared[source.TaskID+"\x00"+source.StorageRelativePath]
		if source.Required && !found {
			return false
		}
		if !found {
			continue
		}
		if entry.Kind != source.Kind || source.ExpectedSize >= 0 && entry.SizeBytes != source.ExpectedSize || source.ExpectedSHA256 != "" && entry.SHA256 != strings.ToLower(source.ExpectedSHA256) {
			return false
		}
		delete(declared, source.TaskID+"\x00"+source.StorageRelativePath)
	}
	return len(declared) == 0
}
