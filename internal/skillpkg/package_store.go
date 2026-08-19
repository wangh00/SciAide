package skillpkg

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/wangh00/SciAide/internal/app/skill"
)

const (
	maxArchiveBytes   = 32 * 1024 * 1024
	maxArchiveEntries = 1024
	maxPackageDepth   = 32
)

type FilePackageStore struct {
	installedRoot string
	stagingRoot   string
	backupRoot    string
	now           func() time.Time
	mu            sync.Mutex
}

func NewFilePackageStore(installedRoot, stagingRoot, backupRoot string) *FilePackageStore {
	return &FilePackageStore{
		installedRoot: filepath.Clean(strings.TrimSpace(installedRoot)),
		stagingRoot:   filepath.Clean(strings.TrimSpace(stagingRoot)),
		backupRoot:    filepath.Clean(strings.TrimSpace(backupRoot)),
		now:           func() time.Time { return time.Now().UTC() },
	}
}

func (s *FilePackageStore) Install(ctx context.Context, command skill.InstallCommand) (skill.PackageInstallOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoots(); err != nil {
		return skill.PackageInstallOperation{}, err
	}
	source, err := filepath.Abs(strings.TrimSpace(command.SourcePath))
	if err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("resolve Skill source: %w", err)
	}
	stage, err := os.MkdirTemp(s.stagingRoot, "install-*")
	if err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("create Skill staging directory: %w", err)
	}
	defer func() { _ = removeManagedTree(s.stagingRoot, stage) }()

	var candidate, snapshot, packageNameHint string
	switch command.SourceKind {
	case skill.SourceFolder:
		if containsPath(source, s.stagingRoot) {
			return skill.PackageInstallOperation{}, fmt.Errorf("Skill source cannot contain the managed staging directory")
		}
		candidate = filepath.Join(stage, "package")
		packageNameHint = filepath.Base(source)
		if err := copyPackageTree(ctx, source, candidate); err != nil {
			return skill.PackageInstallOperation{}, fmt.Errorf("stage Skill folder: %w", err)
		}
		snapshot = filepath.Join(stage, "source.zip")
		if err := writePackageZIP(ctx, candidate, snapshot); err != nil {
			return skill.PackageInstallOperation{}, fmt.Errorf("archive Skill folder source: %w", err)
		}
	case skill.SourceZIP:
		if err := validateArchiveSource(source); err != nil {
			return skill.PackageInstallOperation{}, err
		}
		extracted := filepath.Join(stage, "extracted")
		if err := extractPackageZIP(ctx, source, extracted); err != nil {
			return skill.PackageInstallOperation{}, err
		}
		candidate, err = locateExtractedPackage(extracted)
		if err != nil {
			return skill.PackageInstallOperation{}, err
		}
		snapshot = filepath.Join(stage, "source.zip")
		if err := copyRegularFile(ctx, source, snapshot, maxArchiveBytes); err != nil {
			return skill.PackageInstallOperation{}, fmt.Errorf("stage original Skill archive: %w", err)
		}
		packageNameHint = strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
		if candidate != extracted {
			packageNameHint = filepath.Base(candidate)
		}
	default:
		return skill.PackageInstallOperation{}, fmt.Errorf("unsupported Skill source kind %q", command.SourceKind)
	}
	if err := prepareCodexCompatiblePackage(candidate, packageNameHint); err != nil {
		return skill.PackageInstallOperation{}, err
	}

	catalog := NewCatalog(s.installedRoot)
	inspected, err := catalog.InspectDirectory(ctx, candidate)
	if err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("validate staged Skill package: %w", err)
	}
	sourceHash, err := hashRegularFileContext(ctx, snapshot, maxArchiveBytes)
	if err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("hash Skill source archive: %w", err)
	}
	archiveRelative := filepath.ToSlash(filepath.Join("packages", inspected.Skill.Manifest.ID, inspected.Skill.Manifest.Version, sourceHash+".zip"))
	archiveAbsolute, err := safeManagedJoin(s.backupRoot, archiveRelative)
	if err != nil {
		return skill.PackageInstallOperation{}, err
	}
	if err := ensureManagedDirectory(s.backupRoot, filepath.ToSlash(filepath.Dir(filepath.FromSlash(archiveRelative)))); err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("prepare Skill source archive directory: %w", err)
	}
	archiveCreated, err := persistArchive(ctx, snapshot, archiveAbsolute, sourceHash)
	if err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("preserve original Skill package: %w", err)
	}
	operation, err := s.installStaged(ctx, candidate, inspected, command.ReplaceExisting)
	if err != nil {
		if archiveCreated {
			_ = os.Remove(archiveAbsolute)
		}
		return skill.PackageInstallOperation{}, err
	}
	operation.Source = skill.PackageSource{
		Kind:                command.SourceKind,
		Name:                filepath.Base(source),
		Hash:                sourceHash,
		Archived:            true,
		ArchiveRelativePath: archiveRelative,
	}
	operation.SourceArchivePath = archiveAbsolute
	operation.SourceArchiveCreated = archiveCreated
	return operation, nil
}

