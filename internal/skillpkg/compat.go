package skillpkg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/skill"
	"gopkg.in/yaml.v3"
)

// prepareCodexCompatiblePackage is an import adapter, not a second installed
// package format. Native SciAide packages keep skill.yaml as their authority.
// A Codex-style package containing only SKILL.md is normalized once in the
// isolated staging directory so installed packages remain versioned,
// integrity checked and reproducible by the rest of SciAide.
func prepareCodexCompatiblePackage(directory, packageNameHint string) error {
	manifestPath := filepath.Join(directory, "skill.yaml")
	if info, err := os.Lstat(manifestPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill.yaml must be a regular file")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect staged skill.yaml: %w", err)
	}

	instructions, err := readBoundedRegular(filepath.Join(directory, "SKILL.md"), maxSkillBytes)
	if err != nil {
		return fmt.Errorf("Skill package must contain skill.yaml or a Codex-compatible SKILL.md: %w", err)
	}
	name, description, err := parseCodexSkillFrontmatter(instructions, packageNameHint)
	if err != nil {
		return fmt.Errorf("import Codex-compatible SKILL.md: %w", err)
	}
	contextTokens := len([]rune(string(instructions)))
	if contextTokens < skill.DefaultContextTokens {
		contextTokens = skill.DefaultContextTokens
	}
	if contextTokens > skill.MaxContextTokens {
		return fmt.Errorf("Codex-compatible SKILL.md exceeds SciAide's maximum Skill context budget; move large references out of SKILL.md")
	}
	manifest := skill.NormalizeManifest(skill.Manifest{
		SchemaVersion: skill.CurrentSchemaVersion,
		ID:            compatibleSkillID(name),
		Name:          name,
		Version:       "0.0.0",
		Description:   description,
		Entry:         "SKILL.md",
		Activation:    skill.Activation{Mode: skill.ActivationExplicit},
		Compatibility: skill.Compatibility{SciAide: ">=0.2.0 <1.0.0"},
		Context:       skill.ContextPolicy{MaxTokens: contextTokens},
	})
	if err := skill.ValidateManifest(manifest); err != nil {
		return fmt.Errorf("normalize Codex-compatible Skill metadata: %w", err)
	}
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode generated skill.yaml: %w", err)
	}
	output, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create generated skill.yaml: %w", err)
	}
	if _, err := output.Write(encoded); err != nil {
		_ = output.Close()
		_ = os.Remove(manifestPath)
		return fmt.Errorf("write generated skill.yaml: %w", err)
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(manifestPath)
		return fmt.Errorf("close generated skill.yaml: %w", err)
	}
	return nil
}

type codexSkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func parseCodexSkillFrontmatter(contents []byte, fallbackName string) (string, string, error) {
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return "", "", fmt.Errorf("SKILL.md must be UTF-8 text without NUL bytes")
	}
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("missing YAML frontmatter delimited by ---")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closing = index
			break
		}
	}
	if closing <= 1 {
		return "", "", fmt.Errorf("missing or empty YAML frontmatter")
	}
	frontmatter := strings.Join(lines[1:closing], "\n")
	if len(frontmatter) > maxManifestBytes {
		return "", "", fmt.Errorf("SKILL.md frontmatter is too large")
	}
	parsed, err := decodeCodexFrontmatter(frontmatter)
	if err != nil {
		if repaired, changed := repairFrontmatterScalarFields(frontmatter); changed {
			parsed, err = decodeCodexFrontmatter(repaired)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	name := strings.Join(strings.Fields(parsed.Name), " ")
	if name == "" {
		name = strings.Join(strings.Fields(fallbackName), " ")
	}
	description := strings.Join(strings.Fields(parsed.Description), " ")
	if name == "" || description == "" {
		return "", "", fmt.Errorf("name and description are required")
	}
	return name, description, nil
}

func decodeCodexFrontmatter(frontmatter string) (codexSkillFrontmatter, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(frontmatter), &document); err != nil {
		return codexSkillFrontmatter{}, err
	}
	nodes := 0
	if len(document.Content) != 1 {
		return codexSkillFrontmatter{}, fmt.Errorf("frontmatter must contain one mapping")
	}
	if err := validateYAMLNode(document.Content[0], 0, &nodes); err != nil {
		return codexSkillFrontmatter{}, err
	}
	var parsed codexSkillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return codexSkillFrontmatter{}, err
	}
	return parsed, nil
}

// Codex tolerates a small class of third-party frontmatter with an unquoted
// colon in a scalar. Keep this repair line-oriented so unrelated malformed
// YAML, aliases and complex documents are still rejected.
func repairFrontmatterScalarFields(frontmatter string) (string, bool) {
	lines := strings.Split(frontmatter, "\n")
	changed := false
	blockIndent := -1
	for index, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if blockIndent >= 0 {
			if strings.TrimSpace(line) == "" || indent > blockIndent {
				continue
			}
			blockIndent = -1
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || colon+1 < len(line) && line[colon+1] != ' ' && line[colon+1] != '\t' {
			continue
		}
		value := strings.TrimSpace(line[colon+1:])
		if value == "" || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
				blockIndent = indent
			}
			continue
		}
		if strings.HasPrefix(value, "'") || strings.HasPrefix(value, `"`) || !strings.Contains(value, ": ") {
			continue
		}
		comment := ""
		if marker := strings.Index(value, " #"); marker >= 0 {
			comment = value[marker:]
			value = strings.TrimSpace(value[:marker])
		}
		quoted := "'" + strings.ReplaceAll(value, "'", "''") + "'"
		lines[index] = line[:colon+1] + " " + quoted + comment
		changed = true
	}
	return strings.Join(lines, "\n"), changed
}

func compatibleSkillID(name string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, character := range strings.ToLower(name) {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
			lastHyphen = false
			continue
		}
		if builder.Len() > 0 && !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	value := strings.Trim(builder.String(), "-")
	if len(value) > 64 {
		value = strings.Trim(value[:64], "-")
	}
	if skill.ValidID(value) {
		return value
	}
	digest := sha256.Sum256([]byte(name))
	return "skill-" + hex.EncodeToString(digest[:6])
}
