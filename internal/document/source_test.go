package document

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceTextPreservesIndentationAcrossChunks(t *testing.T) {
	for _, ext := range []string{".py", ".R", ".jl", ".m", ".sql"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "analysis"+ext)
			text := "    # 中文源码\r\n" + strings.Repeat("    value = 42\n", 4000) + "\n    # end\n"
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			format, ok := FormatForName(path)
			if !ok || format != FormatText {
				t.Fatal(format, ok)
			}
			parsed, err := Parse(context.Background(), path, format)
			if err != nil {
				t.Fatal(err)
			}
			var recovered strings.Builder
			for _, unit := range parsed.Units {
				if unit.Kind != "source" {
					t.Fatal(unit.Kind)
				}
				recovered.WriteString(unit.Content)
			}
			if recovered.String() != text || len(parsed.Units) < 2 || parsed.Metadata["executionStatus"] != "not_executed" {
				t.Fatal("source changed or mislabeled")
			}
		})
	}
}

func TestSourceTextRejectsBinaryAndDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "analysis.py")
	text := "raise RuntimeError('must not execute')\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(context.Background(), path, FormatText); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{'M', 'Z', 0, 255}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(context.Background(), path, FormatText); err == nil {
		t.Fatal("binary accepted as source")
	}
	if _, ok := FormatForName("program.exe"); ok {
		t.Fatal("executable accepted")
	}
}
