// Package skill contains only the read-only data contract for immutable P4
// Run Skill snapshots. New Runs use internal/opensciskill.
package skill

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	CurrentSchemaVersion = 1
	DefaultContextTokens = 8_000
	MaxContextTokens     = 32_000
)

type ActivationMode string

const (
	ActivationExplicit ActivationMode = "explicit"
	ActivationSuggest  ActivationMode = "suggest"
)

type Activation struct {
	Mode     ActivationMode `json:"mode" yaml:"mode"`
	Triggers []string       `json:"triggers" yaml:"triggers"`
}

type Requirements struct {
	Tools         []string `json:"tools" yaml:"tools"`
	OptionalTools []string `json:"optionalTools" yaml:"optional_tools"`
	Skills        []string `json:"skills,omitempty" yaml:"skills,omitempty"`
}

type Coordination struct {
	ConflictGroups []string `json:"conflictGroups,omitempty" yaml:"conflict_groups,omitempty"`
}

type Compatibility struct {
	SciAide string `json:"sciaide" yaml:"sciaide"`
}

type ContextPolicy struct {
	MaxTokens int `json:"maxTokens" yaml:"max_tokens"`
}

type Manifest struct {
	SchemaVersion int           `json:"schemaVersion" yaml:"schema_version"`
	ID            string        `json:"id" yaml:"id"`
	Name          string        `json:"name" yaml:"name"`
	Version       string        `json:"version" yaml:"version"`
	Description   string        `json:"description" yaml:"description"`
	Entry         string        `json:"entry" yaml:"entry"`
	Activation    Activation    `json:"activation" yaml:"activation"`
	Requires      Requirements  `json:"requires" yaml:"requires"`
	Coordination  Coordination  `json:"coordination,omitempty" yaml:"coordination,omitempty"`
	Permissions   []string      `json:"permissions" yaml:"permissions"`
	Compatibility Compatibility `json:"compatibility" yaml:"compatibility"`
	Context       ContextPolicy `json:"context" yaml:"context"`
}

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	toolPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)
)

func ValidID(value string) bool      { return idPattern.MatchString(value) }
func ValidVersion(value string) bool { return versionPattern.MatchString(value) }

func NormalizeManifest(value Manifest) Manifest {
	value.ID, value.Version, value.Entry = strings.TrimSpace(value.ID), strings.TrimSpace(value.Version), strings.TrimSpace(value.Entry)
	value.Name, value.Description = normalizeSingleLine(value.Name), normalizeSingleLine(value.Description)
	value.Activation.Mode = ActivationMode(strings.TrimSpace(string(value.Activation.Mode)))
	if value.Activation.Mode == "" {
		value.Activation.Mode = ActivationExplicit
	}
	value.Activation.Triggers = normalizeList(value.Activation.Triggers, false)
	value.Requires.Tools = normalizeList(value.Requires.Tools, false)
	value.Requires.OptionalTools = normalizeList(value.Requires.OptionalTools, false)
	value.Requires.Skills = normalizeList(value.Requires.Skills, true)
	value.Coordination.ConflictGroups = normalizeList(value.Coordination.ConflictGroups, true)
	value.Permissions = normalizeList(value.Permissions, false)
	value.Compatibility.SciAide = strings.TrimSpace(value.Compatibility.SciAide)
	if value.Context.MaxTokens == 0 {
		value.Context.MaxTokens = DefaultContextTokens
	}
	return value
}

func ValidateManifest(value Manifest) error {
	if value.SchemaVersion != CurrentSchemaVersion || !ValidID(value.ID) || !ValidVersion(value.Version) || value.Entry != "SKILL.md" {
		return fmt.Errorf("invalid historical Skill manifest identity")
	}
	if !validText(value.Name, 1, 100) || !validText(value.Description, 1, 500) || !validText(value.Compatibility.SciAide, 1, 256) {
		return fmt.Errorf("invalid historical Skill manifest text")
	}
	if value.Activation.Mode != ActivationExplicit && value.Activation.Mode != ActivationSuggest || value.Activation.Mode == ActivationSuggest && len(value.Activation.Triggers) == 0 {
		return fmt.Errorf("invalid historical Skill activation")
	}
	if err := validateUniqueList(value.Activation.Triggers, 50, 100, nil); err != nil {
		return err
	}
	if err := validateUniqueList(value.Requires.Tools, 64, 255, toolPattern); err != nil {
		return err
	}
	if err := validateUniqueList(value.Requires.OptionalTools, 64, 255, toolPattern); err != nil {
		return err
	}
	requiredTools := make(map[string]struct{}, len(value.Requires.Tools))
	for _, name := range value.Requires.Tools {
		requiredTools[name] = struct{}{}
	}
	for _, name := range value.Requires.OptionalTools {
		if _, duplicate := requiredTools[name]; duplicate {
			return fmt.Errorf("historical Skill tool is both required and optional")
		}
	}
	if err := validateUniqueList(value.Requires.Skills, 32, 64, idPattern); err != nil {
		return err
	}
	for _, dependency := range value.Requires.Skills {
		if dependency == value.ID {
			return fmt.Errorf("historical Skill depends on itself")
		}
	}
	if err := validateUniqueList(value.Coordination.ConflictGroups, 32, 64, idPattern); err != nil {
		return err
	}
	if err := validateUniqueList(value.Permissions, 7, 64, nil); err != nil {
		return err
	}
	allowedPermissions := map[string]struct{}{"workspace.read": {}, "workspace.write": {}, "filesystem.external": {}, "network.domain": {}, "process.execute": {}, "destructive": {}, "secret.use": {}}
	for _, permission := range value.Permissions {
		if _, allowed := allowedPermissions[permission]; !allowed {
			return fmt.Errorf("unsupported historical Skill permission")
		}
	}
	if value.Context.MaxTokens < 256 || value.Context.MaxTokens > MaxContextTokens {
		return fmt.Errorf("invalid historical Skill context budget")
	}
	if err := validateVersionConstraint(value.Compatibility.SciAide); err != nil {
		return fmt.Errorf("invalid historical Skill compatibility: %w", err)
	}
	return nil
}

func CanonicalManifest(value Manifest) ([]byte, error) { return json.Marshal(value) }

func validText(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	length := len([]rune(value))
	return length >= minimum && length <= maximum
}

func validateUniqueList(values []string, maximumItems, maximumRunes int, pattern *regexp.Regexp) error {
	if len(values) > maximumItems {
		return fmt.Errorf("historical Skill list is too large")
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !validText(value, 1, maximumRunes) || pattern != nil && !pattern.MatchString(value) {
			return fmt.Errorf("invalid historical Skill list value")
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate historical Skill list value")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func normalizeList(values []string, optional bool) []string {
	if len(values) == 0 {
		if optional {
			return nil
		}
		return []string{}
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = normalizeSingleLine(value)
	}
	return result
}

func normalizeSingleLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func validateVersionConstraint(value string) error {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 8 {
		return fmt.Errorf("constraint is empty or too complex")
	}
	for _, field := range fields {
		version := field
		for _, operator := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(version, operator) {
				version = strings.TrimPrefix(version, operator)
				break
			}
		}
		if !ValidVersion(version) {
			return fmt.Errorf("invalid semantic version")
		}
		base := strings.SplitN(strings.SplitN(version, "+", 2)[0], "-", 2)
		if len(base) == 2 {
			for _, identifier := range strings.Split(base[1], ".") {
				if _, err := strconv.Atoi(identifier); err == nil && len(identifier) > 1 && identifier[0] == '0' {
					return fmt.Errorf("numeric prerelease identifier has a leading zero")
				}
			}
		}
	}
	return nil
}