func (s *FilePackageStore) installStaged(ctx context.Context, candidate string, inspected skill.Package, replace bool) (skill.PackageInstallOperation, error) {
	if err := ctx.Err(); err != nil {
		return skill.PackageInstallOperation{}, err
	}
	relative := inspected.Skill.PackageRelativePath
	destination, err := safeManagedJoin(s.installedRoot, relative)
	if err != nil {
		return skill.PackageInstallOperation{}, err
	}
	idDirectory := filepath.Dir(destination)
	if err := os.MkdirAll(idDirectory, 0o700); err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("create installed Skill id directory: %w", err)
	}
	if err := requireSafeDirectory(idDirectory); err != nil {
		return skill.PackageInstallOperation{}, fmt.Errorf("installed Skill id directory is unsafe")
	}

	operation := skill.PackageInstallOperation{InstalledPath: destination}
	info, statErr := os.Lstat(destination)
	switch {
	case os.IsNotExist(statErr):
		if err := os.Rename(candidate, destination); err != nil {
			return skill.PackageInstallOperation{}, fmt.Errorf("activate staged Skill package: %w", err)
		}
	case statErr != nil:
		return skill.PackageInstallOperation{}, fmt.Errorf("inspect installed Skill destination: %w", statErr)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return skill.PackageInstallOperation{}, fmt.Errorf("installed Skill destination is not a safe directory")
	default:
		existing, loadErr := NewCatalog(s.installedRoot).InspectDirectory(ctx, destination)
		if loadErr == nil && existing.Skill.PackageHash == inspected.Skill.PackageHash {
			operation.Idempotent = true
			break
		}
		if !replace {
			return skill.PackageInstallOperation{}, fmt.Errorf("Skill %s@%s already exists with different or invalid content; explicit replacement is required", inspected.Skill.Manifest.ID, inspected.Skill.Manifest.Version)
		}
		previous, err := s.allocateArchiveDirectory("replaced", inspected.Skill.Manifest.ID, inspected.Skill.Manifest.Version, inspected.Skill.PackageHash)
		if err != nil {
			return skill.PackageInstallOperation{}, err
		}
		if err := os.Rename(destination, previous); err != nil {
			return skill.PackageInstallOperation{}, fmt.Errorf("preserve previous installed Skill: %w", err)
		}
		if err := os.Rename(candidate, destination); err != nil {
			_ = os.Rename(previous, destination)
			return skill.PackageInstallOperation{}, fmt.Errorf("activate replacement Skill package: %w", err)
		}
		operation.Replaced = true
		operation.PreviousPath = previous
	}

	verified, err := NewCatalog(s.installedRoot).Load(ctx, relative, inspected.Skill.PackageHash)
	if err != nil {
		if !operation.Idempotent {
			if moveErr := os.Rename(destination, candidate); moveErr != nil {
				return skill.PackageInstallOperation{}, fmt.Errorf("verify activated Skill package: %w; move invalid activation back to staging: %v", err, moveErr)
			}
			if operation.PreviousPath != "" {
				if restoreErr := os.Rename(operation.PreviousPath, destination); restoreErr != nil {
					return skill.PackageInstallOperation{}, fmt.Errorf("verify activated Skill package: %w; restore previous installation: %v", err, restoreErr)
				}
			}
		}
		return skill.PackageInstallOperation{}, fmt.Errorf("verify activated Skill package: %w", err)
	}
	operation.Package = verified
	return operation, nil
}

