package bootstrap

import (
	"context"
	w "github.com/wangh00/SciAide/internal/transport/wails"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveProjectBrowserInstallation(t *testing.T) {
	root := os.Getenv("SCIAIDE_BROWSER_LIVE_ROOT")
	if root == "" {
		t.Skip("explicit project-local live installation only")
	}
	a, e := New(Options{RootDir: filepath.Join(root, "appdata")})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.Startup(context.Background())
	p, e := a.ProjectFacade.CreateProject(w.CreateProjectRequest{Name: "Browser runtime QA", WorkspacePath: filepath.Join(root, "workspace")})
	if e != nil {
		t.Fatal(e)
	}
	d, e := a.PythonFacade.DetectInterpreters()
	if e != nil {
		t.Fatal(e)
	}
	base := ""
	for _, v := range d.Interpreters {
		if v.HasVenv && v.Prefix == v.BasePrefix {
			base = v.ExecutablePath
			break
		}
	}
	if base == "" {
		t.Fatal("base Python not found")
	}
	env, e := a.PythonFacade.CreateProjectEnvironment(p.ID, base, false)
	if e != nil {
		t.Fatal(e)
	}
	t.Log("python", env.EnvironmentPythonPath)
	st, e := a.PythonFacade.InstallBrowserEnvironment(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !st.Ready || !strings.HasPrefix(st.BinaryPath, filepath.Join(p.WorkspacePath, ".sciaide")) {
		t.Fatalf("invalid browser %+v", st)
	}
	t.Logf("browser %+v", st)
	st, e = a.PythonFacade.GetBrowserEnvironment(p.ID)
	if e != nil || !st.Ready {
		t.Fatalf("status %+v %v", st, e)
	}
}
