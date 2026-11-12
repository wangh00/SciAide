package bootstrap

import "context"

func (a *Application) startMCP(ctx context.Context) {
	a.startupMu.Lock()
	defer a.startupMu.Unlock()
	if !a.mcpAutoStart || a.startupClosed || a.startupDone != nil {
		return
	}
	ctx, a.startupCancel = context.WithCancel(ctx)
	done := make(chan struct{})
	a.startupDone = done
	go func() {
		defer close(done)
		result, err := a.mcpService.ConnectOnStartup(ctx)
		if err != nil {
			a.Logger.Warn("MCP startup connection failed", "error", err)
		} else if result.Failed > 0 {
			a.Logger.Warn("MCP startup connections incomplete", "failed", result.Failed)
		}
	}()
}

func (a *Application) stopMCPStartup() {
	a.startupMu.Lock()
	a.startupClosed = true
	if a.startupCancel != nil {
		a.startupCancel()
	}
	done := a.startupDone
	a.startupMu.Unlock()
	if done != nil {
		<-done // service status writes must finish before closing SQLite/logger
	}
}