func (s *FilePackageStore) RollbackInstall(ctx context.Context, operation skill.PackageInstallOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	removeArchive := func() error {
		if !operation.SourceArchiveCreated || operation.SourceArchivePath == "" {
			return nil
		}
		if err := requireManagedPath(s.backupRoot, operation.SourceArchivePath); err != nil {
			return err
		}
		if err := requireManagedDirectoryPath(s.backupRoot, filepath.Dir(operation.SourceArchivePath)); err != nil {
			return err
		}
		hash, err := hashRegularFileContext(ctx, operation.SourceArchivePath, maxArchiveBytes)
		if err != nil {
			return err
		}
		if hash != operation.Source.Hash {
			return fmt.Errorf("refuse to remove a changed Skill source archive during rollback")
		}
		return os.Remove(operation.SourceArchivePath)
	}
	if operation.Idempotent || operation.InstalledPath == "" {
		return removeArchive()
	}
	if err := s.ensureRoots(); err != nil {
		return err
	}
	if err := requireManagedPath(s.installedRoot, operation.InstalledPath); err != nil {
		return err
	}
	if _, err := NewCatalog(s.installedRoot).Load(ctx, operation.Package.Skill.PackageRelativePath, operation.Package.Skill.PackageHash); err != nil {
		return fmt.Errorf("refuse to rollback an installed Skill that changed again: %w", err)
	}
	failed, err := s.allocateArchiveDirectory("failed", operation.Package.Skill.Manifest.ID, operation.Package.Skill.Manifest.Version, operation.Package.Skill.PackageHash)
	if err != nil {
		return err
	}
	if err := os.Rename(operation.InstalledPath, failed); err != nil {
		return fmt.Errorf("move failed Skill installation aside: %w", err)
	}
	if operation.PreviousPath != "" {
		if err := requireManagedDirectoryPath(s.backupRoot, operation.PreviousPath); err != nil {
			_ = os.Rename(failed, operation.InstalledPath)
			return err
		}
		if err := os.Rename(operation.PreviousPath, operation.InstalledPath); err != nil {
			_ = os.Rename(failed, operation.InstalledPath)
			return fmt.Errorf("restore previous Skill installation: %w", err)
		}
	}
	return removeArchive()
}

