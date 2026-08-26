package projectarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
)

var archiveFixtureTime = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

type archiveFixtureEntry struct {
	name   string
	data   []byte
	method uint16
	mode   os.FileMode
}

type archiveFixtureRepository struct {
	snapshot      Snapshot
	projectExists map[string]bool
	rewrite       project.Project
	mergeErr      error
	mergeCalls    int
}

func (r *archiveFixtureRepository) CreateSnapshot(_ context.Context, _ string, destination string) (Snapshot, error) {
	if err := os.WriteFile(destination, []byte("sqlite-fixture"), 0o600); err != nil {
		return Snapshot{}, err
	}
	return r.snapshot, nil
}

func (r *archiveFixtureRepository) ValidateSnapshot(context.Context, string, string, int) (Snapshot, error) {
	return r.snapshot, nil
}

func (r *archiveFixtureRepository) RewriteSnapshot(_ context.Context, _ string, restored project.Project) (RewritePlan, error) {
	r.rewrite = restored
	return RewritePlan{}, nil
}

func (r *archiveFixtureRepository) RewriteKnowledgeIndexes(context.Context, string, RewritePlan) error {
	return nil
}

func (r *archiveFixtureRepository) MergeSnapshot(context.Context, string, []SkillBinding) (MergeReport, error) {
	r.mergeCalls++
	if r.mergeErr != nil {
		return MergeReport{}, r.mergeErr
	}
	if r.projectExists == nil {
		r.projectExists = map[string]bool{}
	}
	if r.rewrite.ID != "" {
		r.projectExists[r.rewrite.ID] = true
	}
	return MergeReport{MissingSkillBindings: []SkillBinding{}}, nil
}

func (r *archiveFixtureRepository) ProjectExists(_ context.Context, projectID string) (bool, error) {
	return r.projectExists[projectID], nil
}

type archiveFixtureProjectLoader struct{ value project.Project }

func (l archiveFixtureProjectLoader) Get(_ context.Context, projectID string) (project.Project, error) {
	if l.value.ID != projectID {
		return project.Project{}, errors.New("project not found")
	}
	return l.value, nil
}

