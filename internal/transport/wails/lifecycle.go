package wails

import (
	"context"
	"sync"
)

type LifecycleContext struct {
	mu  sync.RWMutex
	ctx context.Context
}

func NewLifecycleContext() *LifecycleContext {
	return &LifecycleContext{ctx: context.Background()}
}

func (l *LifecycleContext) Set(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ctx = ctx
}

func (l *LifecycleContext) Context() context.Context {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ctx
}
