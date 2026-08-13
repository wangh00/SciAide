package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MemoryRegistry is the process-local composition point for builtin, MCP and
// Skill tools. It rejects duplicate names instead of allowing registration
// order to silently replace a security definition.
type MemoryRegistry struct {
	mu      sync.RWMutex
	entries map[string]registryEntry
}

type registryEntry struct {
	tool       Tool
	definition Definition
}

func NewRegistry() *MemoryRegistry {
	return &MemoryRegistry{entries: make(map[string]registryEntry)}
}

func (r *MemoryRegistry) Register(ctx context.Context, value Tool) error {
	if value == nil {
		return fmt.Errorf("tool is required")
	}
	definition, err := value.Definition(ctx)
	if err != nil {
		return fmt.Errorf("load tool definition: %w", err)
	}
	if err := ValidateDefinition(definition); err != nil {
		return fmt.Errorf("validate tool definition: %w", err)
	}
	definition = SnapshotDefinition(definition)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[definition.QualifiedName]; exists {
		return fmt.Errorf("tool %q is already registered", definition.QualifiedName)
	}
	r.entries[definition.QualifiedName] = registryEntry{tool: value, definition: definition}
	return nil
}

// ReplaceNamespace atomically replaces one dynamic namespace. Builtin and
// other server namespaces are left untouched, and a validation failure keeps
// the previous entries intact.
func (r *MemoryRegistry) ReplaceNamespace(ctx context.Context, prefix string, values []Tool) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || !strings.HasSuffix(prefix, ".") {
		return fmt.Errorf("tool namespace prefix must end with a dot")
	}
	replacements := make(map[string]registryEntry, len(values))
	for _, value := range values {
		if value == nil {
			return fmt.Errorf("tool is required")
		}
		definition, err := value.Definition(ctx)
		if err != nil {
			return fmt.Errorf("load tool definition: %w", err)
		}
		if err := ValidateDefinition(definition); err != nil {
			return fmt.Errorf("validate tool definition: %w", err)
		}
		definition = SnapshotDefinition(definition)
		if !strings.HasPrefix(definition.QualifiedName, prefix) {
			return fmt.Errorf("tool %q is outside namespace %q", definition.QualifiedName, prefix)
		}
		if _, exists := replacements[definition.QualifiedName]; exists {
			return fmt.Errorf("duplicate replacement tool %q", definition.QualifiedName)
		}
		replacements[definition.QualifiedName] = registryEntry{tool: value, definition: definition}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.entries {
		if strings.HasPrefix(name, prefix) {
			delete(r.entries, name)
		}
	}
	for name, entry := range replacements {
		r.entries[name] = entry
	}
	return nil
}

func (r *MemoryRegistry) Resolve(_ context.Context, qualifiedName string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.entries[strings.TrimSpace(qualifiedName)]
	if !exists {
		return nil, fmt.Errorf("tool %q is not registered", qualifiedName)
	}
	return entry.tool, nil
}

func (r *MemoryRegistry) Definition(_ context.Context, qualifiedName string) (Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.entries[strings.TrimSpace(qualifiedName)]
	if !exists {
		return Definition{}, fmt.Errorf("tool %q is not registered", qualifiedName)
	}
	return cloneDefinition(entry.definition), nil
}

func (r *MemoryRegistry) Definitions(context.Context) ([]Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]Definition, 0, len(r.entries))
	for _, entry := range r.entries {
		values = append(values, cloneDefinition(entry.definition))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].QualifiedName < values[j].QualifiedName })
	return values, nil
}

func cloneDefinition(value Definition) Definition {
	return SnapshotDefinition(value)
}
