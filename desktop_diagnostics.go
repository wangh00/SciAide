//go:build !bindings

package main

import (
	"os"

	"github.com/wangh00/SciAide/internal/bootstrap"
	"github.com/wangh00/SciAide/internal/platform/appdirs"
	"github.com/wangh00/SciAide/internal/platform/desktopdiag"
)

func startDesktopDiagnostics() (*desktopdiag.Session, error) {
	dirs := appdirs.ResolveUnder(os.Getenv("SCIAIDE_HOME"))
	if os.Getenv("SCIAIDE_HOME") == "" {
		var err error
		dirs, err = appdirs.Resolve("SciAide")
		if err != nil {
			return nil, err
		}
	}
	session, err := desktopdiag.Start(dirs.Logs, bootstrap.Version)
	if err != nil {
		return nil, err
	}
	return session, session.AttachGoDiagnostics()
}
