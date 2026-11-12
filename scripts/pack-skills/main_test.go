package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "defaultskills", "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: fixture\n---\n完整科研资料\n")
	if err := os.WriteFile(filepath.Join(root, "defaultskills", "fixture", "SKILL.md"), body, 0600); err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"source":"fixture"}`)
	digest := sha256.Sum256(upstream)
	fileDigest := sha256.Sum256(body)
	manifest, _ := json.Marshal(map[string]any{"upstreamManifestSha256": hex.EncodeToString(digest[:]), "fileCount": 1, "files": []map[string]any{{"path": "fixture/SKILL.md", "size": len(body), "sha256": hex.EncodeToString(fileDigest[:])}}})
	for name, data := range map[string][]byte{"defaultskills.upstream.manifest.json": upstream, "defaultskills.manifest.json": manifest, "capability-audit.v1.json": []byte(`{"fixture":true}`)} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestArchiveIsDeterministicLosslessAndExcludesTestInventories(t *testing.T) {
	root := fixture(t)
	a, err := pack(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pack(root)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("non-deterministic archive", err)
	}
	z, err := zip.NewReader(bytes.NewReader(a), int64(len(a)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 2 {
		t.Fatal("test inventories shipped", len(z.File))
	}
	if err := run(root, false); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "defaultskills.zip"), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err == nil {
		t.Fatal("accepted stale archive")
	}
	if err := run(root, false); err != nil {
		t.Fatal("could not replace stale archive", err)
	}
	if err := run(root, true); err != nil {
		t.Fatal(err)
	}
}

func TestPackingRejectsSourceDriftWithoutReplacingLastGoodArchive(t *testing.T) {
	for _, mode := range []string{"changed", "extra", "missing", "symlink", "manifest_path", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			root := fixture(t)
			if err := run(root, false); err != nil {
				t.Fatal(err)
			}
			old, _ := os.ReadFile(filepath.Join(root, "defaultskills.zip"))
			file := filepath.Join(root, "defaultskills", "fixture", "SKILL.md")
			switch mode {
			case "changed":
				os.WriteFile(file, []byte("changed"), 0600)
			case "extra":
				os.WriteFile(filepath.Join(root, "defaultskills", "extra.txt"), []byte("extra"), 0600)
			case "missing":
				os.Remove(file)
			case "symlink":
				if err := os.Symlink(file, filepath.Join(root, "defaultskills", "link.md")); err != nil {
					t.Skip("symlink unavailable", err)
				}
			default:
				manifestFile := filepath.Join(root, "defaultskills.manifest.json")
				data, _ := os.ReadFile(manifestFile)
				var m map[string]any
				json.Unmarshal(data, &m)
				if mode == "manifest_path" {
					m["files"].([]any)[0].(map[string]any)["path"] = "../outside"
				} else {
					entries := m["files"].([]any)
					m["files"] = append(entries, entries[0])
					m["fileCount"] = 2
				}
				data, _ = json.Marshal(m)
				os.WriteFile(manifestFile, data, 0600)
			}
			if err := run(root, false); err == nil {
				t.Fatal("source drift accepted")
			}
			now, _ := os.ReadFile(filepath.Join(root, "defaultskills.zip"))
			if !bytes.Equal(old, now) {
				t.Fatal("failed pack replaced good archive")
			}
		})
	}
}
