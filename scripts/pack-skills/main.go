// pack-skills creates the lossless, deterministic production Skill archive.
// Raw sources and the two provenance inventories remain versioned for review.
package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"
)

func main() {
	root := flag.String("root", "internal/opensciskill", "directory containing defaultskills and inventories")
	check := flag.Bool("check", false, "verify the committed archive without updating it")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(root string) ([]byte, error) {
	var manifest struct {
		UpstreamSHA string `json:"upstreamManifestSha256"`
		FileCount   int    `json:"fileCount"`
		Files       []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
			SHA  string `json:"sha256"`
		} `json:"files"`
	}
	encoded, err := os.ReadFile(filepath.Join(root, "defaultskills.manifest.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return nil, err
	}
	upstream, err := os.ReadFile(filepath.Join(root, "defaultskills.upstream.manifest.json"))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(upstream)
	if hex.EncodeToString(digest[:]) != manifest.UpstreamSHA || len(manifest.Files) != manifest.FileCount {
		return nil, fmt.Errorf("Skill provenance inventory does not match; sync sources before packing")
	}
	inputs := make(map[string][]byte, len(manifest.Files)+1)
	for _, file := range manifest.Files {
		if !fs.ValidPath(file.Path) || file.Path == "." {
			return nil, fmt.Errorf("invalid inventory path %q", file.Path)
		}
		name := path.Join("defaultskills", file.Path)
		if _, exists := inputs[name]; exists {
			return nil, fmt.Errorf("duplicate inventory path %q", file.Path)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != file.Size || hex.EncodeToString(digest[:]) != file.SHA {
			return nil, fmt.Errorf("Skill source %q differs from inventory; sync sources before packing", name)
		}
		inputs[name] = data
	}
	count := 0
	if err := filepath.WalkDir(filepath.Join(root, "defaultskills"), func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Skill source symlink %q is not an immutable packaged file", file)
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		if _, exists := inputs[filepath.ToSlash(rel)]; !exists {
			return fmt.Errorf("Skill source %q is absent from inventory", rel)
		}
		count++
		return nil
	}); err != nil {
		return nil, err
	}
	if count != manifest.FileCount {
		return nil, fmt.Errorf("Skill source file count mismatch")
	}
	audit, err := os.ReadFile(filepath.Join(root, "capability-audit.v1.json"))
	if err != nil {
		return nil, err
	}
	inputs["capability-audit.v1.json"] = audit
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	w.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, flate.BestCompression) })
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		header.SetMode(0444)
		file, err := w.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(inputs[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func run(root string, check bool) error {
	data, err := pack(root)
	if err != nil {
		return err
	}
	output := filepath.Join(root, "defaultskills.zip")
	existing, readErr := os.ReadFile(output)
	if check {
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("Skill archive is stale; run go run ./scripts/pack-skills before building")
		}
	} else if readErr != nil || !bytes.Equal(existing, data) {
		file, err := os.CreateTemp(root, ".defaultskills-pack-*")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err := file.Write(data); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Rename(name, output); err != nil {
			return err
		}
	}
	fmt.Printf("Skill archive verified: %d bytes; raw files and provenance inventories preserved.\n", len(data))
	return nil
}
