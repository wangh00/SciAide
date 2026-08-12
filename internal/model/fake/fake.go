package fake

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/wangh00/SciAide/internal/model"
)

type Step struct {
	Event model.Event
	Err   error
}

type Model struct {
	mu       sync.Mutex
	scripts  [][]Step
	requests []model.ChatRequest
}

func New(scripts ...[]Step) *Model {
	copyScripts := make([][]Step, len(scripts))
	for i := range scripts {
		copyScripts[i] = append([]Step(nil), scripts[i]...)
	}
	return &Model{scripts: copyScripts}
}

func (m *Model) Capabilities(context.Context) (model.Capabilities, error) {
	return model.Capabilities{Streaming: true, ToolCalling: true, MaxContextTokens: 8192}, nil
}

func (m *Model) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.scripts) == 0 {
		return nil, fmt.Errorf("fake model has no remaining script")
	}
	m.requests = append(m.requests, request)
	script := m.scripts[0]
	m.scripts = m.scripts[1:]
	return &stream{ctx: ctx, steps: script}, nil
}

func (m *Model) Requests() []model.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.ChatRequest(nil), m.requests...)
}

type stream struct {
	ctx    context.Context
	steps  []Step
	index  int
	closed bool
}

func (s *stream) Recv() (model.Event, error) {
	if s.closed || s.index >= len(s.steps) {
		return model.Event{}, io.EOF
	}
	select {
	case <-s.ctx.Done():
		return model.Event{}, s.ctx.Err()
	default:
	}
	step := s.steps[s.index]
	s.index++
	return step.Event, step.Err
}

func (s *stream) Close() error {
	s.closed = true
	return nil
}