func (s *FilePackageStore) Uninstall(ctx context.Context, value skill.InstalledSkill) (skill.PackageUninstallOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoots(); err != nil {
		return skill.PackageUninstallOperation{}, err
	}
	destination, err := safeManagedJoin(s.installedRoot, value.PackageRelativePath)
	if err != nil {
		return skill.PackageUninstallOperation{}, err
	}
	info, err := os.Lstat(destination)
	if os.IsNotExist(err) {
		return skill.PackageUninstallOperation{InstalledPath: destination}, nil
	}
	if err != nil {
		return skill.PackageUninstallOperation{}, fmt.Errorf("inspect installed Skill before uninstall: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return skill.PackageUninstallOperation{}, fmt.Errorf("refuse to uninstall an unsafe Skill package path")
	}
	if err := requireManagedDirectoryPath(s.installedRoot, destination); err != nil {
		return skill.PackageUninstallOperation{}, fmt.Errorf("refuse to uninstall a Skill through an unsafe directory chain")
	}
	if err := ctx.Err(); err != nil {
		return skill.PackageUninstallOperation{}, err
	}
	archived, err := s.allocateArchiveDirectory("uninstalled", value.Manifest.ID, value.Manifest.Version, value.PackageHash)
	if err != nil {
		return skill.PackageUninstallOperation{}, err
	}
	if err := os.Rename(destination, archived); err != nil {
		return skill.PackageUninstallOperation{}, fmt.Errorf("archive uninstalled Skill package: %w", err)
	}
	return skill.PackageUninstallOperation{Moved: true, InstalledPath: destination, ArchivedPath: archived}, nil
}

// ReadResource reads from the exact package captured by a Run. It prefers the
// installed content after verifying the full package hash, then falls back to
// the immutable source archive captured at installation time. Host paths are
// never returned to the caller.
func (s *FilePackageStore) ReadResource(ctx context.Context, selected skill.RunSkill, resourcePath string, maxBytes int) (skill.ResourceContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoots(); err != nil {
		return skill.ResourceContent{}, err
	}
	if err := skill.ValidateResourcePath(resourcePath); err != nil {
		return skill.ResourceContent{}, err
	}
	if maxBytes < 1 || maxBytes > skill.MaxResourceReadBytes {
		return skill.ResourceContent{}, fmt.Errorf("Skill resource read limit is invalid")
	}

	if loaded, err := NewCatalog(s.installedRoot).Load(ctx, selected.PackagePath, selected.PackageHash); err == nil {
		if loaded.Skill.ContentHash != selected.ContentHash || loaded.Skill.ManifestHash != selected.ManifestHash {
			return skill.ResourceContent{}, fmt.Errorf("installed Skill provenance does not match the Run snapshot")
		}
		packageRoot, joinErr := safeManagedJoin(s.installedRoot, selected.PackagePath)
		if joinErr != nil {
			return skill.ResourceContent{}, joinErr
		}
		return readResourceAt(ctx, packageRoot, resourcePath, maxBytes)
	} else if ctx.Err() != nil {
		return skill.ResourceContent{}, ctx.Err()
	}

	if selected.SourceHash == "" || selected.SourceArchive == "" {
		return skill.ResourceContent{}, fmt.Errorf("snapshotted Skill package is no longer installed and has no archived source")
	}
	archive, err := safeManagedJoin(s.backupRoot, selected.SourceArchive)
	if err != nil {
		return skill.ResourceContent{}, err
	}
	if err := requireManagedDirectoryPath(s.backupRoot, filepath.Dir(archive)); err != nil {
		return skill.ResourceContent{}, fmt.Errorf("Skill source archive directory is unsafe: %w", err)
	}
	actualHash, err := hashRegularFileContext(ctx, archive, maxArchiveBytes)
	if err != nil || actualHash != selected.SourceHash {
		return skill.ResourceContent{}, fmt.Errorf("Skill source archive failed integrity validation")
	}
	stage, err := os.MkdirTemp(s.stagingRoot, "resource-*")
	if err != nil {
		return skill.ResourceContent{}, fmt.Errorf("create Skill resource staging directory: %w", err)
	}
	defer func() { _ = removeManagedTree(s.stagingRoot, stage) }()
	extracted := filepath.Join(stage, "extracted")
	if err := extractPackageZIP(ctx, archive, extracted); err != nil {
		return skill.ResourceContent{}, fmt.Errorf("extract archived Skill source: %w", err)
	}
	packageRoot, err := locateExtractedPackage(extracted)
	if err != nil {
		return skill.ResourceContent{}, err
	}
	instructions, err := readBoundedRegular(filepath.Join(packageRoot, filepath.FromSlash(selected.Manifest.Entry)), maxSkillBytes)
	if err != nil {
		return skill.ResourceContent{}, fmt.Errorf("verify archived SKILL.md: %w", err)
	}
	contentHash := sha256.Sum256(instructions)
	if hex.EncodeToString(contentHash[:]) != selected.ContentHash {
		return skill.ResourceContent{}, fmt.Errorf("archived SKILL.md does not match the Run snapshot")
	}
	return readResourceAt(ctx, packageRoot, resourcePath, maxBytes)
}

func readResourceAt(ctx context.Context, packageRoot, resourcePath string, maxBytes int) (skill.ResourceContent, error) {
	if err := ctx.Err(); err != nil {
		return skill.ResourceContent{}, err
	}
	if err := requireSafeDirectory(packageRoot); err != nil {
		return skill.ResourceContent{}, fmt.Errorf("Skill package root is unsafe")
	}
	target, err := safeManagedJoin(packageRoot, resourcePath)
	if err != nil {
		return skill.ResourceContent{}, err
	}
	parent := filepath.Dir(target)
	if parent != packageRoot {
		if err := requireManagedDirectoryPath(packageRoot, parent); err != nil {
			return skill.ResourceContent{}, fmt.Errorf("Skill resource directory is unsafe: %w", err)
		}
	}
	info, err := os.Lstat(target)
	if err != nil {
		return skill.ResourceContent{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maxSingleFile {
		return skill.ResourceContent{}, fmt.Errorf("Skill resource is not a bounded regular file")
	}
	input, err := os.Open(target)
	if err != nil {
		return skill.ResourceContent{}, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) {
		return skill.ResourceContent{}, fmt.Errorf("Skill resource changed before it could be read")
	}
	contents, err := io.ReadAll(&contextReader{ctx: ctx, reader: io.LimitReader(input, int64(maxBytes)+1)})
	if err != nil {
		return skill.ResourceContent{}, err
	}
	truncated := len(contents) > maxBytes
	if truncated {
		contents = contents[:maxBytes]
		for len(contents) > 0 && !utf8.Valid(contents) {
			contents = contents[:len(contents)-1]
		}
	}
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return skill.ResourceContent{}, fmt.Errorf("Skill resource is not supported UTF-8 text")
	}
	return skill.ResourceContent{Path: resourcePath, Content: contents, OriginalBytes: info.Size(), Truncated: truncated}, nil
}

func (s *FilePackageStore) RollbackUninstall(_ context.Context, operation skill.PackageUninstallOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !operation.Moved {
		return nil
	}
	if err := s.ensureRoots(); err != nil {
		return err
	}
	if err := requireManagedPath(s.installedRoot, operation.InstalledPath); err != nil {
		return err
	}
	if err := requireManagedDirectoryPath(s.backupRoot, operation.ArchivedPath); err != nil {
		return err
	}
	if _, err := os.Lstat(operation.InstalledPath); !os.IsNotExist(err) {
		return fmt.Errorf("cannot restore uninstalled Skill because its destination is no longer empty")
	}
	if err := os.MkdirAll(filepath.Dir(operation.InstalledPath), 0o700); err != nil {
		return err
	}
	if err := os.Rename(operation.ArchivedPath, operation.InstalledPath); err != nil {
		return fmt.Errorf("restore uninstalled Skill package: %w", err)
	}
	return nil
}

func (s *FilePackageStore) ensureRoots() error {
	roots := map[string]string{"installed": s.installedRoot, "staging": s.stagingRoot, "backup": s.backupRoot}
	for label, root := range roots {
		if root == "" || root == "." {
			return fmt.Errorf("Skill %s root is not configured", label)
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return fmt.Errorf("create Skill %s root: %w", label, err)
		}
		if err := requireSafeDirectory(root); err != nil {
			return fmt.Errorf("Skill %s root is unsafe", label)
		}
	}
	labels := []string{"installed", "staging", "backup"}
	for left := 0; left < len(labels); left++ {
		for right := left + 1; right < len(labels); right++ {
			leftRoot, rightRoot := roots[labels[left]], roots[labels[right]]
			if containsPath(leftRoot, rightRoot) || containsPath(rightRoot, leftRoot) {
				return fmt.Errorf("Skill %s and %s roots must be separate", labels[left], labels[right])
			}
		}
	}
	return nil
}

func (s *FilePackageStore) allocateArchiveDirectory(kind, id, version, hash string) (string, error) {
	relative := filepath.ToSlash(filepath.Join(kind, id, version))
	if err := ensureManagedDirectory(s.backupRoot, relative); err != nil {
		return "", err
	}
	parent, err := safeManagedJoin(s.backupRoot, relative)
	if err != nil {
		return "", err
	}
	stamp := s.now().Format("20060102T150405.000000000Z")
	temporary, err := os.MkdirTemp(parent, stamp+"-"+shortHash(hash)+"-*")
	if err != nil {
		return "", err
	}
	if err := os.Remove(temporary); err != nil {
		return "", err
	}
	return temporary, nil
}

func validateArchiveSource(source string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect Skill ZIP source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxArchiveBytes {
		return fmt.Errorf("Skill ZIP source must be a regular file no larger than %d bytes", maxArchiveBytes)
	}
	if !strings.EqualFold(filepath.Ext(source), ".zip") {
		return fmt.Errorf("Skill archive must use the .zip extension")
	}
	return nil
}

type archiveEntry struct {
	file      *zip.File
	relative  string
	directory bool
}

func extractPackageZIP(ctx context.Context, source, destination string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return fmt.Errorf("open Skill ZIP: %w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("Skill ZIP contains an invalid number of entries")
	}
	entries := make([]archiveEntry, 0, len(reader.File))
	seen := map[string]bool{}
	var total uint64
	files := 0
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		directory := file.FileInfo().IsDir()
		relative, err := normalizeArchiveEntry(file.Name, directory)
		if err != nil {
			return err
		}
		key := strings.ToLower(relative)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("Skill ZIP contains duplicate path %q", file.Name)
		}
		seen[key] = directory
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || !directory && !mode.IsRegular() {
			return fmt.Errorf("Skill ZIP contains a symbolic link or non-regular entry")
		}
		if file.Flags&0x1 != 0 {
			return fmt.Errorf("encrypted Skill ZIP entries are not supported")
		}
		if !directory {
			files++
			if files > maxPackageFiles || file.UncompressedSize64 > maxSingleFile {
				return fmt.Errorf("Skill ZIP exceeds file count or single-file size limits")
			}
			total += file.UncompressedSize64
			if total > maxPackageBytes {
				return fmt.Errorf("Skill ZIP exceeds the uncompressed size limit")
			}
		}
		entries = append(entries, archiveEntry{file: file, relative: relative, directory: directory})
	}
	if files == 0 {
		return fmt.Errorf("Skill ZIP contains no files")
	}
	for key, directory := range seen {
		parts := strings.Split(key, "/")
		for index := 1; index < len(parts); index++ {
			if parentDirectory, exists := seen[strings.Join(parts[:index], "/")]; exists && !parentDirectory {
				return fmt.Errorf("Skill ZIP path collides with a file")
			}
		}
		if !directory {
			prefix := key + "/"
			for other := range seen {
				if strings.HasPrefix(other, prefix) {
					return fmt.Errorf("Skill ZIP file path collides with a directory")
				}
			}
		}
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := safeManagedJoin(destination, entry.relative)
		if err != nil {
			return err
		}
		if entry.directory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		input, err := entry.file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return err
		}
		written, copyErr := io.Copy(output, &contextReader{ctx: ctx, reader: io.LimitReader(input, maxSingleFile+1)})
		closeOut, closeIn := output.Close(), input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOut != nil {
			return closeOut
		}
		if closeIn != nil {
			return closeIn
		}
		if written > maxSingleFile || uint64(written) != entry.file.UncompressedSize64 {
			return fmt.Errorf("Skill ZIP entry size does not match its declaration")
		}
	}
	return nil
}

