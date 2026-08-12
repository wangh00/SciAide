package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
)

type Dirs struct {
	Config  string
	Data    string
	Cache   string
	Logs    string
	Skills  string
	MCP     string
	Backups string
}

func Resolve(product string) (Dirs, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("resolve config directory: %w", err)
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("resolve cache directory: %w", err)
	}
	dataRoot, err := userDataDir()
	if err != nil {
		return Dirs{}, err
	}

	return Dirs{
		Config:  filepath.Join(configRoot, product, "config"),
		Data:    filepath.Join(dataRoot, product, "data"),
		Cache:   filepath.Join(cacheRoot, product),
		Logs:    filepath.Join(dataRoot, product, "logs"),
		Skills:  filepath.Join(dataRoot, product, "skills"),
		MCP:     filepath.Join(dataRoot, product, "mcp"),
		Backups: filepath.Join(dataRoot, product, "backups"),
	}, nil
}

func ResolveUnder(root string) Dirs {
	return Dirs{
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "logs"),
		Skills:  filepath.Join(root, "skills"),
		MCP:     filepath.Join(root, "mcp"),
		Backups: filepath.Join(root, "backups"),
	}
}

func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Config, d.Data, d.Cache, d.Logs, d.Skills, d.MCP, d.Backups} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create application directory %q: %w", dir, err)
		}
	}
	return nil
}

func userDataDir() (string, error) {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return local, nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	return config, nil
}
