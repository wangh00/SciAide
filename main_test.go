package main

import (
	"os"
	"strings"
	"testing"
)

func TestSingleInstanceGatePrecedesApplicationBootstrap(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	gate := strings.Index(string(source), "acquireApplicationInstance()")
	bootstrap := strings.Index(string(source), "bootstrap.New(bootstrap.Options{})")
	if gate < 0 || bootstrap < 0 || gate > bootstrap {
		t.Fatalf("single-instance gate must run before application bootstrap: gate=%d bootstrap=%d", gate, bootstrap)
	}
	if !strings.Contains(string(source), "WindowClassName:                   singleInstanceWindowClass") {
		t.Fatal("main window must keep the stable class used by second-launch activation")
	}
}

func TestInitialWindowSizeScalesAndClamps(t *testing.T) {
	tests := []struct {
		name                      string
		screenWidth, screenHeight int
		wantWidth, wantHeight     int
	}{
		{name: "full hd", screenWidth: 1920, screenHeight: 1080, wantWidth: 1280, wantHeight: 800},
		{name: "qhd", screenWidth: 2560, screenHeight: 1440, wantWidth: 1707, wantHeight: 1067},
		{name: "minimum", screenWidth: 1366, screenHeight: 768, wantWidth: 960, wantHeight: 640},
		{name: "maximum", screenWidth: 3840, screenHeight: 2160, wantWidth: 1920, wantHeight: 1200},
		{name: "fallback", screenWidth: 0, screenHeight: 0, wantWidth: 1280, wantHeight: 800},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			width, height := initialWindowSize(test.screenWidth, test.screenHeight)
			if width != test.wantWidth || height != test.wantHeight {
				t.Fatalf("initialWindowSize(%d,%d) = %dx%d, want %dx%d", test.screenWidth, test.screenHeight, width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}
