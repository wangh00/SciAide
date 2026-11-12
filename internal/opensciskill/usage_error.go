package opensciskill

import (
	"fmt"
	"sort"
)

// UsageError contains only host-generated catalog/read diagnostics. Storage,
// filesystem and provenance failures must not be converted into this type.
type UsageError struct {
	Kind     string
	Name     string
	Reason   string
	Section  string
	Sections []InstructionSection
	Maximum  int
}

const (
	UsageName        = "name"
	UsageDisabled    = "disabled"
	UsageUnavailable = "unavailable"
	UsageSection     = "section"
	UsageOffset      = "offset"
	UsageLimit       = "limit"
)

func (e *UsageError) Error() string {
	switch e.Kind {
	case UsageName:
		return "valid Skill name is required"
	case UsageDisabled:
		return fmt.Sprintf("Skill %q is disabled", e.Name)
	case UsageUnavailable:
		return fmt.Sprintf("Skill %q is currently unavailable: %s", e.Name, e.Reason)
	case UsageSection:
		return fmt.Sprintf("Skill section %q was not found", e.Section)
	case UsageOffset:
		return fmt.Sprintf("Skill offset is outside the range 0..%d", e.Maximum)
	case UsageLimit:
		return fmt.Sprintf("Skill read limit must be between 1 and %d runes", e.Maximum)
	default:
		return "invalid Skill read arguments"
	}
}

// Callable is the common candidate predicate, not an additional load gate.
// Dependency/external-service Skills remain discoverable with their existing
// capability boundaries. Non-entry resources can still be read explicitly.
func (info Info) Callable() bool {
	return info.Enabled && info.Entry && info.Capability != CapabilityUnavailable
}

// CallableCategories describes the same candidates as Browse/routing, while
// Snapshot.Categories keeps the full management catalog (including disabled
// and unavailable entries) for settings/source inspection.
func (snapshot Snapshot) CallableCategories() []Category {
	counts := map[string]int{}
	for _, info := range snapshot.Skills {
		if info.Callable() {
			counts[normalizedCategory(info.Category)]++
		}
	}
	result := make([]Category, 0, len(counts))
	for name, count := range counts {
		result = append(result, Category{Name: name, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].Name < result[j].Name
		}
		return result[i].Count > result[j].Count
	})
	return result
}

func skillAvailabilityError(info Info) error {
	if !info.Enabled {
		return &UsageError{Kind: UsageDisabled, Name: info.Name}
	}
	if info.Capability == CapabilityUnavailable {
		return &UsageError{Kind: UsageUnavailable, Name: info.Name, Reason: info.CapabilityReason}
	}
	return nil
}