func TestExtractAndVerifyArchiveRejectsUntrustedZIPs(t *testing.T) {
	database := []byte("sqlite-fixture")
	artifact := []byte("artifact")
	artifactPath := "files/artifacts/objects/aa/" + strings.Repeat("a", 64)
	artifactStorage := strings.TrimPrefix(artifactPath, "files/")
	artifactEntry := fixtureFileEntry(artifactPath, artifactStorage, FileArtifactObject, artifact)

	tests := []struct {
		name    string
		entries func() []archiveFixtureEntry
		want    string
	}{
		{
			name: "path traversal",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				return fixtureArchiveEntries(manifest, database, archiveFixtureEntry{name: "../escape", data: []byte("escape")})
			},
			want: "unsafe path",
		},
		{
			name: "duplicate path",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				return append(fixtureArchiveEntries(manifest, database), archiveFixtureEntry{name: databasePath, data: database})
			},
			want: "duplicate path",
		},
		{
			name: "case collision",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				return append(fixtureArchiveEntries(manifest, database), archiveFixtureEntry{name: "PROJECT.SQLITE", data: database})
			},
			want: "case-colliding",
		},
		{
			name: "compression bomb",
			entries: func() []archiveFixtureEntry {
				bomb := bytes.Repeat([]byte{0}, 2<<20)
				manifest := fixtureManifest(bomb, nil)
				return fixtureArchiveEntries(manifest, bomb)
			},
			want: "compression ratio",
		},
		{
			name: "missing declared blob",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, []FileEntry{artifactEntry})
				return fixtureArchiveEntries(manifest, database)
			},
			want: "declared file",
		},
		{
			name: "missing database",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, []FileEntry{artifactEntry})
				return []archiveFixtureEntry{fixtureManifestEntry(manifest), {name: artifactPath, data: artifact}}
			},
			want: "database payload is missing",
		},
		{
			name: "undeclared entry",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				return fixtureArchiveEntries(manifest, database, archiveFixtureEntry{name: artifactPath, data: artifact})
			},
			want: "unlisted entries",
		},
		{
			name: "oversized path component",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				name := "files/artifacts/objects/aa/" + strings.Repeat("x", 256)
				return fixtureArchiveEntries(manifest, database, archiveFixtureEntry{name: name, data: artifact})
			},
			want: "path component",
		},
		{
			name: "windows reserved path component",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				return fixtureArchiveEntries(manifest, database, archiveFixtureEntry{name: "files/artifacts/objects/aa/CON.txt", data: artifact})
			},
			want: "path component",
		},
		{
			name: "symlink entry",
			entries: func() []archiveFixtureEntry {
				manifest := fixtureManifest(database, nil)
				return fixtureArchiveEntries(manifest, database, archiveFixtureEntry{name: artifactPath, data: []byte("target"), mode: os.ModeSymlink | 0o777})
			},
			want: "link or directory",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "malicious.sciaide-project")
			writeFixtureArchive(t, archivePath, test.entries())
			destination := filepath.Join(t.TempDir(), "extract")
			if err := os.MkdirAll(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := extractAndVerifyArchive(context.Background(), archivePath, destination)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("archive error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExtractAndVerifyArchiveAcceptsBoundedChineseAndLongPaths(t *testing.T) {
	database := []byte("sqlite-fixture")
	chinese := []byte("中文科研证据")
	long := []byte("long-name")
	chinesePath := "files/attachments/objects/中文/研究记录.txt"
	longPath := "files/artifacts/objects/aa/" + strings.Repeat("l", 220)
	files := []FileEntry{
		fixtureFileEntry(chinesePath, strings.TrimPrefix(chinesePath, "files/"), FileAttachment, chinese),
		fixtureFileEntry(longPath, strings.TrimPrefix(longPath, "files/"), FileArtifactObject, long),
	}
	manifest := fixtureManifest(database, files)
	archivePath := filepath.Join(t.TempDir(), "valid.sciaide-project")
	writeFixtureArchive(t, archivePath, fixtureArchiveEntries(manifest, database,
		archiveFixtureEntry{name: chinesePath, data: chinese}, archiveFixtureEntry{name: longPath, data: long}))
	destination := filepath.Join(t.TempDir(), "extract")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	verified, err := extractAndVerifyArchive(context.Background(), archivePath, destination)
	if err != nil || len(verified.Files) != 2 {
		t.Fatalf("verified archive = %#v, %v", verified, err)
	}
	for _, entry := range files {
		contents, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(entry.Path)))
		if err != nil || fixtureSHA(contents) != entry.SHA256 {
			t.Fatalf("extracted %q = %q, %v", entry.Path, contents, err)
		}
	}
}

func TestPublishNoReplacePreservesExistingTargets(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
		if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := filepublish.NoReplace(source, destination); err == nil {
			t.Fatal("publish replaced an existing file")
		}
		contents, _ := os.ReadFile(destination)
		if string(contents) != "existing" {
			t.Fatalf("destination changed to %q", contents)
		}
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("failed publish removed source: %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(destination, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, "sentinel"), []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := filepublish.NoReplace(source, destination); err == nil {
			t.Fatal("publish replaced an existing directory")
		}
		contents, err := os.ReadFile(filepath.Join(destination, "sentinel"))
		if err != nil || string(contents) != "existing" {
			t.Fatalf("destination directory changed: %q, %v", contents, err)
		}
	})
}

