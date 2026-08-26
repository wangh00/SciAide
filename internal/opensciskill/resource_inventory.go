package opensciskill

import (
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Service) listPackageResources(info Info) ([]ResourceEntry, error) {
	result := make([]ResourceEntry, 0, max(0, info.FileCount-1))
	visit := func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		rel := filepath.ToSlash(file)
		if info.bundled {
			if rel == info.root {
				return nil
			}
			rel = strings.TrimPrefix(rel, strings.TrimSuffix(info.root, "/")+"/")
		} else {
			rel = strings.TrimPrefix(rel, "./")
		}
		if rel == "SKILL.md" || ValidateResourcePath(rel) != nil {
			return nil
		}
		metadata, err := entry.Info()
		if err != nil || !metadata.Mode().IsRegular() {
			return err
		}
		kind := "other"
		switch strings.Split(rel, "/")[0] {
		case "references":
			kind = "reference"
		case "assets":
			kind = "asset"
		case "scripts":
			kind = "script"
		}
		result = append(result, ResourceEntry{Path: rel, Kind: kind, Size: metadata.Size(), Text: textResourceExtension(rel), MediaType: mime.TypeByExtension(strings.ToLower(path.Ext(rel)))})
		return nil
	}
	if info.bundled {
		if err := fs.WalkDir(bundled, info.root, visit); err != nil {
			return nil, err
		}
	} else {
		root, err := os.OpenRoot(filepath.FromSlash(info.root))
		if err != nil {
			return nil, err
		}
		defer root.Close()
		if err := fs.WalkDir(root.FS(), ".", visit); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (s *Service) listPackageSource(info Info) ([]SourceEntry, error) {
	result := make([]SourceEntry, 0, info.FileCount+4)
	directories := map[string]struct{}{}
	visit := func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(file)
		if info.bundled {
			if rel == info.root {
				return nil
			}
			rel = strings.TrimPrefix(rel, strings.TrimSuffix(info.root, "/")+"/")
		} else {
			rel = strings.TrimPrefix(rel, "./")
		}
		if rel == "." || rel == "" {
			return nil
		}
		if ValidateResourcePath(rel) != nil {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			directories[rel] = struct{}{}
			return nil
		}
		metadata, err := entry.Info()
		if err != nil || !metadata.Mode().IsRegular() {
			return err
		}
		text := textResourceExtension(rel) && metadata.Size() <= MaxSkillMarkdownSize
		mediaType := mime.TypeByExtension(strings.ToLower(path.Ext(rel)))
		if text && mediaType == "" {
			mediaType = "text/plain; charset=utf-8"
		}
		result = append(result, SourceEntry{Path: rel, Kind: "file", Size: metadata.Size(), Text: text, MediaType: mediaType})
		return nil
	}
	if info.bundled {
		if err := fs.WalkDir(bundled, info.root, visit); err != nil {
			return nil, err
		}
	} else {
		root, err := os.OpenRoot(filepath.FromSlash(info.root))
		if err != nil {
			return nil, err
		}
		defer root.Close()
		if err := fs.WalkDir(root.FS(), ".", visit); err != nil {
			return nil, err
		}
	}
	for directory := range directories {
		result = append(result, SourceEntry{Path: directory, Kind: "directory"})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.Split(result[i].Path, "/"), strings.Split(result[j].Path, "/")
		limit := min(len(left), len(right))
		for index := 0; index < limit; index++ {
			if left[index] == right[index] {
				continue
			}
			return left[index] < right[index]
		}
		if len(left) != len(right) {
			return len(left) < len(right)
		}
		return result[i].Kind == "directory" && result[j].Kind != "directory"
	})
	return result, nil
}

func textResourceExtension(value string) bool {
	switch strings.ToLower(path.Ext(value)) {
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".xml", ".html", ".htm", ".css", ".js", ".jsx", ".ts", ".tsx", ".py", ".r", ".go", ".rs", ".java", ".c", ".cc", ".cpp", ".h", ".hpp", ".sh", ".bash", ".zsh", ".ps1", ".bat", ".cmd", ".sql", ".tex", ".sty", ".bib", ".bst", ".mplstyle", ".ini", ".cfg", ".conf", ".env", ".example":
		return true
	default:
		return path.Ext(value) == ""
	}
}
