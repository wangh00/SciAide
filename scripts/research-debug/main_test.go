package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/debugapi"
)

func TestInvalidWindowRejectedBeforeOpeningWorkspace(t *testing.T) {
	if os.Getenv("SCIAIDE_WINDOW_PREFLIGHT_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("research-debug", flag.ContinueOnError)
		os.Args = []string{"research-debug", "--root", os.Getenv("SCIAIDE_PREFLIGHT_ROOT"), os.Getenv("SCIAIDE_PREFLIGHT_FLAG"), os.Getenv("SCIAIDE_PREFLIGHT_VALUE")}
		err := run()
		if err != nil && (strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "max-duration") || strings.Contains(err.Error(), "port must") || strings.Contains(err.Error(), "max-requests must")) {
			return
		}
		t.Fatalf("window was not rejected at preflight: %v", err)
	}
	for _, existing := range []bool{false, true} {
		for _, invalid := range [][2]string{{"--deadline", "2000-01-01T00:00:00Z"}, {"--max-duration", "0s"}, {"--max-duration", "2h"}, {"--port", "-1"}, {"--port", "65536"}, {"--max-requests", "0"}, {"--max-requests", "1001"}} {
			t.Run(invalid[0]+invalid[1]+map[bool]string{false: "-new", true: "-existing"}[existing], func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "workspace")
				var dbPath string
				if existing {
					w, err := debugapi.OpenWorkspace(root, "", false, 12)
					if err != nil {
						t.Fatal(err)
					}
					_ = w.Close()
					if err := os.MkdirAll(filepath.Join(root, "data"), 0700); err != nil {
						t.Fatal(err)
					}
					dbPath = filepath.Join(root, "data", "sciaide.db")
					// An opaque existing artifact must not even be opened by bootstrap
					// when the supplied grant is invalid. Old ordering reports a DB error.
					if err := os.WriteFile(dbPath, []byte("opaque-existing-database-sentinel"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestInvalidWindowRejectedBeforeOpeningWorkspace$")
				cmd.Env = append(os.Environ(), "SCIAIDE_WINDOW_PREFLIGHT_HELPER=1", "SCIAIDE_PREFLIGHT_ROOT="+root, "SCIAIDE_PREFLIGHT_FLAG="+invalid[0], "SCIAIDE_PREFLIGHT_VALUE="+invalid[1])
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("preflight subprocess: %v %s", err, out)
				}
				if existing {
					data, err := os.ReadFile(dbPath)
					if err != nil || string(data) != "opaque-existing-database-sentinel" {
						t.Fatal("invalid window altered existing app state", err)
					}
				} else if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("invalid window created workspace", err)
				}
			})
		}
	}
}
