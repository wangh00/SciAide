// Package debugapi is a developer-only, isolated harness around the real
// application services. The desktop executable does not start an HTTP server.
package debugapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/wangh00/SciAide/internal/apperr"
)

type BudgetState struct {
	Enabled bool `json:"enabled"`
	Limit   int  `json:"limit"`
	Used    int  `json:"used"`
}

type requestBudget struct {
	mu    sync.Mutex
	path  string
	state BudgetState
	// Runtime-only grant has an independent short lock: expiry and cancellation
	// cannot wait for this budget's persistent IO to finish.
	grant *sessionGrant
	// Private deterministic test seams; production uses the real clock and
	// atomicJSON. There is no HTTP/CLI option to replace either dependency.
	clock     func() sessionInstant
	writeJSON func(string, []byte) error
}

func (b *requestBudget) now() sessionInstant {
	if b.clock != nil {
		return b.clock()
	}
	return sessionNow()
}

func (b *requestBudget) save(data []byte) error {
	return b.saveAs(b.path, data)
}

func (b *requestBudget) saveAs(path string, data []byte) error {
	if b.writeJSON != nil {
		return b.writeJSON(path, data)
	}
	return atomicJSON(path, data)
}

func (b *requestBudget) expired() bool {
	if b.grant != nil && b.grant.expired() {
		b.state.Enabled = false
		return true
	}
	return false
}

func expiredBudgetError() error {
	return &apperr.Error{Code: "DEBUG_SESSION_EXPIRED", UserMessage: "调试授权时间窗已结束，未发起模型请求；不能通过重试本请求延长授权。"}
}

func openBudget(root string, enabled bool, limit int) (*requestBudget, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("max-requests must be 1..1000")
	}
	b := &requestBudget{path: filepath.Join(root, "model-budget.json"), state: BudgetState{Enabled: enabled, Limit: limit}}
	data, err := os.ReadFile(b.path)
	if err == nil {
		var saved struct {
			Enabled *bool `json:"enabled"`
			Limit   *int  `json:"limit"`
			Used    *int  `json:"used"`
		}
		if json.Unmarshal(data, &saved) != nil || saved.Enabled == nil || saved.Limit == nil || saved.Used == nil || *saved.Limit < 1 || *saved.Limit > 1000 || *saved.Used < 0 {
			return nil, fmt.Errorf("invalid durable model budget; refusing to reset it")
		}
		b.state.Used = *saved.Used // A restart cannot refund attempted requests.
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return b, nil
}

func (b *requestBudget) take(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.expired() {
		return expiredBudgetError()
	}
	if !b.state.Enabled {
		return &apperr.Error{Code: "DEBUG_MODEL_DISABLED", UserMessage: "调试实例未启用模型调用；需显式启用，可能产生费用。"}
	}
	if b.state.Used >= b.state.Limit {
		return &apperr.Error{Code: "DEBUG_MODEL_BUDGET_EXHAUSTED", UserMessage: "调试实例的模型请求次数上限已到，已停止；重启不会退还已计次数。"}
	}
	next := b.state
	next.Used++
	data, _ := json.Marshal(next)
	// Persist BEFORE a potentially billable call. A crash may overcount, never
	// silently undercount. The session lock guarantees one writer process.
	if err := b.save(data); err != nil {
		return &apperr.Error{Code: "DEBUG_BUDGET_SAVE_FAILED", UserMessage: "无法保存调试调用预算，未发起模型请求。", Cause: err}
	}
	b.state = next
	// fsync/rename can outlive the grant or cancellation. Keep the durable
	// conservative count, but never admit the HTTP request after that boundary.
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.expired() {
		return expiredBudgetError()
	}
	return nil
}

func (b *requestBudget) snapshot() BudgetState { b.mu.Lock(); defer b.mu.Unlock(); return b.state }

func atomicJSON(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".debug-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
