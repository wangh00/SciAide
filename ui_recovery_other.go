//go:build !windows || bindings

package main

import (
	"context"
	"github.com/wangh00/SciAide/internal/transport/wails"
)

func startUIRecovery(context.Context, *wails.SystemFacade) func() { return func() {} }
