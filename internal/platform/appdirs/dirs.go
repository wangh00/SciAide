package appdirs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Dirs struct {
	Root       string
	Config     string
	Data       string
	Cache      string
	Logs       string
	Skills     string
	MCP        string
	Backups    string
	Workspaces string
	Trash      string
}

func Resolve(product string) (Dirs, error) {
	if product == "" {
		return Dirs{}, fmt.Errorf("product name is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("resolve user home: %w", err)
	}
	return ResolveUnder(filepath.Join(home, ".sciaide")), nil
}

func ResolveUnder(root string) Dirs {
	dirs := Dirs{
		Root:    root,
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "logs"),
		Skills:  filepath.Join(root, "skills"),
		MCP:     filepath.Join(root, "mcp"),
		Backups: filepath.Join(root, "backups"),
	}
	dirs.Workspaces = filepath.Join(dirs.Data, "workspaces")
	dirs.Trash = filepath.Join(dirs.Backups, "trash")
	return dirs
}

func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Root, d.Config, d.Data, d.Cache, d.Logs, d.Skills, d.MCP, d.Backups, d.Workspaces, d.Trash} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create application directory %q: %w", dir, err)
		}
	}
	return nil
}

type LegacyMigrationResult struct {
	Migrated bool   `json:"migrated"`
	Source   string `json:"source,omitempty"`
	At       string `json:"at"`
}

// MigrateLegacy copies the P0/P1 AppData layout into ~/.sciaide once. The
// source is deliberately retained as a rollback copy and existing target files
// are never overwritten.
func MigrateLegacy(product string, target Dirs) (LegacyMigrationResult, error) {
	result := LegacyMigrationResult{At: time.Now().UTC().Format(time.RFC3339Nano)}
	marker := filepath.Join(target.Config, "migration-appdata-v1.json")
	if _, err := os.Stat(marker); err == nil {
		return result, nil
	} else if !os.IsNotExist(err) {
		return result, err
	}
	localRoot := ""
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		localRoot = filepath.Join(local, product)
	}
	if localRoot == "" {
		return result, writeMigrationMarker(marker, result)
	}
	legacyDB := filepath.Join(localRoot, "data", "sciaide.db")
	if _, err := os.Stat(legacyDB); os.IsNotExist(err) {
		return result, writeMigrationMarker(marker, result)
	} else if err != nil {
		return result, err
	}
	if _, err := os.Stat(filepath.Join(target.Data, "sciaide.db")); err == nil {
		return result, writeMigrationMarker(marker, result)
	} else if !os.IsNotExist(err) {
		return result, err
	}

	for _, mapping := range []struct{ source, target string }{
		{filepath.Join(localRoot, "data"), target.Data}, {filepath.Join(localRoot, "logs"), target.Logs},
		{filepath.Join(localRoot, "skills"), target.Skills}, {filepath.Join(localRoot, "mcp"), target.MCP},
		{filepath.Join(localRoot, "backups"), target.Backups},
	} {
		if err := copyTreeMissing(mapping.source, mapping.target); err != nil {
			return result, fmt.Errorf("migrate legacy %q: %w", mapping.source, err)
		}
	}
	if configRoot, err := os.UserConfigDir(); err == nil {
		if err := copyTreeMissing(filepath.Join(configRoot, product, "config"), target.Config); err != nil {
			return result, fmt.Errorf("migrate legacy config: %w", err)
		}
	}
	result.Migrated, result.Source = true, localRoot
	return result, writeMigrationMarker(marker, result)
}

func writeMigrationMarker(marker string, result LegacyMigrationResult) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(marker, data, 0o600); err != nil {
		return fmt.Errorf("write legacy migration marker: %w", err)
	}
	return nil
}

func copyTreeMissing(source, target string) error {
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		sourcePath, targetPath := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
		if entry.IsDir() {
			if err := copyTreeMissing(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Stat(targetPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		input, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		info, err := input.Stat()
		if err != nil {
			input.Close()
			return err
		}
		output, err := os.CreateTemp(target, "."+entry.Name()+".migrate-*")
		if err != nil {
			input.Close()
			return err
		}
		temporaryPath := output.Name()
		if err := output.Chmod(info.Mode().Perm()); err != nil {
			output.Close()
			input.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOut, closeIn := output.Close(), input.Close()
		if copyErr != nil {
			_ = os.Remove(temporaryPath)
			return copyErr
		}
		if closeOut != nil {
			_ = os.Remove(temporaryPath)
			return closeOut
		}
		if closeIn != nil {
			_ = os.Remove(temporaryPath)
			return closeIn
		}
		if _, err := os.Stat(targetPath); err == nil {
			_ = os.Remove(temporaryPath)
			continue
		} else if !os.IsNotExist(err) {
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := os.Rename(temporaryPath, targetPath); err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
	}
	return nil
}
