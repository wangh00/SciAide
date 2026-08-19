package skillpkg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/skill"
	"gopkg.in/yaml.v3"
)

const (
	maxManifestBytes = 128 * 1024
	maxSkillBytes    = 512 * 1024
	maxPackageFiles  = 512
	maxPackageBytes  = 16 * 1024 * 1024
	maxSingleFile    = 2 * 1024 * 1024
	maxYAMLNodes     = 4096
	maxYAMLDepth     = 32
)

type Catalog struct {
	root string
	now  func() time.Time
}

func NewCatalog(root string) *Catalog {
	root = strings.TrimSpace(root)
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Catalog{root: root, now: func() time.Time { return time.Now().UTC() }}
}

func (c *Catalog) Discover(ctx context.Context) (skill.CatalogSnapshot, error) {
	if strings.TrimSpace(c.root) == "" {
		return skill.CatalogSnapshot{}, fmt.Errorf("Skill root is not configured")
	}
	if err := os.MkdirAll(c.root, 0o700); err != nil {
		return skill.CatalogSnapshot{}, fmt.Errorf("create Skill root: %w", err)
	}
	if err := requireSafeDirectory(c.root); err != nil {
		return skill.CatalogSnapshot{}, fmt.Errorf("inspect Skill root: %w", err)
	}
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return skill.CatalogSnapshot{}, fmt.Errorf("list Skill root: %w", err)
	}
	result := skill.CatalogSnapshot{Packages: []skill.Package{}, Diagnostics: []skill.Diagnostic{}, SeenPaths: []string{}}
	for _, idEntry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if strings.HasPrefix(idEntry.Name(), ".") {
			continue
		}
		idRelative := filepath.ToSlash(idEntry.Name())
		info, err := os.Lstat(filepath.Join(c.root, idEntry.Name()))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !skill.ValidID(idEntry.Name()) {
			result.Diagnostics = append(result.Diagnostics, skill.Diagnostic{PackageRelativePath: idRelative, Message: "invalid Skill id directory"})
			continue
		}
		versions, err := os.ReadDir(filepath.Join(c.root, idEntry.Name()))
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, skill.Diagnostic{PackageRelativePath: idRelative, Message: "cannot list Skill versions"})
			continue
		}
		for _, versionEntry := range versions {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if strings.HasPrefix(versionEntry.Name(), ".") {
				continue
			}
			relative := filepath.ToSlash(filepath.Join(idEntry.Name(), versionEntry.Name()))
			versionPath := filepath.Join(c.root, idEntry.Name(), versionEntry.Name())
			info, err := os.Lstat(versionPath)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !skill.ValidVersion(versionEntry.Name()) {
				result.Diagnostics = append(result.Diagnostics, skill.Diagnostic{PackageRelativePath: relative, Message: "invalid Skill version directory"})
				continue
			}
			result.SeenPaths = append(result.SeenPaths, relative)
			loaded, err := c.loadAt(ctx, relative)
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, skill.Diagnostic{PackageRelativePath: relative, Message: publicError(err)})
				continue
			}
			if loaded.Skill.Manifest.ID != idEntry.Name() || loaded.Skill.Manifest.Version != versionEntry.Name() {
				result.Diagnostics = append(result.Diagnostics, skill.Diagnostic{PackageRelativePath: relative, Message: "Skill manifest identity does not match its directory"})
				continue
			}
			result.Packages = append(result.Packages, loaded)
		}
	}
	sort.Strings(result.SeenPaths)
	sort.Slice(result.Packages, func(i, j int) bool {
		return result.Packages[i].Skill.PackageRelativePath < result.Packages[j].Skill.PackageRelativePath
	})
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		return result.Diagnostics[i].PackageRelativePath < result.Diagnostics[j].PackageRelativePath
	})
	return result, nil
}

func (c *Catalog) Load(ctx context.Context, packageRelativePath, expectedPackageHash string) (skill.Package, error) {
	loaded, err := c.loadAt(ctx, packageRelativePath)
	if err != nil {
		return skill.Package{}, err
	}
	if strings.TrimSpace(expectedPackageHash) == "" || !strings.EqualFold(loaded.Skill.PackageHash, expectedPackageHash) {
		return skill.Package{}, fmt.Errorf("Skill package changed after it was enabled; refresh and review it before use")
	}
	return loaded, nil
}