func normalizeArchiveEntry(name string, directory bool) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("Skill ZIP contains an unsafe path")
	}
	trimmed := name
	if directory {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	if trimmed == "" || path.Clean(trimmed) != trimmed || strings.HasPrefix(trimmed, "../") || trimmed == ".." {
		return "", fmt.Errorf("Skill ZIP contains a non-canonical or escaping path")
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) > maxPackageDepth {
		return "", fmt.Errorf("Skill ZIP path is too deep")
	}
	for _, part := range parts {
		if !safeWindowsPathComponent(part) {
			return "", fmt.Errorf("Skill ZIP contains a path unsupported on Windows")
		}
	}
	return trimmed, nil
}

func safeWindowsPathComponent(value string) bool {
	if value == "" || len([]rune(value)) > 255 || strings.ContainsAny(value, `<>:"|?*`) || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	reserved := map[string]struct{}{"CON": {}, "PRN": {}, "AUX": {}, "NUL": {}, "CLOCK$": {}}
	if _, exists := reserved[base]; exists {
		return false
	}
	for index := 1; index <= 9; index++ {
		if base == fmt.Sprintf("COM%d", index) || base == fmt.Sprintf("LPT%d", index) {
			return false
		}
	}
	return true
}

func locateExtractedPackage(root string) (string, error) {
	if hasPackageMarker(root) {
		return root, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("Skill ZIP must contain package files at its root or inside one wrapper directory")
	}
	candidate := filepath.Join(root, entries[0].Name())
	if !hasPackageMarker(candidate) {
		return "", fmt.Errorf("Skill ZIP does not contain skill.yaml or SKILL.md at the package root")
	}
	return candidate, nil
}