func TestProjectArchiveExportPublishRaceDoesNotOverwriteDestination(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	selected := project.Project{ID: "source-project", Name: "Source", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged, CreatedAt: archiveFixtureTime, UpdatedAt: archiveFixtureTime}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := project.PrepareRestoredWorkspace(workspace, selected.ID); err != nil {
		t.Fatal(err)
	}
	repository := &archiveFixtureRepository{snapshot: Snapshot{Project: selected, DatabaseSchemaVersion: 49, Files: []FileSource{}, SkillBindings: []SkillBinding{}}}
	service := newArchiveFixtureService(t, root, repository, archiveFixtureProjectLoader{value: selected})
	destination := filepath.Join(root, "export.sciaide-project")
	service.publish = func(source, target string) error {
		if target != destination {
			t.Fatalf("publish target = %q", target)
		}
		if err := os.WriteFile(target, []byte("appeared"), 0o600); err != nil {
			return err
		}
		return filepublish.NoReplace(source, target)
	}
	if _, err := service.Export(context.Background(), selected.ID, destination); err == nil || !strings.Contains(err.Error(), "publish") {
		t.Fatalf("export race error = %v", err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "appeared" {
		t.Fatalf("existing archive was overwritten: %q, %v", contents, err)
	}
}

func TestProjectArchiveRestoreFailureNeverPublishesPartialProject(t *testing.T) {
	database := []byte("sqlite-fixture")
	payload := []byte("artifact")
	path := "files/artifacts/objects/aa/" + strings.Repeat("a", 64)
	file := fixtureFileEntry(path, strings.TrimPrefix(path, "files/"), FileArtifactObject, payload)

	tests := []struct {
		name      string
		files     []FileEntry
		payloads  []archiveFixtureEntry
		configure func(*Service, *archiveFixtureRepository, string)
		wantMerge int
	}{
		{
			name:  "disk full while staging",
			files: []FileEntry{file}, payloads: []archiveFixtureEntry{{name: path, data: payload}},
			configure: func(service *Service, _ *archiveFixtureRepository, _ string) {
				service.copyFile = func(string, string, int64, string) error { return syscall.ENOSPC }
			},
		},
		{
			name: "target appears during publish",
			configure: func(service *Service, _ *archiveFixtureRepository, finalWorkspace string) {
				service.publish = func(source, target string) error {
					if target != finalWorkspace {
						t.Fatalf("publish target = %q", target)
					}
					if err := os.MkdirAll(target, 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("existing"), 0o600); err != nil {
						return err
					}
					return filepublish.NoReplace(source, target)
				}
			},
		},
		{
			name: "database merge fails after publish",
			configure: func(_ *Service, repository *archiveFixtureRepository, _ string) {
				repository.mergeErr = errors.New("merge failed")
			},
			wantMerge: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := fixtureManifest(database, test.files)
			archivePath := filepath.Join(root, "restore.sciaide-project")
			writeFixtureArchive(t, archivePath, fixtureArchiveEntries(manifest, database, test.payloads...))
			snapshotFiles := make([]FileSource, 0, len(test.files))
			for _, entry := range test.files {
				snapshotFiles = append(snapshotFiles, FileSource{StorageRelativePath: entry.StorageRelativePath, Kind: entry.Kind, Required: true, ExpectedSize: entry.SizeBytes, ExpectedSHA256: entry.SHA256})
			}
			repository := &archiveFixtureRepository{snapshot: Snapshot{
				Project:               project.Project{ID: manifest.SourceProject.ID, Name: manifest.SourceProject.Name, Description: manifest.SourceProject.Description, CreatedAt: manifest.SourceProject.CreatedAt, UpdatedAt: manifest.SourceProject.UpdatedAt},
				DatabaseSchemaVersion: manifest.DatabaseSchemaVersion, Files: snapshotFiles, SkillBindings: []SkillBinding{}, Stats: manifest.Stats,
			}}
			service := newArchiveFixtureService(t, root, repository, archiveFixtureProjectLoader{value: project.Project{ID: "unused"}})
			service.newID = func() (string, error) { return "restored-project", nil }
			finalWorkspace := filepath.Join(root, "managed", "restored-project")
			test.configure(service, repository, finalWorkspace)
			_, err := service.Restore(context.Background(), RestoreCommand{Path: archivePath})
			if err == nil {
				t.Fatal("restore failure was accepted")
			}
			if repository.mergeCalls != test.wantMerge {
				t.Fatalf("merge calls = %d, want %d", repository.mergeCalls, test.wantMerge)
			}
			if test.name == "target appears during publish" {
				contents, readErr := os.ReadFile(filepath.Join(finalWorkspace, "sentinel"))
				if readErr != nil || string(contents) != "existing" {
					t.Fatalf("publish race target changed: %q, %v", contents, readErr)
				}
			} else if _, statErr := os.Lstat(finalWorkspace); !os.IsNotExist(statErr) {
				t.Fatalf("partial restored workspace remains: %v", statErr)
			}
		})
	}
}

func TestProjectArchiveRecoveryCleansStagingAndResolvesRestoreMarkers(t *testing.T) {
	root := t.TempDir()
	repository := &archiveFixtureRepository{projectExists: map[string]bool{"published": true}}
	service := newArchiveFixtureService(t, root, repository, archiveFixtureProjectLoader{value: project.Project{ID: "unused"}})
	service.now = func() time.Time { return archiveFixtureTime }

	for _, name := range []string{"restore-stale", "export-stale", "unrelated"} {
		if err := os.MkdirAll(filepath.Join(root, "staging", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"published", "orphan"} {
		workspace := filepath.Join(root, "managed", id)
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := project.PrepareRestoredWorkspace(workspace, id); err != nil {
			t.Fatal(err)
		}
		if err := writeRestoreMarker(project.PrivateDataPath(project.Project{ID: id, WorkspacePath: workspace}), restoreMarker{SchemaVersion: 1, ProjectID: id, SourceProjectID: "source", StartedAt: archiveFixtureTime}); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.Recover(context.Background())
	if err != nil || result.StagingDirectoriesRemoved != 2 || result.PublishedMarkersRemoved != 1 || result.OrphanWorkspacesArchived != 1 {
		t.Fatalf("recovery = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "staging", "unrelated")); err != nil {
		t.Fatalf("unrelated staging directory was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "published")); err != nil {
		t.Fatalf("published workspace was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "published", project.PrivateDirectoryName, restoreMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("published restore marker remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan workspace remains visible: %v", err)
	}
	trash, err := os.ReadDir(filepath.Join(root, "trash"))
	if err != nil || len(trash) != 1 || !strings.Contains(trash[0].Name(), "restore-orphan") {
		t.Fatalf("recovery trash = %#v, %v", trash, err)
	}
}

func newArchiveFixtureService(t *testing.T, root string, repository Repository, projects ProjectLoader) *Service {
	t.Helper()
	service, err := NewService(repository, projects, filepath.Join(root, "managed"), filepath.Join(root, "staging"), filepath.Join(root, "trash"), "0.4.0-test")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func fixtureManifest(database []byte, files []FileEntry) Manifest {
	if files == nil {
		files = []FileEntry{}
	}
	return Manifest{
		SchemaVersion: SchemaVersion, Application: "SciAide", ApplicationVersion: "0.4.0-test", DatabaseSchemaVersion: 49,
		CreatedAt:     archiveFixtureTime,
		SourceProject: ProjectSnapshot{ID: "source-project", Name: "Source project", Description: "fixture", CreatedAt: archiveFixtureTime, UpdatedAt: archiveFixtureTime},
		Database:      DatabaseEntry{Path: databasePath, SizeBytes: int64(len(database)), SHA256: fixtureSHA(database)},
		Files:         files, SkillBindings: []SkillBinding{}, Stats: ArchiveStats{}, Excluded: []string{"secrets"},
	}
}

func fixtureFileEntry(path, storage string, kind FileKind, data []byte) FileEntry {
	return FileEntry{Path: path, StorageRelativePath: storage, Kind: kind, SizeBytes: int64(len(data)), SHA256: fixtureSHA(data)}
}

func fixtureSHA(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func fixtureManifestEntry(manifest Manifest) archiveFixtureEntry {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		panic(err)
	}
	return archiveFixtureEntry{name: manifestPath, data: append(contents, '\n')}
}

func fixtureArchiveEntries(manifest Manifest, database []byte, extra ...archiveFixtureEntry) []archiveFixtureEntry {
	entries := []archiveFixtureEntry{fixtureManifestEntry(manifest), {name: databasePath, data: database}}
	return append(entries, extra...)
}

func writeFixtureArchive(t *testing.T, destination string, entries []archiveFixtureEntry) {
	t.Helper()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		method := entry.method
		if method == 0 {
			method = zip.Deflate
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		mode := entry.mode
		if mode == 0 {
			mode = 0o600
		}
		header.SetMode(mode)
		created, err := writer.CreateHeader(header)
		if err != nil {
			writer.Close()
			output.Close()
			t.Fatal(err)
		}
		if _, err := created.Write(entry.data); err != nil {
			writer.Close()
			output.Close()
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
