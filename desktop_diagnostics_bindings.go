//go:build bindings

package main

import "github.com/wangh00/SciAide/internal/platform/desktopdiag"

// Binding generation is not a desktop session and must not touch user logs.
func startDesktopDiagnostics() (*desktopdiag.Session, error) { return nil, nil }
