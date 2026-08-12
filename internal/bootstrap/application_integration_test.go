package bootstrap

import (
	"context"
	"testing"

	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestApplicationCanRestartOnSameData(t *testing.T) {
	root := t.TempDir()
	first, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	first.Startup(context.Background())
	created, err := first.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{
		Name: "可重复启动", Description: "baseline",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	defer second.Close()
	projects, err := second.ProjectFacade.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 1 || projects[0].ID != created.ID {
		t.Fatalf("projects = %#v, want created project", projects)
	}
}