func hasPackageMarker(root string) bool {
	for _, name := range []string{"skill.yaml", "SKILL.md"} {
		if info, err := os.Lstat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return true
		}
	}
	return false
}

func copyPackageTree(ctx context.Context, source, destination string) error {
	if err := requireSafeDirectory(source); err != nil {
		return fmt.Errorf("Skill folder source is missing or unsafe")
	}
	files, directories := 0, 0
	total := int64(0)
	seenPaths := make(map[string]struct{})
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill folders cannot contain symbolic links")
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o700)
		}
		if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || len(strings.Split(filepath.ToSlash(relative), "/")) > maxPackageDepth {
			return fmt.Errorf("Skill folder contains an invalid path")
		}
		relativeSlash := filepath.ToSlash(relative)
		for _, component := range strings.Split(relativeSlash, "/") {
			if !safeWindowsPathComponent(component) {
				return fmt.Errorf("Skill folder contains a path unsupported on Windows")
			}
		}
		caseKey := strings.ToLower(relativeSlash)
		if _, duplicate := seenPaths[caseKey]; duplicate {
			return fmt.Errorf("Skill folder contains case-insensitive duplicate paths")
		}
		seenPaths[caseKey] = struct{}{}
		target, err := safeManagedJoin(destination, relativeSlash)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories++
			if directories > maxArchiveEntries {
				return fmt.Errorf("Skill folder contains too many directories")
			}
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Skill folders can contain only regular files")
		}
		files++
		total += info.Size()
		if files > maxPackageFiles || info.Size() > maxSingleFile || total > maxPackageBytes {
			return fmt.Errorf("Skill folder exceeds file count or size limits")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return copyRegularFile(ctx, current, target, maxSingleFile)
	})
}

