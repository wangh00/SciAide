package wails

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type WorkflowFacade struct {
	lifecycle *LifecycleContext
	service   *workflow.Service
	runtime   *workflow.RuntimeService
	starter   *workflow.StarterService
	projects  *project.Service
	tasks     *researchtask.Service
	artifacts *artifact.Service
}

func (f *WorkflowFacade) ReadLiteratureCandidate(projectID, runID, stepID, candidateID string) (json.RawMessage, error) {
	return f.runtime.ReadLiteratureCandidate(f.lifecycle.Context(), projectID, runID, stepID, candidateID)
}

func (f *WorkflowFacade) ConfirmResearchRevision(command workflow.ConfirmResearchRevisionCommand) (workflow.RunDetail, error) {
	return f.runtime.ConfirmResearchRevision(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) ListResearchRevisionProposals(projectID, runID string) ([]workflow.ResearchRevisionProposal, error) {
	return f.runtime.ListResearchRevisionProposals(f.lifecycle.Context(), projectID, runID)
}

func (f *WorkflowFacade) ResearchTimeline(query workflow.ResearchTimelineQuery) (workflow.ResearchTimelinePage, error) {
	return f.runtime.ResearchTimeline(f.lifecycle.Context(), query)
}

func (f *WorkflowFacade) GetPendingResearchAdoption(projectID, runID string) (*workflow.AdoptResearchRouteResult, error) {
	if f.starter == nil {
		return nil, fmt.Errorf("research starter is not configured")
	}
	return f.starter.PendingAdoption(f.lifecycle.Context(), projectID, runID)
}

// WorkflowConversationLookup is deliberately a single return object for the
// Wails bridge.  Wails bindings are more predictable with an explicit found
// flag than with a multi-return (detail, bool) method.
type WorkflowConversationLookup struct {
	Found bool                `json:"found"`
	Run   *workflow.RunDetail `json:"run,omitempty"`
}

func (f *WorkflowFacade) ReplanResearchStarter(command workflow.ReplanResearchStarterCommand) (workflow.RunDetail, error) {
	if f.starter == nil {
		return workflow.RunDetail{}, fmt.Errorf("research starter is not configured")
	}
	return f.starter.Replan(f.lifecycle.Context(), command)
}

func NewWorkflowFacade(lifecycle *LifecycleContext, service *workflow.Service, runtimeService *workflow.RuntimeService, starterService *workflow.StarterService, artifactService *artifact.Service, projects ...*project.Service) *WorkflowFacade {
	value := &WorkflowFacade{lifecycle: lifecycle, service: service, runtime: runtimeService, artifacts: artifactService}
	value.starter = starterService
	if len(projects) > 0 {
		value.projects = projects[0]
	}
	return value
}

func (f *WorkflowFacade) SetResearchTaskService(service *researchtask.Service) {
	if f != nil {
		f.tasks = service
	}
}

// ListResearchTasks is the stable resource owner list used by the project
// knowledge/artifact managers. It is separate from ListProjectRuns because a
// task remains visible after its execution records are removed.
func (f *WorkflowFacade) ListResearchTasks(projectID string, limit int) ([]researchtask.Task, error) {
	if f.tasks == nil {
		return []researchtask.Task{}, nil
	}
	return f.tasks.List(f.lifecycle.Context(), projectID, limit)
}

func (f *WorkflowFacade) StartResearch(command workflow.StartResearchCommand) (workflow.RunDetail, error) {
	if f.starter == nil {
		return workflow.RunDetail{}, fmt.Errorf("research starter is not configured")
	}
	return f.starter.Start(f.lifecycle.Context(), command)
}

// AnswerResearchClarification accepts only option IDs offered by the
// completed planner and starts the next planner attempt for the same task.
func (f *WorkflowFacade) AnswerResearchClarification(command workflow.AnswerResearchClarificationCommand) (workflow.RunDetail, error) {
	if f.starter == nil {
		return workflow.RunDetail{}, fmt.Errorf("research starter is not configured")
	}
	return f.starter.AnswerClarification(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) AdoptResearchRoute(command workflow.AdoptResearchRouteCommand) (workflow.AdoptResearchRouteResult, error) {
	if f.starter == nil {
		return workflow.AdoptResearchRouteResult{}, fmt.Errorf("research starter is not configured")
	}
	return f.starter.Adopt(f.lifecycle.Context(), command)
}

// StartAdoptedResearchRoute resumes a route that was adopted while waiting
// for a required project input.  Keeping this boundary on StarterService
// prevents callers from forging the internal research-task idempotency key.
func (f *WorkflowFacade) StartAdoptedResearchRoute(command workflow.StartAdoptedResearchRouteCommand) (workflow.RunDetail, error) {
	if f.starter == nil {
		return workflow.RunDetail{}, fmt.Errorf("research starter is not configured")
	}
	return f.starter.StartAdoptedResearchRoute(f.lifecycle.Context(), command)
}

// GetResearchStarterPlan returns the host-validated projection of a completed
// AI research-starter Run. The planner output remains immutable in storage;
// this endpoint only lets the UI show the same structural, resource and Skill
// validation result that AdoptResearchRoute will enforce.
func (f *WorkflowFacade) GetResearchStarterPlan(projectID, runID string) (workflow.ResearchStarterPlan, error) {
	if f.starter == nil || f.runtime == nil {
		return workflow.ResearchStarterPlan{}, fmt.Errorf("research starter is not configured")
	}
	detail, err := f.runtime.Get(f.lifecycle.Context(), projectID, runID)
	if err != nil {
		return workflow.ResearchStarterPlan{}, err
	}
	plan, err := f.starter.ProjectPlan(f.lifecycle.Context(), detail)
	if err != nil {
		return workflow.ResearchStarterPlan{}, err
	}
	if plan == nil {
		return workflow.ResearchStarterPlan{}, fmt.Errorf("当前 Run 不是已完成的 AI 研究启动任务")
	}
	return *plan, nil
}

func (f *WorkflowFacade) ChooseInputFile(projectID, kind string) (workflow.InputFile, error) {
	return f.chooseInputFile(projectID, kind, "")
}

// ChooseInputFileForTask stages an explicitly selected table directly into a
// research task's private workspace. A pending adopted route already owns its
// starter task identity, so it must not leave a second project-shared copy.
func (f *WorkflowFacade) ChooseInputFileForTask(projectID, kind, taskID string) (workflow.InputFile, error) {
	ctx := f.lifecycle.Context()
	resolvedTaskID, err := f.resolveResearchTaskID(ctx, projectID, taskID)
	if err != nil {
		return workflow.InputFile{}, err
	}
	return f.chooseInputFile(projectID, kind, resolvedTaskID)
}

// ChooseInputFileForResearchRoute stages a file for a pending AI route. The
// caller supplies the immutable Planner Run ID. The Run lookup is
// project-scoped, so the server derives the only valid task owner itself.
func (f *WorkflowFacade) ChooseInputFileForResearchRoute(projectID, kind, starterRunID string) (workflow.InputFile, error) {
	ctx := f.lifecycle.Context()
	projectID, starterRunID = strings.TrimSpace(projectID), strings.TrimSpace(starterRunID)
	if f.runtime == nil || projectID == "" || starterRunID == "" {
		return workflow.InputFile{}, fmt.Errorf("research route and project are required")
	}
	detail, err := f.runtime.Get(ctx, projectID, starterRunID)
	if err != nil {
		return workflow.InputFile{}, err
	}
	if detail.Run.WorkflowPurpose != workflow.PurposeResearchStarter {
		return workflow.InputFile{}, fmt.Errorf("selected research route starter is invalid")
	}
	taskID, err := f.runtime.ResolveResearchTaskForRun(ctx, projectID, detail.Run)
	if err != nil {
		return workflow.InputFile{}, err
	}
	return f.chooseInputFile(projectID, kind, taskID)
}

// resolveResearchTaskForRun is the single ownership boundary for a Workflow
// Run that needs task-private resources. The Run was already loaded through a
// project-scoped query, so its project is authoritative; callers must not
// reconstruct a task identity from a browser-side route object.
func (f *WorkflowFacade) resolveResearchTaskID(ctx context.Context, projectID, value string) (string, error) {
	value = strings.TrimSpace(value)
	if err := researchtask.Validate(ctx, f.tasks, projectID, value); err != nil {
		return "", err
	}
	return value, nil
}

func (f *WorkflowFacade) chooseInputFile(projectID, kind, taskID string) (workflow.InputFile, error) {
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
	if strings.TrimSpace(taskID) != "" {
		return f.service.StageInputFileForTask(f.lifecycle.Context(), projectID, path, kind, taskID)
	}
	return f.service.StageInputFile(f.lifecycle.Context(), projectID, path, kind)
}

func (f *WorkflowFacade) Start(command workflow.StartCommand) (workflow.RunDetail, error) {
	return f.runtime.Start(f.lifecycle.Context(), command)
}

func (f *WorkflowFacade) ListRuns(projectID, workflowID string, limit int) ([]workflow.Run, error) {
	return f.runtime.List(f.lifecycle.Context(), projectID, workflowID, limit)
}

// ListProjectRuns exposes research tasks as project-level objects. A Workflow
// is a reusable plan; callers should not need to select one before finding a
// task that was already started.
func (f *WorkflowFacade) ListProjectRuns(projectID string, limit int) ([]workflow.Run, error) {
	return f.runtime.List(f.lifecycle.Context(), projectID, "", limit)
}

func (f *WorkflowFacade) GetRun(projectID, runID string) (workflow.RunDetail, error) {
	return f.runtime.Get(f.lifecycle.Context(), projectID, runID)
}

// GetRunByConversation restores the research workspace for a conversation
// opened from a history entry. Ordinary conversations return found=false.
func (f *WorkflowFacade) GetRunByConversation(conversationID string) (WorkflowConversationLookup, error) {
	detail, found, err := f.runtime.GetRunByConversation(f.lifecycle.Context(), conversationID)
	if err != nil || !found {
		return WorkflowConversationLookup{Found: found}, err
	}
	return WorkflowConversationLookup{Found: true, Run: &detail}, nil
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

func (f *WorkflowFacade) DeleteRun(projectID, runID string) error {
	return f.runtime.Delete(f.lifecycle.Context(), projectID, runID)
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