// InspectDirectory validates a staged package directory without trusting its
// source path. The manifest identity determines the canonical installed
// relative path; callers must copy untrusted folders into an isolated staging
// root before using this method.
func (c *Catalog) InspectDirectory(ctx context.Context, directory string) (skill.Package, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return skill.Package{}, fmt.Errorf("Skill package directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return skill.Package{}, err
	}
	if err := requireSafeDirectory(absolute); err != nil {
		return skill.Package{}, fmt.Errorf("Skill package directory is missing or unsafe")
	}
	return c.loadDirectory(ctx, absolute, "")
}

func (c *Catalog) loadAt(ctx context.Context, packageRelativePath string) (skill.Package, error) {
	relative, packagePath, err := c.resolvePackagePath(packageRelativePath)
	if err != nil {
		return skill.Package{}, err
	}
	for _, directory := range []string{c.root, filepath.Dir(packagePath), packagePath} {
		if err := requireSafeDirectory(directory); err != nil {
			return skill.Package{}, fmt.Errorf("Skill package directory is missing or unsafe")
		}
	}
	return c.loadDirectory(ctx, packagePath, relative)
}

func (c *Catalog) loadDirectory(ctx context.Context, packagePath, relative string) (skill.Package, error) {
	manifestBytes, err := readBoundedRegular(filepath.Join(packagePath, "skill.yaml"), maxManifestBytes)
	if err != nil {
		return skill.Package{}, fmt.Errorf("read skill.yaml: %w", err)
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return skill.Package{}, err
	}
	if relative == "" {
		relative = filepath.ToSlash(filepath.Join(manifest.ID, manifest.Version))
	}
	contentBytes, err := readBoundedRegular(filepath.Join(packagePath, manifest.Entry), maxSkillBytes)
	if err != nil {
		return skill.Package{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	if !utf8.Valid(contentBytes) || bytes.IndexByte(contentBytes, 0) >= 0 || strings.TrimSpace(string(contentBytes)) == "" {
		return skill.Package{}, fmt.Errorf("SKILL.md must be non-empty UTF-8 text without NUL bytes")
	}
	if len([]rune(string(contentBytes))) > manifest.Context.MaxTokens {
		return skill.Package{}, fmt.Errorf("SKILL.md exceeds its declared context.max_tokens budget")
	}
	packageHash, err := hashPackage(ctx, packagePath)
	if err != nil {
		return skill.Package{}, err
	}
	now := c.now()
	return skill.Package{
		Skill: skill.InstalledSkill{
			Manifest:            manifest,
			PackageRelativePath: relative,
			ManifestHash:        hashBytes(manifestBytes),
			ContentHash:         hashBytes(contentBytes),
			PackageHash:         packageHash,
			Integrity:           skill.IntegrityValid,
			Availability:        skill.AvailabilityUnavailable,
			InstalledAt:         now,
			UpdatedAt:           now,
		},
		Instructions: string(contentBytes),
	}, nil
}

func requireSafeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("directory is missing or is a symbolic link")
	}
	return nil
}

func (c *Catalog) resolvePackagePath(value string) (string, string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "." || value == "" || filepath.IsAbs(value) {
		return "", "", fmt.Errorf("invalid Skill package path")
	}
	parts := strings.FieldsFunc(filepath.ToSlash(value), func(r rune) bool { return r == '/' })
	if len(parts) != 2 || !skill.ValidID(parts[0]) || !skill.ValidVersion(parts[1]) {
		return "", "", fmt.Errorf("Skill package path must be <id>/<version>")
	}
	relative := filepath.Join(parts[0], parts[1])
	target := filepath.Join(c.root, relative)
	rootAbsolute, err := filepath.Abs(c.root)
	if err != nil {
		return "", "", err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("Skill package path escapes the Skill root")
	}
	return filepath.ToSlash(relative), targetAbsolute, nil
}