func writePackageZIP(ctx context.Context, root, destination string) error {
	files := make([]string, 0)
	if err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == root || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(current)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("staged Skill contains an unsafe file")
		}
		files = append(files, current)
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(files)
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(output)
	for _, filename := range files {
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			archive.Close()
			output.Close()
			return err
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(relative), Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		header.SetMode(0o600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			archive.Close()
			output.Close()
			return err
		}
		input, err := os.Open(filename)
		if err != nil {
			archive.Close()
			output.Close()
			return err
		}
		_, copyErr := io.Copy(writer, &contextReader{ctx: ctx, reader: input})
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			archive.Close()
			output.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
	}
	if err := archive.Close(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func copyRegularFile(ctx context.Context, source, destination string, maximum int64) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return fmt.Errorf("source is not a bounded regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) {
		return fmt.Errorf("source file changed before it could be copied")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, &contextReader{ctx: ctx, reader: io.LimitReader(input, maximum+1)})
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != info.Size() || written > maximum {
		return fmt.Errorf("source file changed while being copied")
	}
	return nil
}

func persistArchive(ctx context.Context, source, destination, expectedHash string) (bool, error) {
	if existing, err := hashRegularFileContext(ctx, destination, maxArchiveBytes); err == nil {
		if existing != expectedHash {
			return false, fmt.Errorf("existing archived Skill source failed integrity validation")
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := requireSafeDirectory(filepath.Dir(destination)); err != nil {
		return false, fmt.Errorf("Skill source archive directory is unsafe")
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".archive-*.tmp")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		_ = os.Remove(temporaryPath)
		return false, err
	}
	input, err := os.Open(source)
	if err != nil {
		temporary.Close()
		_ = os.Remove(temporaryPath)
		return false, err
	}
	_, copyErr := io.Copy(temporary, &contextReader{ctx: ctx, reader: io.LimitReader(input, maxArchiveBytes+1)})
	closeIn, closeOut := input.Close(), temporary.Close()
	if copyErr != nil || closeIn != nil || closeOut != nil {
		_ = os.Remove(temporaryPath)
		if copyErr != nil {
			return false, copyErr
		}
		if closeIn != nil {
			return false, closeIn
		}
		return false, closeOut
	}
	actual, err := hashRegularFileContext(ctx, temporaryPath, maxArchiveBytes)
	if err != nil || actual != expectedHash {
		_ = os.Remove(temporaryPath)
		return false, fmt.Errorf("archived Skill source hash mismatch")
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		_ = os.Remove(temporaryPath)
		if existing, hashErr := hashRegularFileContext(ctx, destination, maxArchiveBytes); hashErr == nil && existing == expectedHash {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func hashRegularFileContext(ctx context.Context, filename string, maximum int64) (string, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum {
		return "", fmt.Errorf("file is not a bounded regular file")
	}
	input, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) {
		return "", fmt.Errorf("file changed before it could be hashed")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, &contextReader{ctx: ctx, reader: io.LimitReader(input, maximum+1)})
	if err != nil || written != info.Size() || written > maximum {
		return "", fmt.Errorf("hash bounded file: size changed or read failed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func safeManagedJoin(root, relative string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid managed Skill path")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := requireManagedPath(root, target); err != nil {
		return "", err
	}
	return target, nil
}

func ensureManagedDirectory(root, relative string) error {
	if err := requireSafeDirectory(root); err != nil {
		return fmt.Errorf("managed root is unsafe")
	}
	relative = filepath.ToSlash(strings.TrimSpace(relative))
	if relative == "" || relative == "." || filepath.IsAbs(relative) {
		return fmt.Errorf("invalid managed directory path")
	}
	current := root
	for _, component := range strings.Split(relative, "/") {
		if !safeWindowsPathComponent(component) || component == "." || component == ".." {
			return fmt.Errorf("invalid managed directory component")
		}
		current = filepath.Join(current, component)
		if err := requireManagedPath(root, current); err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed directory contains an unsafe link or file")
		}
	}
	return nil
}

func requireManagedPath(root, target string) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("managed Skill path escapes or aliases its root")
	}
	return nil
}

func requireManagedDirectoryPath(root, target string) error {
	if err := requireManagedPath(root, target); err != nil {
		return err
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil {
		return err
	}
	current := rootAbsolute
	for _, component := range strings.Split(filepath.Clean(relative), string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed directory chain is missing or unsafe")
		}
	}
	return nil
}

func removeManagedTree(root, target string) error {
	if err := requireManagedPath(root, target); err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func containsPath(parent, child string) bool {
	parentAbsolute, errParent := filepath.Abs(parent)
	childAbsolute, errChild := filepath.Abs(child)
	if errParent != nil || errChild != nil {
		return false
	}
	relative, err := filepath.Rel(parentAbsolute, childAbsolute)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative))
}

func shortHash(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	if value == "" {
		return "unknown"
	}
	return value
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
