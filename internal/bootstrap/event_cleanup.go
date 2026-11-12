package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

const orphanEventCleanupKey = "maintenance.orphan-run-events.v1"

func (a *Application) startEventCleanup(parent context.Context) {
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	if a.maintenanceClosed || a.maintenanceDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	a.maintenanceCancel = cancel
	done := make(chan struct{})
	a.maintenanceDone = done
	go func() {
		defer close(done)
		wait := func(d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
		if !wait(3 * time.Second) {
			return
		}
		var value string
		err := a.store.DB().QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key=?`, orphanEventCleanupKey).Scan(&value)
		if err == nil {
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			if ctx.Err() == nil {
				a.Logger.Warn("event cleanup status read failed", "error", err)
			}
			return
		}
		var after, total int64
		started := time.Now()
		for {
			next, count, finished, err := sqlite.SweepOrphanRunEvents(ctx, a.store.DB(), after, 1000)
			if err != nil {
				if ctx.Err() == nil {
					a.Logger.Warn("orphan event cleanup stopped", "removed", total, "error", err)
				}
				return
			}
			after = next
			total += count
			if finished {
				_, err = a.store.DB().ExecContext(ctx, `INSERT INTO settings(key,value_json,updated_at) VALUES (?,'true',?) ON CONFLICT(key) DO NOTHING`, orphanEventCleanupKey, time.Now().UTC().Format(time.RFC3339Nano))
				if err != nil {
					if ctx.Err() == nil {
						a.Logger.Warn("event cleanup completion save failed", "error", err)
					}
					return
				}
				a.Logger.Info("orphan event cleanup completed", "removed", total, "durationMillis", time.Since(started).Milliseconds())
				return
			}
			if !wait(25 * time.Millisecond) {
				return
			}
		}
	}()
}
func (a *Application) stopEventCleanup() {
	a.maintenanceMu.Lock()
	a.maintenanceClosed = true
	if a.maintenanceCancel != nil {
		a.maintenanceCancel()
	}
	done := a.maintenanceDone
	a.maintenanceMu.Unlock()
	if done != nil {
		<-done
	}
}