func decodeManifest(contents []byte) (skill.Manifest, error) {
	var document yaml.Node
	nodeDecoder := yaml.NewDecoder(bytes.NewReader(contents))
	if err := nodeDecoder.Decode(&document); err != nil {
		return skill.Manifest{}, fmt.Errorf("decode skill.yaml: %w", err)
	}
	if len(document.Content) != 1 {
		return skill.Manifest{}, fmt.Errorf("skill.yaml must contain one document")
	}
	nodes := 0
	if err := validateYAMLNode(document.Content[0], 0, &nodes); err != nil {
		return skill.Manifest{}, err
	}
	var extra yaml.Node
	if err := nodeDecoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return skill.Manifest{}, fmt.Errorf("skill.yaml cannot contain multiple documents")
		}
		return skill.Manifest{}, fmt.Errorf("decode trailing skill.yaml document: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var manifest skill.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return skill.Manifest{}, fmt.Errorf("decode strict skill.yaml: %w", err)
	}
	manifest = skill.NormalizeManifest(manifest)
	if err := skill.ValidateManifest(manifest); err != nil {
		return skill.Manifest{}, err
	}
	return manifest, nil
}

func validateYAMLNode(node *yaml.Node, depth int, count *int) error {
	if node == nil {
		return nil
	}
	(*count)++
	if *count > maxYAMLNodes || depth > maxYAMLDepth {
		return fmt.Errorf("skill.yaml is too complex")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("skill.yaml aliases and anchors are not allowed")
	}
	switch node.Kind {
	case yaml.MappingNode, yaml.SequenceNode, yaml.ScalarNode:
	default:
		return fmt.Errorf("skill.yaml contains an unsupported YAML node")
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func readBoundedRegular(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("file is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("file size is outside the allowed range")
	}
	input, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed before it could be read")
	}
	contents, err := io.ReadAll(io.LimitReader(input, maximum+1))
	if err != nil || int64(len(contents)) != info.Size() || int64(len(contents)) > maximum {
		return nil, fmt.Errorf("file changed while it was being read")
	}
	return contents, nil
}

type packageFile struct {
	relative string
	path     string
	size     int64
	info     fs.FileInfo
}

func hashPackage(ctx context.Context, root string) (string, error) {
	files := make([]packageFile, 0)
	total := int64(0)
	directories := 0
	seenPaths := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill packages cannot contain symbolic links")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("Skill package contains an invalid path")
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		parts := strings.Split(relative, "/")
		if filepath.IsAbs(relative) || strings.HasPrefix(relative, "../") || len(parts) > maxPackageDepth {
			return fmt.Errorf("Skill package contains an invalid or overly deep path")
		}
		for _, component := range parts {
			if !safeWindowsPathComponent(component) {
				return fmt.Errorf("Skill package contains a path unsupported on Windows")
			}
		}
		caseKey := strings.ToLower(relative)
		if _, duplicate := seenPaths[caseKey]; duplicate {
			return fmt.Errorf("Skill package contains case-insensitive duplicate paths")
		}
		seenPaths[caseKey] = struct{}{}
		if info.IsDir() {
			directories++
			if directories > maxArchiveEntries {
				return fmt.Errorf("Skill package contains too many directories")
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Skill packages can contain only regular files")
		}
		if info.Size() > maxSingleFile {
			return fmt.Errorf("Skill package contains a file larger than %d bytes", maxSingleFile)
		}
		total += info.Size()
		if total > maxPackageBytes || len(files) >= maxPackageFiles {
			return fmt.Errorf("Skill package exceeds file count or total size limit")
		}
		files = append(files, packageFile{relative: relative, path: path, size: info.Size(), info: info})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("inspect Skill package: %w", err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("Skill package is empty")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	hash := sha256.New()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		input, err := os.Open(file.path)
		if err != nil {
			return "", err
		}
		opened, err := input.Stat()
		if err != nil || !opened.Mode().IsRegular() || opened.Size() != file.size || !os.SameFile(file.info, opened) {
			input.Close()
			return "", fmt.Errorf("Skill package changed while it was being hashed")
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", file.relative, file.size)
		written, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: io.LimitReader(input, maxSingleFile+1)})
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil || written != file.size {
			return "", fmt.Errorf("Skill package changed while it was being hashed")
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func publicError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}
