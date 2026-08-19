package skill

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	DefaultResourceReadBytes = 128 * 1024
	MaxResourceReadBytes     = 256 * 1024
)

type ResourceContent struct {
	Path          string
	Content       []byte
	OriginalBytes int64
	Truncated     bool
}

type PackageResourceReader interface {
	ReadResource(ctx context.Context, selected RunSkill, resourcePath string, maxBytes int) (ResourceContent, error)
}

// ReadRunResource exposes progressive Skill disclosure without exposing a
// host path. The Run snapshot, not the current project selection, decides
// which exact package may be read.
func (s *Service) ReadRunResource(ctx context.Context, runID, skillID, resourcePath string, maxBytes int) (ResourceContent, error) {
	runID, skillID = strings.TrimSpace(runID), strings.TrimSpace(skillID)
	if runID == "" || !ValidID(skillID) {
		return ResourceContent{}, fmt.Errorf("Run id and valid Skill id are required")
	}
	if err := ValidateResourcePath(resourcePath); err != nil {
		return ResourceContent{}, err
	}
	if maxBytes == 0 {
		maxBytes = DefaultResourceReadBytes
	}
	if maxBytes < 1 || maxBytes > MaxResourceReadBytes {
		return ResourceContent{}, fmt.Errorf("Skill resource read limit is invalid")
	}
	repository, ok := s.repository.(RunContextRepository)
	if !ok {
		return ResourceContent{}, fmt.Errorf("Run Skill context persistence is not configured")
	}
	snapshot, err := repository.GetRunContext(ctx, runID)
	if err != nil {
		return ResourceContent{}, fmt.Errorf("load Run Skill context: %w", err)
	}
	var selected *RunSkill
	for index := range snapshot.Skills {
		if snapshot.Skills[index].Manifest.ID == skillID {
			selected = &snapshot.Skills[index]
			break
		}
	}
	if selected == nil {
		return ResourceContent{}, fmt.Errorf("Skill %s is not selected in this Run", skillID)
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	reader, ok := s.packages.(PackageResourceReader)
	if !ok {
		return ResourceContent{}, fmt.Errorf("Skill resource reading is not configured")
	}
	value, err := reader.ReadResource(ctx, *selected, resourcePath, maxBytes)
	if err != nil {
		return ResourceContent{}, fmt.Errorf("read Skill %s resource: %w", skillID, err)
	}
	if value.Path != resourcePath || value.OriginalBytes < 0 || len(value.Content) > maxBytes || !utf8.Valid(value.Content) || bytes.IndexByte(value.Content, 0) >= 0 {
		return ResourceContent{}, fmt.Errorf("Skill resource reader returned invalid content")
	}
	return value, nil
}

func ValidateResourcePath(value string) error {
	if value == "" || len([]rune(value)) > 4096 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") || value == ".." {
		return fmt.Errorf("Skill resource path must be a canonical package-relative path")
	}
	parts := strings.Split(value, "/")
	if len(parts) > 32 {
		return fmt.Errorf("Skill resource path is too deep")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || !validResourcePathComponent(part) {
			return fmt.Errorf("Skill resource path is invalid")
		}
	}
	return nil
}

func validResourcePathComponent(value string) bool {
	if len([]rune(value)) > 255 || strings.ContainsAny(value, `<>:"|?*`) || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	reserved := map[string]struct{}{"CON": {}, "PRN": {}, "AUX": {}, "NUL": {}, "CLOCK$": {}}
	if _, exists := reserved[base]; exists {
		return false
	}
	for index := 1; index <= 9; index++ {
		if base == fmt.Sprintf("COM%d", index) || base == fmt.Sprintf("LPT%d", index) {
			return false
		}
	}
	return true
}
