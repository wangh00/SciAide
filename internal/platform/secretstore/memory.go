package secretstore

import (
	"context"
	"errors"
	"sync"
)

var ErrNotFound = errors.New("secret not found")

// Memory is intended for tests only. Production composition uses the native
// operating-system credential store.
type Memory struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func NewMemory() *Memory { return &Memory{values: make(map[string][]byte)} }

func (m *Memory) Put(_ context.Context, ref string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[ref] = append([]byte(nil), value...)
	return nil
}

func (m *Memory) Get(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.values[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (m *Memory) Delete(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, ref)
	return nil
}

func (m *Memory) Configured(ctx context.Context, ref string) (bool, string, error) {
	value, err := m.Get(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	defer zero(value)
	return true, mask(value), nil
}

func mask(value []byte) string {
	if len(value) <= 4 {
		return "••••"
	}
	return "••••" + string(value[len(value)-4:])
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
