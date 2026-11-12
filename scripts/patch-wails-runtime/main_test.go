package main

import (
	"strings"
	"testing"
)

func TestPatchChangesOnlyNativeReload(t *testing.T) {
	source := "package windows\n// before\n" + oldReload + "\n// after\n"
	got, err := patch(source)
	if err != nil || strings.Replace(got, nativeReload, oldReload, 1) != source {
		t.Fatal("overlay changed unrelated source", err)
	}
	if !strings.Contains(got, "f.chromium.Navigate(f.startURL.String())") || strings.Contains(got, "f.ExecJS(") {
		t.Fatal("recovery still depends on crashed page JS")
	}
	for _, changed := range []string{"", oldReload + oldReload, strings.Replace(oldReload, "f.ExecJS", "f.Reload", 1)} {
		if _, err := patch(changed); err == nil {
			t.Fatal("changed upstream source silently accepted")
		}
	}
}
