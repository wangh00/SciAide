// Run from the repository root: go run ./examples/research-cases/tools/check_materials.go
// Read-only verification through SciAide's real document parser, not an E2E run.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/document"
)

func main() {
	root := filepath.Join("examples", "research-cases")
	var results []map[string]any
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "__pycache__" {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.Contains(filepath.ToSlash(path), "/materials/") {
			return nil
		}
		format, ok := document.FormatForName(path)
		if !ok {
			return fmt.Errorf("unrecognized material: %s", path)
		}
		parsed, err := document.Parse(context.Background(), path, format)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if parsed.Truncated || parsed.ExtractedRunes == 0 {
			return fmt.Errorf("incomplete material: %s", path)
		}
		results = append(results, map[string]any{"path": filepath.ToSlash(path), "units": len(parsed.Units),
			"runes": parsed.ExtractedRunes, "truncated": parsed.Truncated, "metadata": parsed.Metadata})
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(results); err != nil {
		panic(err)
	}
}
