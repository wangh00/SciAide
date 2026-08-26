package wails

import (
	"fmt"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type WorkflowFacade struct {
	lifecycle *LifecycleContext
	service   *workflow.Service
	runtime   *workflow.RuntimeService
	projects  *project.Service
}

func NewWorkflowFacade(lifecycle *LifecycleContext, service *workflow.Service, runtimeService *workflow.RuntimeService, projects ...*project.Service) *WorkflowFacade {
	value := &WorkflowFacade{lifecycle: lifecycle, service: service, runtime: runtimeService}
	if len(projects) > 0 {
		value.projects = projects[0]
	}
	return value
}

func (f *WorkflowFacade) ChooseInputFile(projectID, kind string) (workflow.InputFile, error) {
	defaultDirectory := ""
	if f.projects != nil {
		if selected, err := f.projects.Get(f.lifecycle.Context(), projectID); err == nil {
			defaultDirectory = selected.WorkspacePath
		}
	}
	filter := runtime.FileFilter{DisplayName: "表格数据 (*.csv;*.tsv;*.xlsx)", Pattern: "*.csv;*.tsv;*.xlsx"}
	switch kind {
	case "xlsx":
		filter = runtime.FileFilter{DisplayName: "Excel 工作簿 (*.xlsx)", Pattern: "*.xlsx"}
	case "delimited":
		filter = runtime.FileFilter{DisplayName: "CSV / TSV 数据 (*.csv;*.tsv)", Pattern: "*.csv;*.tsv"}
	case "tabular":
	default:
		return workflow.InputFile{}, fmt.Errorf("unsupported Workflow input file kind")
	}
	path, err := runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title:            "选择项目分析数据",
		DefaultDirectory: defaultDirectory,
		Filters:          []runtime.FileFilter{filter},
	})
	if err != nil || path == "" {
		return workflow.InputFile{}, err
	}
	return f.service.StageInputFile(f.lifecycle.Context(), projectID, path, kind)
}

func (f *WorkflowFacade) Start(command workflow.StartCommand) (workflow.RunDetail, error) {
	return f.runtime.Start(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) ListRuns(projectID, workflowID string, limit int) ([]workflow.Run, error) {
	return f.runtime.List(f.lifecycle.Context(), projectID, workflowID, limit)
}

func (f *WorkflowFacade) GetRun(projectID, runID string) (workflow.RunDetail, error) {
	return f.runtime.Get(f.lifecycle.Context(), projectID, runID)
}

func (f *WorkflowFacade) Pause(projectID, runID string) (workflow.RunDetail, error) {
	return f.runtime.Pause(f.lifecycle.Context(), projectID, runID)
}

func (f *WorkflowFacade) Resume(projectID, runID string) (workflow.RunDetail, error) {
	return f.runtime.Resume(f.lifecycle.Context(), projectID, runID)
}

func (f *WorkflowFacade) Cancel(projectID, runID string) (workflow.RunDetail, error) {
	return f.runtime.Cancel(f.lifecycle.Context(), projectID, runID)
}

func (f *WorkflowFacade) Decide(command workflow.HumanDecisionCommand) (workflow.RunDetail, error) {
	return f.runtime.Decide(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) ResolveApproval(command permission.ResolveCommand) (workflow.RunDetail, error) {
	return f.runtime.ResolveApproval(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) Retry(command workflow.RetryCommand) (workflow.RunDetail, error) {
	return f.runtime.Retry(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) Validate(projectID string, definition workflow.Definition) workflow.Preview {
	return f.service.Validate(f.lifecycle.Context(), projectID, definition)
}

func (f *WorkflowFacade) Save(command workflow.SaveCommand) (workflow.SaveResult, error) {
	return f.service.Save(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) List(projectID string) ([]workflow.Workflow, error) {
	return f.service.List(f.lifecycle.Context(), projectID)
}

func (f *WorkflowFacade) Get(projectID, workflowID string) (workflow.Detail, error) {
	return f.service.Get(f.lifecycle.Context(), projectID, workflowID)
}

func (f *WorkflowFacade) Delete(projectID, workflowID string) error {
	return f.service.Delete(f.lifecycle.Context(), projectID, workflowID)
}

func (f *WorkflowFacade) Templates() []workflow.Template {
	return workflow.ReferenceTemplates()
}
