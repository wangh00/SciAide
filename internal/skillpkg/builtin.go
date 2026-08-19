package skillpkg

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/wangh00/SciAide/internal/app/skill"
)

//go:embed builtins/*/*/skill.yaml builtins/*/*/SKILL.md
var builtinSkillFiles embed.FS

type BuiltinInstaller interface {
	InstallBuiltin(context.Context, string) (skill.InstallResult, error)
}

type BuiltinSeedResult struct {
	Installed  []string
	Reconciled []string
	Preserved  []string
}

type builtinDescriptor struct {
	id      string
	version string
}

var builtinDescriptors = []builtinDescriptor{
	{id: "academic-writing", version: "1.0.0"},
	{id: "literature-reading", version: "1.1.0"},
}

// SeedBuiltins copies trusted embedded bytes to a temporary source and then
// hands them to the regular package installer. Existing different bytes are
// preserved: a built-in update must use a new semantic version and can never
// silently replace a user package.
func SeedBuiltins(ctx context.Context, installer BuiltinInstaller, installedRoot, cacheRoot string) (BuiltinSeedResult, error) {
	result := BuiltinSeedResult{Installed: []string{}, Reconciled: []string{}, Preserved: []string{}}
	if installer == nil {
		return result, fmt.Errorf("built-in Skill installer is required")
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return result, fmt.Errorf("create built-in Skill cache root: %w", err)
	}
	if err := requireSafeDirectory(cacheRoot); err != nil {
		return result, fmt.Errorf("built-in Skill cache root is unsafe")
	}
	temporary, err := os.MkdirTemp(cacheRoot, "builtin-seed-*")
	if err != nil {
		return result, fmt.Errorf("create built-in Skill source: %w", err)
	}
	defer func() { _ = removeManagedTree(cacheRoot, temporary) }()

	for _, descriptor := range builtinDescriptors {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		identity := descriptor.id + "@" + descriptor.version
		source, err := materializeBuiltin(descriptor, temporary)
		if err != nil {
			return result, err
		}
		candidate, err := NewCatalog(installedRoot).InspectDirectory(ctx, source)
		if err != nil {
			return result, fmt.Errorf("validate embedded Skill %s: %w", identity, err)
		}
		if candidate.Skill.Manifest.ID != descriptor.id || candidate.Skill.Manifest.Version != descriptor.version {
			return result, fmt.Errorf("embedded Skill identity does not match %s", identity)
		}

		destination, err := safeManagedJoin(installedRoot, filepath.ToSlash(filepath.Join(descriptor.id, descriptor.version)))
		if err != nil {
			return result, err
		}
		existed := false
		if _, err := os.Lstat(destination); err == nil {
			existed = true
			existing, loadErr := NewCatalog(installedRoot).InspectDirectory(ctx, destination)
			if loadErr != nil || existing.Skill.PackageHash != candidate.Skill.PackageHash {
				result.Preserved = append(result.Preserved, identity)
				continue
			}
		} else if !os.IsNotExist(err) {
			return result, fmt.Errorf("inspect built-in Skill destination: %w", err)
		}

		installed, err := installer.InstallBuiltin(ctx, source)
		if err != nil {
			return result, fmt.Errorf("install built-in Skill %s: %w", identity, err)
		}
		if installed.Skill.Manifest.ID != descriptor.id || installed.Skill.Manifest.Version != descriptor.version || installed.Skill.Source.Kind != skill.SourceBuiltin {
			return result, fmt.Errorf("installed built-in Skill provenance does not match %s", identity)
		}
		if existed {
			result.Reconciled = append(result.Reconciled, identity)
		} else {
			result.Installed = append(result.Installed, identity)
		}
	}
	return result, nil
}

func materializeBuiltin(descriptor builtinDescriptor, root string) (string, error) {
	source := filepath.Join(root, descriptor.id)
	if err := os.Mkdir(source, 0o700); err != nil {
		return "", fmt.Errorf("create embedded Skill directory: %w", err)
	}
	for _, name := range []string{"skill.yaml", "SKILL.md"} {
		embeddedPath := filepath.ToSlash(filepath.Join("builtins", descriptor.id, descriptor.version, name))
		contents, err := fs.ReadFile(builtinSkillFiles, embeddedPath)
		if err != nil {
			return "", fmt.Errorf("read embedded Skill file %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(source, name), contents, 0o600); err != nil {
			return "", fmt.Errorf("materialize embedded Skill file %s: %w", name, err)
		}
	}
	return source, nil
}
