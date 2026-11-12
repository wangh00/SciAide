package wails

import (
	"reflect"
	"testing"
)

func TestNativeRecoveryHooksAreNotBoundToPage(t *testing.T) {
	typ := reflect.TypeOf(NewSystemFacade("fixture"))
	for _, name := range []string{"ObserveRendererExit", "SuspendUIRecovery", "RecoverUI"} {
		if _, exposed := typ.MethodByName(name); exposed {
			t.Fatalf("native recovery hook bound to browser: %s", name)
		}
	}
	for _, name := range []string{"UIHeartbeat", "ReportUIError"} {
		if _, exposed := typ.MethodByName(name); !exposed {
			t.Fatalf("readonly diagnostic missing: %s", name)
		}
	}
}
