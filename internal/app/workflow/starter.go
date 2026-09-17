package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/skillrun"
)

const maxResearchIdeaRunes = 8_000

const adoptedResearchRouteCreationPrefix = "research-route:"

const dynamicResearchPlannerVersion = "dynamic-v2"

const (
	maxResearchStarterSkillCandidates = 8
	maxResearchStarterSelectedSkills  = 4
)

// ApplyResearchTaskLink derives the stable user-facing task identity from the
// immutable Run relationship. The planning and execution Runs stay separate
// audit records while the UI can treat them as one continuous research task.
func ApplyResearchTaskLink(run *Run) {
	if run == nil {
		return
	}
	if run.WorkflowPurpose == PurposeResearchStarter {
		return
	}
	if !strings.HasPrefix(run.CreationKey, adoptedResearchRouteCreationPrefix) {
		return
	}
	remainder := strings.TrimPrefix(run.CreationKey, adoptedResearchRouteCreationPrefix)
	separator := strings.LastIndex(remainder, ":")
	if separator <= 0 {
		return
	}
	run.ResearchStarterID = strings.TrimSpace(remainder[:separator])
	// A replanned starter Run can have a different ID while remaining part of
	// the same durable research task. RuntimeService persists that task identity
	// explicitly; this projection only links the originating planner Run.
}

type StarterAttachmentLoader interface {
	List(ctx context.Context, projectID string) ([]attachment.Attachment, error)
}

type StarterKnowledgeLoader interface {
	ListDocuments(ctx context.Context, projectID string) ([]knowledge.Document, error)
}

type ResourceSnapshot struct {
	ResourceFingerprint    string         `json:"resourceFingerprint,omitempty"`
	AttachmentCount        int            `json:"attachmentCount"`
	ReadyAttachmentCount   int            `json:"readyAttachmentCount"`
	AttachmentFormats      map[string]int `json:"attachmentFormats"`
	AttachmentNames        []string       `json:"attachmentNames"`
	KnowledgeDocumentCount int            `json:"knowledgeDocumentCount"`
	ReadyKnowledgeCount    int            `json:"readyKnowledgeCount"`
	WorkspaceFileCount     int            `json:"workspaceFileCount"`
	TabularFiles           []string       `json:"tabularFiles"`
	SnapshotLimitReached   bool           `json:"snapshotLimitReached"`
}

type ResearchStarterContext struct {
	ResearchIdea     string           `json:"researchIdea"`
	ResourceSnapshot ResourceSnapshot `json:"resourceSnapshot"`
	StageCatalog     []ResearchStage  `json:"stageCatalog"`
	PlannerVersion   string           `json:"plannerVersion"`
	// ClarificationAnswers is populated only on a replanning attempt. Keeping
	// the option IDs in the frozen input makes the hand-off auditable without
	// relying on the rendered labels or reparsing the planner prompt.
	ClarificationAnswers map[string][]string `json:"clarificationAnswers,omitempty"`
	PriorStarterRunID    string              `json:"priorStarterRunId,omitempty"`
}

type ResearchStage struct {
	StageID      string   `json:"stageId"`
	Name         string   `json:"name"`
	Purpose      string   `json:"purpose"`
	Requires     []string `json:"requires"`
	Provides     []string `json:"provides"`
	ResourceKind string   `json:"resourceKind,omitempty"`
}

type ResearchRoute struct {
	RouteID      string `json:"routeId"`
	Title        string `json:"title"`
	Reason       string `json:"reason"`
	AvailableNow bool   `json:"availableNow"`
	// Validation is host-derived metadata. It is intentionally omitted from
	// the planner schema and is only added to the read-only UI projection after
	// the frozen route has been checked by the host.
	Validation        string   `json:"validation,omitempty"`
	ValidationError   string   `json:"validationError,omitempty"`
	PlanningNotes     []string `json:"planningNotes,omitempty"`
	RequiredResources []string `json:"requiredResources"`
	Deliverables      []string `json:"deliverables"`
	Blockers          []string `json:"blockers"`
	StageIDs          []string `json:"stageIds,omitempty"`
	ReviewCheckpoints []string `json:"reviewCheckpoints,omitempty"`
	// StagePlans is the semantic stage selection emitted by new planners.
	// Executable order, inputs, outputs and display layers are host-derived;
	// legacy StageIDs/Layers remain readable for frozen historical runs.
	StagePlans     []ResearchStagePlan      `json:"stagePlans,omitempty"`
	Layers         []ResearchRouteLayer     `json:"layers,omitempty"`
	SelectedSkills []ResearchSkillSelection `json:"-"`
}

// ResearchStagePlan is the semantic portion of a planner route. The model
// selects and describes the stages that are meaningful for the question; the
// host later compiles these declarations into executable order, ports,
// checkpoints and display layers.
type ResearchStagePlan struct {
	StageID    string   `json:"stageId"`
	Objective  string   `json:"objective"`
	Methods    []string `json:"methods,omitempty"`
	SkillNames []string `json:"skillNames,omitempty"`
}

type ResearchSkillSelection struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	StageIDs    []string `json:"stageIds"`
	Limitations []string `json:"limitations"`
	ContentHash string   `json:"contentHash,omitempty"`
	PackageHash string   `json:"packageHash,omitempty"`
}

type ResearchRouteLayer struct {
	LayerID   string               `json:"layerId"`
	Title     string               `json:"title"`
	Objective string               `json:"objective"`
	Stages    []ResearchRouteStage `json:"stages"`
}

type ResearchRouteStage struct {
	StageID         string   `json:"stageId"`
	Objective       string   `json:"objective"`
	Methods         []string `json:"methods"`
	SkillNames      []string `json:"skillNames"`
	Inputs          []string `json:"inputs"`
	Outputs         []string `json:"outputs"`
	HumanCheckpoint bool     `json:"humanCheckpoint"`
}

type ResearchStarterPlan struct {
	LoadedSkills         []skillrun.Snapshot      `json:"loadedSkills,omitempty"`
	NormalizedQuestion   string                   `json:"normalizedQuestion"`
	ResearchType         string                   `json:"researchType"`
	AvailableResources   []string                 `json:"availableResources"`
	MissingInformation   []string                 `json:"missingInformation"`
	Routes               []ResearchRoute          `json:"routes"`
	RecommendedRouteID   string                   `json:"recommendedRouteId"`
	RecommendationReason string                   `json:"recommendationReason"`
	Confidence           string                   `json:"confidence"`
	Limitations          []string                 `json:"limitations"`
	SelectedSkills       []ResearchSkillSelection `json:"selectedSkills"`
	Clarification        *ResearchClarification   `json:"clarification,omitempty"`
}

// ResearchClarification is an optional, structured decision gate emitted by
// the research planner only when the user's initial idea leaves a
// high-impact design choice unresolved. The host validates every option
// before accepting an answer; free-form route definitions are never accepted
// from the UI.
type ResearchClarification struct {
	NeedsUserInput bool                            `json:"needsUserInput"`
	Intro          string                          `json:"intro,omitempty"`
	Questions      []ResearchClarificationQuestion `json:"questions,omitempty"`
}

type ResearchClarificationQuestion struct {
	Kind          string                        `json:"kind,omitempty"`
	ID            string                        `json:"id"`
	Text          string                        `json:"text"`
	SelectionMode string                        `json:"selectionMode,omitempty"`
	Required      bool                          `json:"required"`
	Impact        string                        `json:"impact,omitempty"`
	Options       []ResearchClarificationOption `json:"options"`
}

type ResearchClarificationOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type StarterSkillLoader interface {
	ListRunSkillSnapshots(ctx context.Context, runID string) ([]skillrun.Snapshot, error)
}

type StartResearchCommand struct {
	ProjectID      string                  `json:"projectId"`
	ResearchIdea   string                  `json:"researchIdea"`
	ModelProfileID string                  `json:"modelProfileId"`
	ModelID        string                  `json:"modelId"`
	ReasoningLevel modelcap.ReasoningLevel `json:"reasoningLevel,omitempty"`
}

type AnswerResearchClarificationCommand struct {
	ProjectID    string              `json:"projectId"`
	StarterRunID string              `json:"starterRunId"`
	Answers      map[string][]string `json:"answers"`
}

type AdoptResearchRouteCommand struct {
	ProjectID string `json:"projectId"`
	RunID     string `json:"runId"`
	RouteID   string `json:"routeId"`
}

// StartAdoptedResearchRouteCommand starts the formal Run created by a prior
// route adoption.  The starter Run and route ID are the durable link between
// planning and execution; callers never supply the internal CreationKey.
type StartAdoptedResearchRouteCommand struct {
	ProjectID         string                      `json:"projectId"`
	StarterRunID      string                      `json:"starterRunId"`
	RouteID           string                      `json:"routeId"`
	WorkflowID        string                      `json:"workflowId"`
	WorkflowVersionID string                      `json:"workflowVersionId"`
	Inputs            json.RawMessage             `json:"inputs"`
	PermissionMode    conversation.PermissionMode `json:"permissionMode,omitempty"`
	ModelProfileID    string                      `json:"modelProfileId,omitempty"`
	ModelID           string                      `json:"modelId,omitempty"`
	ReasoningLevel    modelcap.ReasoningLevel     `json:"reasoningLevel,omitempty"`
}

type AdoptResearchRouteResult struct {
	Workflow       SaveResult      `json:"workflow"`
	TemplateID     string          `json:"templateId"`
	InitialInputs  json.RawMessage `json:"initialInputs"`
	ResearchIdea   string          `json:"researchIdea"`
	AvailableNow   bool            `json:"availableNow"`
	StarterRunID   string          `json:"starterRunId"`
	ResearchTaskID string          `json:"researchTaskId"`
	RouteID        string          `json:"routeId"`
	Run            *RunDetail      `json:"run,omitempty"`
}

type StarterService struct {
	workflows   *Service
	runtime     *RuntimeService
	projects    ProjectLoader
	attachments StarterAttachmentLoader
	knowledge   StarterKnowledgeLoader
	skills      StarterSkillLoader
	adoptMu     sync.Mutex
}

func NewStarterService(workflows *Service, runtime *RuntimeService, projects ProjectLoader, attachments StarterAttachmentLoader, knowledgeLoader StarterKnowledgeLoader, skills StarterSkillLoader) (*StarterService, error) {
	if workflows == nil || runtime == nil || projects == nil || attachments == nil || knowledgeLoader == nil || skills == nil {
		return nil, fmt.Errorf("research starter is not configured")
	}
	return &StarterService{workflows: workflows, runtime: runtime, projects: projects, attachments: attachments, knowledge: knowledgeLoader, skills: skills}, nil
}

func (s *StarterService) Start(ctx context.Context, command StartResearchCommand) (RunDetail, error) {
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.ResearchIdea = strings.TrimSpace(command.ResearchIdea)
	command.ModelProfileID = strings.TrimSpace(command.ModelProfileID)
	command.ModelID = strings.TrimSpace(command.ModelID)
	if command.ProjectID == "" || command.ResearchIdea == "" {
		return RunDetail{}, fmt.Errorf("项目和研究想法不能为空")
	}
	if len([]rune(command.ResearchIdea)) > maxResearchIdeaRunes {
		return RunDetail{}, fmt.Errorf("研究想法不能超过 %d 个字符", maxResearchIdeaRunes)
	}
	if command.ModelProfileID == "" || command.ModelID == "" {
		return RunDetail{}, fmt.Errorf("请先选择用于研究规划的 AI 模型")
	}
	snapshot, err := s.snapshot(ctx, command.ProjectID)
	if err != nil {
		return RunDetail{}, err
	}
	starter := ResearchStarterTemplate()
	saved, err := s.workflows.save(ctx, SaveCommand{ProjectID: command.ProjectID, Definition: starter.Definition}, PurposeResearchStarter)
	if err != nil {
		return RunDetail{}, fmt.Errorf("prepare research starter: %w", err)
	}
	inputs, err := json.Marshal(map[string]any{"starter_context": ResearchStarterContext{
		ResearchIdea: command.ResearchIdea, ResourceSnapshot: snapshot,
		StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: dynamicResearchPlannerVersion,
	}})
	if err != nil {
		return RunDetail{}, err
	}
	return s.runtime.Start(ctx, StartCommand{
		ProjectID: command.ProjectID, ResearchTaskID: NewResearchTaskID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID,
		Inputs: inputs, PermissionMode: conversation.PermissionFullAccess,
		ModelProfileID: command.ModelProfileID, ModelID: command.ModelID, ReasoningLevel: command.ReasoningLevel,
	})
}

// AnswerClarification starts a fresh, immutable planner attempt for the same
// research task. The prior planner result remains available in the audit
// history, while the new attempt receives the user's validated choices as
// additional research context.
func (s *StarterService) AnswerClarification(ctx context.Context, command AnswerResearchClarificationCommand) (RunDetail, error) {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.StarterRunID = strings.TrimSpace(command.StarterRunID)
	if command.ProjectID == "" || command.StarterRunID == "" {
		return RunDetail{}, fmt.Errorf("project and starter Run are required")
	}
	if len(command.Answers) > 32 {
		return RunDetail{}, fmt.Errorf("too many clarification answers")
	}
	prior, err := s.runtime.Get(ctx, command.ProjectID, command.StarterRunID)
	if err != nil {
		return RunDetail{}, err
	}
	if prior.Run.WorkflowPurpose != PurposeResearchStarter || prior.Run.Status != RunCompleted {
		return RunDetail{}, fmt.Errorf("research starter Run is not completed")
	}
	starterContext, plan, execution, err := decodeStarterResult(prior)
	if err != nil {
		return RunDetail{}, err
	}
	if plan.Clarification == nil || !plan.Clarification.NeedsUserInput {
		return RunDetail{}, fmt.Errorf("current research plan does not require clarification")
	}
	if err := validateResearchClarification(*plan.Clarification); err != nil {
		return RunDetail{}, err
	}
	answers, err := normalizeResearchClarificationAnswers(*plan.Clarification, command.Answers)
	if err != nil {
		return RunDetail{}, err
	}
	idea := clarifiedResearchIdea(starterContext.ResearchIdea, *plan.Clarification, answers)
	taskID := strings.TrimSpace(prior.Run.ResearchTaskID)
	if taskID == "" {
		return RunDetail{}, fmt.Errorf("research starter has no durable task identity; create a new research task")
	}
	snapshot, err := s.snapshotForTask(ctx, command.ProjectID, taskID)
	if err != nil {
		return RunDetail{}, err
	}
	return s.startReplannedResearch(ctx, prior, execution, idea, snapshot, answers, "answer")
}

func validateResearchClarification(value ResearchClarification) error {
	if !value.NeedsUserInput {
		if len(value.Questions) != 0 {
			return fmt.Errorf("non-blocking clarification must not contain questions")
		}
		return nil
	}
	if len(value.Questions) == 0 || len(value.Questions) > 12 {
		return fmt.Errorf("clarification must contain 1-12 questions")
	}
	seen := map[string]bool{}
	for _, question := range value.Questions {
		if question.Kind != "" && question.Kind != "research_direction" {
			return fmt.Errorf("clarification 仅用于研究方向决策；文件上传和依赖准备应在采纳路线后完成，请将资源缺口放入路线 requiredResources/blockers")
		}
		question.ID, question.Text = strings.TrimSpace(question.ID), strings.TrimSpace(question.Text)
		if !validClarificationID(question.ID) || seen[question.ID] || question.Text == "" || len(question.Options) < 2 || len(question.Options) > 8 {
			return fmt.Errorf("clarification question is invalid")
		}
		seen[question.ID] = true
		if err := validateClarificationSelection(question); err != nil {
			return err
		}
		optionIDs := map[string]bool{}
		for _, option := range question.Options {
			option.ID, option.Label = strings.TrimSpace(option.ID), strings.TrimSpace(option.Label)
			if !validClarificationID(option.ID) || option.Label == "" || optionIDs[option.ID] {
				return fmt.Errorf("clarification option is invalid")
			}
			optionIDs[option.ID] = true
		}
	}
	return nil
}

func validClarificationID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9' && index > 0) || (index > 0 && (character == '-' || character == '_')) {
			continue
		}
		return false
	}
	return true
}

func normalizeResearchClarificationAnswers(clarification ResearchClarification, answers map[string][]string) (map[string][]string, error) {
	if err := validateResearchClarification(clarification); err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(clarification.Questions))
	known := make(map[string]ResearchClarificationQuestion, len(clarification.Questions))
	for _, question := range clarification.Questions {
		known[question.ID] = question
	}
	for questionID := range answers {
		if _, ok := known[questionID]; !ok {
			return nil, fmt.Errorf("clarification contains an unknown question")
		}
	}
	for _, question := range clarification.Questions {
		values := answers[question.ID]
		if question.Required && len(values) == 0 {
			return nil, fmt.Errorf("clarification question %q requires an answer", question.ID)
		}
		if question.SelectionMode != "multiple" && len(values) > 1 {
			return nil, fmt.Errorf("clarification questions accept one option")
		}
		allowed := make(map[string]bool, len(question.Options))
		for _, option := range question.Options {
			allowed[option.ID] = true
		}
		seen := map[string]bool{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if !allowed[value] || seen[value] {
				return nil, fmt.Errorf("clarification answer contains an invalid option")
			}
			seen[value] = true
		}
		// Canonical option order makes equivalent multi-select answers replayable.
		for _, option := range question.Options {
			if seen[option.ID] {
				result[question.ID] = append(result[question.ID], option.ID)
			}
		}
	}
	return result, nil
}

func clarifiedResearchIdea(original string, clarification ResearchClarification, answers map[string][]string) string {
	var result strings.Builder
	result.WriteString(strings.TrimSpace(original))
	result.WriteString("\n\nUser-selected research boundary decisions:\n")
	for _, question := range clarification.Questions {
		values := answers[question.ID]
		labels := make([]string, 0, len(values))
		for _, value := range values {
			for _, option := range question.Options {
				if option.ID == value {
					labels = append(labels, option.Label)
					break
				}
			}
		}
		if len(labels) > 0 {
			fmt.Fprintf(&result, "- %s: %s\n", question.Text, strings.Join(labels, ", "))
		}
	}
	return result.String()
}

// ProjectPlan validates a completed starter Run for presentation. Adoption
// still calls the same validator immediately before creating a formal Run;
// this method is only an early, truthful UI projection.
func (s *StarterService) ProjectPlan(ctx context.Context, detail RunDetail) (*ResearchStarterPlan, error) {
	if detail.Run.WorkflowPurpose != PurposeResearchStarter || detail.Run.Status != RunCompleted {
		return nil, nil
	}
	starterContext, plan, _, err := decodeStarterResult(detail)
	if err != nil {
		return nil, err
	}
	if plan.Clarification != nil {
		if err := validateResearchClarification(*plan.Clarification); err != nil {
			return nil, err
		}
	}
	loaded, err := s.loadStarterSkills(ctx, detail)
	if err != nil {
		return nil, err
	}
	plan.LoadedSkills = append([]skillrun.Snapshot(nil), loaded...)
	if err := annotateStarterRoutesForSelection(&plan, starterContext, loaded); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (s *StarterService) Adopt(ctx context.Context, command AdoptResearchRouteCommand) (AdoptResearchRouteResult, error) {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()

	command.ProjectID, command.RunID, command.RouteID = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.RunID), strings.TrimSpace(command.RouteID)
	if command.ProjectID == "" || command.RunID == "" || command.RouteID == "" {
		return AdoptResearchRouteResult{}, fmt.Errorf("项目、研究启动任务和候选路线不能为空")
	}
	detail, err := s.runtime.Get(ctx, command.ProjectID, command.RunID)
	if err != nil {
		return AdoptResearchRouteResult{}, err
	}
	if detail.Run.WorkflowPurpose != PurposeResearchStarter {
		return AdoptResearchRouteResult{}, fmt.Errorf("只有 AI 研究启动任务可以采纳候选路线")
	}
	owner, err := s.workflows.Get(ctx, command.ProjectID, detail.Run.WorkflowID)
	if err != nil || owner.Workflow.Purpose != PurposeResearchStarter {
		return AdoptResearchRouteResult{}, fmt.Errorf("研究启动任务的系统 Workflow 归属无效")
	}
	if detail.Run.Status != RunCompleted {
		return AdoptResearchRouteResult{}, fmt.Errorf("AI 研究启动尚未完成，不能采纳路线")
	}
	// Resolve and validate the durable task owner from the project-scoped
	// planner Run so later file staging and execution share one owner.
	researchTaskID, err := s.runtime.resolveResearchTaskForRun(ctx, command.ProjectID, detail.Run)
	if err != nil {
		return AdoptResearchRouteResult{}, err
	}
	if code, snapshotErr := s.runtime.validateRunSnapshots(ctx, detail); snapshotErr != nil {
		return AdoptResearchRouteResult{}, fmt.Errorf("%s: %w", code, snapshotErr)
	}
	starterContext, plan, loadedSkills, err := s.selectionStarterResult(ctx, detail)
	if err != nil {
		return AdoptResearchRouteResult{}, err
	}
	if plan.Clarification != nil && plan.Clarification.NeedsUserInput {
		return AdoptResearchRouteResult{}, fmt.Errorf("research route requires the user's clarification choices before adoption")
	}
	var selected *ResearchRoute
	for index := range plan.Routes {
		if plan.Routes[index].RouteID == command.RouteID {
			selected = &plan.Routes[index]
			break
		}
	}
	if selected == nil {
		return AdoptResearchRouteResult{}, fmt.Errorf("所选路线不在本次 AI 冻结的候选结果中")
	}
	if selected.Validation == "invalid" {
		return AdoptResearchRouteResult{}, fmt.Errorf("所选路线 %q 无法采纳：%s", selected.Title, nonEmptyMessage(selected.ValidationError, "路线未通过宿主校验"))
	}
	// Re-derive the route-local Skill snapshot immediately before compiling the
	// formal Workflow. The projection is only a UI aid; this check is the final
	// adoption boundary for the selected candidate.
	selected.SelectedSkills, err = validatedResearchSkillsForRoute(plan, *selected, loadedSkills)
	if err != nil {
		return AdoptResearchRouteResult{}, fmt.Errorf("所选路线的 Skill 声明无效：%w", err)
	}
	template, initialInputs, availableNow, err := routeDefinition(*selected, starterContext)
	if err != nil {
		return AdoptResearchRouteResult{}, err
	}
	// Adoption is replayable across application restarts.  The route adoption
	// event is the durable hand-off between the planner Run and the formal
	// Workflow; reuse it instead of creating another saved plan when the user
	// clicks the same route again.
	if adopted, ok := adoptedRouteRecord(detail.Events, command.RouteID); ok {
		if adopted.WorkflowID != "" {
			if existing, getErr := s.workflows.Get(ctx, command.ProjectID, adopted.WorkflowID); getErr == nil {
				if existing.Workflow.Purpose != PurposeUserPlan {
					return AdoptResearchRouteResult{}, fmt.Errorf("adopted route Workflow is not a user plan")
				}
				version, versionOK := selectVersion(existing, adopted.WorkflowVersionID)
				if !versionOK {
					return AdoptResearchRouteResult{}, fmt.Errorf("adopted route Workflow version not found")
				}
				if err := VerifyVersionSnapshot(version); err != nil {
					return AdoptResearchRouteResult{}, fmt.Errorf("adopted route Workflow snapshot is invalid: %w", err)
				}
				result := AdoptResearchRouteResult{
					Workflow: SaveResult{Workflow: existing.Workflow, Version: version}, TemplateID: adopted.TemplateID, InitialInputs: initialInputs,
					ResearchIdea: starterContext.ResearchIdea, AvailableNow: availableNow,
					StarterRunID: command.RunID, ResearchTaskID: researchTaskID, RouteID: command.RouteID,
				}
				if adopted.FormalRunID != "" {
					if formal, runErr := s.runtime.Get(ctx, command.ProjectID, adopted.FormalRunID); runErr == nil {
						result.Run = &formal
						if strings.TrimSpace(result.ResearchTaskID) == "" {
							result.ResearchTaskID = strings.TrimSpace(formal.Run.ResearchTaskID)
						}
					}
				}
				return result, nil
			} else if !strings.Contains(strings.ToLower(getErr.Error()), "workflow not found") {
				return AdoptResearchRouteResult{}, fmt.Errorf("load adopted route Workflow: %w", getErr)
			}
		}
	}
	if err := s.ensureCurrentStarter(ctx, detail); err != nil {
		return AdoptResearchRouteResult{}, err
	}
	saved, err := s.workflows.Save(ctx, SaveCommand{ProjectID: command.ProjectID, Definition: template.Definition})
	if err != nil {
		return AdoptResearchRouteResult{}, err
	}
	result := AdoptResearchRouteResult{Workflow: saved, TemplateID: template.ID, InitialInputs: initialInputs, ResearchIdea: starterContext.ResearchIdea, AvailableNow: availableNow, StarterRunID: command.RunID, ResearchTaskID: researchTaskID, RouteID: command.RouteID}
	if !availableNow {
		if err := s.recordAdoptionEvent(ctx, detail, command.RouteID, template.ID, saved.Workflow.ID, saved.Version.ID, "", command.RouteID == plan.RecommendedRouteID, false); err != nil {
			return AdoptResearchRouteResult{}, err
		}
		return result, nil
	}
	execution := starterAIExecution(detail)
	if execution == nil {
		if saved.Created {
			if rollbackErr := s.workflows.deleteUnstarted(ctx, command.ProjectID, saved.Workflow.ID, saved.Version.ID); rollbackErr != nil {
				return AdoptResearchRouteResult{}, fmt.Errorf("研究启动任务当前尝试没有可复用的模型快照（回滚失败：%v）", rollbackErr)
			}
		}
		return AdoptResearchRouteResult{}, fmt.Errorf("研究启动任务当前尝试没有可复用的模型快照")
	}
	started, err := s.runtime.Start(ctx, StartCommand{
		ProjectID: command.ProjectID, ResearchTaskID: researchTaskID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID,
		Inputs: initialInputs, PermissionMode: conversation.PermissionFullAccess,
		ModelProfileID: execution.ModelProfileID, ModelID: execution.ModelID, ReasoningLevel: execution.ReasoningLevel,
		CreationKey: adoptedResearchRouteCreationPrefix + command.RunID + ":" + command.RouteID,
	})
	if err != nil {
		// SaveVersion and Runtime.Start cannot share one SQLite transaction.
		// Compensate only this exact, still-unused Workflow so a failed launch
		// does not appear later as a reusable ghost plan. If another actor has
		// already used or edited it, preserve it rather than deleting data.
		if saved.Created {
			if rollbackErr := s.workflows.deleteUnstarted(ctx, command.ProjectID, saved.Workflow.ID, saved.Version.ID); rollbackErr != nil {
				return AdoptResearchRouteResult{}, fmt.Errorf("start adopted research route: %w (rollback failed: %v)", err, rollbackErr)
			}
		}
		return AdoptResearchRouteResult{}, fmt.Errorf("start adopted research route: %w", err)
	}
	result.Run = &started
	if err := s.recordAdoptionEvent(ctx, detail, command.RouteID, template.ID, saved.Workflow.ID, saved.Version.ID, started.Run.ID, command.RouteID == plan.RecommendedRouteID, true); err != nil {
		return AdoptResearchRouteResult{}, err
	}
	return result, nil
}

// PendingAdoption restores an existing upload checkpoint without adopting,
// starting a run, or asking the model to regenerate the frozen route.
func (s *StarterService) PendingAdoption(ctx context.Context, projectID, runID string) (*AdoptResearchRouteResult, error) {
	detail, err := s.runtime.Get(ctx, projectID, runID)
	if err != nil {
		return nil, err
	}
	if detail.Run.WorkflowPurpose != PurposeResearchStarter || detail.Run.Status != RunCompleted {
		return nil, nil
	}
	if err := s.ensureCurrentStarter(ctx, detail); err != nil {
		return nil, nil
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		if detail.Events[i].Type != "research.route_adopted" {
			continue
		}
		var payload struct {
			RouteID     string `json:"routeId"`
			FormalRunID string `json:"formalRunId"`
		}
		if err := json.Unmarshal(detail.Events[i].Payload, &payload); err != nil {
			return nil, err
		}
		if payload.FormalRunID != "" {
			return nil, nil
		}
		record, found := adoptedRouteRecord(detail.Events, payload.RouteID)
		if !found {
			return nil, fmt.Errorf("adopted route record missing")
		}
		owner, err := s.workflows.Get(ctx, projectID, record.WorkflowID)
		if err != nil {
			return nil, err
		}
		version, ok := selectVersion(owner, record.WorkflowVersionID)
		if !ok {
			return nil, fmt.Errorf("adopted route version missing")
		}
		if err := VerifyVersionSnapshot(version); err != nil {
			return nil, err
		}
		starter, plan, loaded, err := s.selectionStarterResult(ctx, detail)
		if err != nil {
			return nil, err
		}
		for _, route := range plan.Routes {
			if route.RouteID != payload.RouteID {
				continue
			}
			route.SelectedSkills, err = validatedResearchSkillsForRoute(plan, route, loaded)
			if err != nil {
				return nil, err
			}
			_, inputs, _, err := routeDefinition(route, starter)
			if err != nil {
				return nil, err
			}
			return &AdoptResearchRouteResult{Workflow: SaveResult{Workflow: owner.Workflow, Version: version}, TemplateID: record.TemplateID, InitialInputs: inputs, ResearchIdea: starter.ResearchIdea, StarterRunID: runID, ResearchTaskID: detail.Run.ResearchTaskID, RouteID: payload.RouteID}, nil
		}
		return nil, fmt.Errorf("adopted route missing from frozen plan")
	}
	return nil, nil
}

// StartAdoptedResearchRoute starts a route that was adopted earlier but was
// waiting for a required resource (normally a tabular input).  It repeats all
// adoption checks against the immutable planner output before delegating to
// the normal Runtime.Start path.  This keeps the planning and formal Runs
// under the same internal task identity and idempotency key.
func (s *StarterService) StartAdoptedResearchRoute(ctx context.Context, command StartAdoptedResearchRouteCommand) (RunDetail, error) {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()

	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.StarterRunID = strings.TrimSpace(command.StarterRunID)
	command.RouteID = strings.TrimSpace(command.RouteID)
	command.WorkflowID = strings.TrimSpace(command.WorkflowID)
	command.WorkflowVersionID = strings.TrimSpace(command.WorkflowVersionID)
	if command.ProjectID == "" || command.StarterRunID == "" || command.RouteID == "" || command.WorkflowID == "" {
		return RunDetail{}, fmt.Errorf("project, starter Run, route and Workflow are required")
	}
	starter, err := s.runtime.Get(ctx, command.ProjectID, command.StarterRunID)
	if err != nil {
		return RunDetail{}, err
	}
	if starter.Run.WorkflowPurpose != PurposeResearchStarter || starter.Run.Status != RunCompleted {
		return RunDetail{}, fmt.Errorf("AI research starter Run is not completed")
	}
	starterContext, plan, loadedSkills, err := s.selectionStarterResult(ctx, starter)
	if err != nil {
		return RunDetail{}, err
	}
	var selected *ResearchRoute
	for index := range plan.Routes {
		if plan.Routes[index].RouteID == command.RouteID {
			selected = &plan.Routes[index]
			break
		}
	}
	if selected == nil {
		return RunDetail{}, fmt.Errorf("selected route is not part of the frozen planner result")
	}
	if selected.Validation == "invalid" {
		return RunDetail{}, fmt.Errorf("selected route is invalid: %s", nonEmptyMessage(selected.ValidationError, "route validation failed"))
	}
	adoptedWorkflow, adopted := adoptedRouteWorkflow(starter.Events, command.RouteID)
	if !adopted {
		return RunDetail{}, fmt.Errorf("route has not been adopted for this research task")
	}
	adoptedVersion, _ := adoptedRouteVersion(starter.Events, command.RouteID)
	if adoptedWorkflow == "" || adoptedVersion == "" {
		return RunDetail{}, fmt.Errorf("adopted route is missing its immutable Workflow version binding")
	}
	if adoptedWorkflow != "" && adoptedWorkflow != command.WorkflowID {
		return RunDetail{}, fmt.Errorf("adopted route Workflow does not match the frozen route")
	}
	if adoptedVersion != "" && adoptedVersion != command.WorkflowVersionID {
		return RunDetail{}, fmt.Errorf("adopted route Workflow version does not match the frozen route")
	}
	selected.SelectedSkills, err = validatedResearchSkillsForRoute(plan, *selected, loadedSkills)
	if err != nil {
		return RunDetail{}, err
	}
	template, canonicalInitialInputs, _, err := routeDefinition(*selected, starterContext)
	if err != nil {
		return RunDetail{}, err
	}
	owned, err := s.workflows.Get(ctx, command.ProjectID, command.WorkflowID)
	if err != nil {
		return RunDetail{}, err
	}
	if owned.Workflow.Purpose != PurposeUserPlan {
		return RunDetail{}, fmt.Errorf("adopted route Workflow is not a user plan")
	}
	version, ok := selectVersion(owned, command.WorkflowVersionID)
	if !ok {
		return RunDetail{}, fmt.Errorf("adopted route Workflow version not found")
	}
	if err := VerifyVersionSnapshot(version); err != nil {
		return RunDetail{}, fmt.Errorf("adopted route Workflow snapshot is invalid: %w", err)
	}
	// The durable adoption event binds this exact immutable Workflow version.
	// Recompiling today's route generator here breaks pending tasks after an
	// application update, even though their saved version is valid. Runtime.Start
	// still verifies the saved compilation and every frozen tool contract.
	var provided map[string]json.RawMessage
	if err := json.Unmarshal(command.Inputs, &provided); err != nil {
		return RunDetail{}, fmt.Errorf("adopted route inputs must be a JSON object: %w", err)
	}
	if provided == nil {
		return RunDetail{}, fmt.Errorf("adopted route inputs must be a JSON object, not null")
	}
	// The route context and research question are immutable planner output.
	// Canonicalize these two fields from the host-validated route before starting
	// the formal Run; browser form state is not authoritative. The user-controlled
	// input_paths value is intentionally preserved.
	var canonical map[string]json.RawMessage
	if err := json.Unmarshal(canonicalInitialInputs, &canonical); err != nil {
		return RunDetail{}, fmt.Errorf("adopted route canonical inputs are invalid: %w", err)
	}
	for _, name := range []string{"research_goal", "route_context"} {
		if raw := canonical[name]; len(raw) > 0 {
			provided[name] = cloneRaw(raw)
		}
	}
	canonicalInputs, err := json.Marshal(provided)
	if err != nil {
		return RunDetail{}, fmt.Errorf("encode adopted route inputs: %w", err)
	}
	if command.PermissionMode == "" {
		command.PermissionMode = conversation.PermissionFullAccess
	}
	// The starter Run is the authority for task ownership. Do not trust the
	// browser's pending task id and do not let Runtime.Start discover a mismatch
	// only after the user has selected a file.
	researchTaskID, err := s.runtime.resolveResearchTaskForRun(ctx, command.ProjectID, starter.Run)
	if err != nil {
		return RunDetail{}, err
	}
	creationKey := adoptedResearchRouteCreationPrefix + command.StarterRunID + ":" + command.RouteID
	if _, found, err := s.runtime.repository.GetRunByCreationKey(ctx, command.ProjectID, creationKey); err != nil {
		return RunDetail{}, err
	} else if !found {
		if err := s.ensureCurrentStarter(ctx, starter); err != nil {
			return RunDetail{}, err
		}
	}
	started, err := s.runtime.Start(ctx, StartCommand{
		ProjectID: command.ProjectID, ResearchTaskID: researchTaskID, WorkflowID: command.WorkflowID, WorkflowVersionID: version.ID,
		Inputs: canonicalInputs, PermissionMode: command.PermissionMode,
		ModelProfileID: command.ModelProfileID, ModelID: command.ModelID, ReasoningLevel: command.ReasoningLevel,
		CreationKey: creationKey,
	})
	if err != nil {
		return RunDetail{}, err
	}
	if err := s.recordAdoptionEvent(ctx, starter, command.RouteID, template.ID, command.WorkflowID, version.ID, started.Run.ID, command.RouteID == plan.RecommendedRouteID, true); err != nil {
		return RunDetail{}, err
	}
	return started, nil
}

func templateRouteContext(route ResearchRoute) json.RawMessage {
	value, _ := json.Marshal(map[string]any{
		"routeId": route.RouteID, "title": route.Title, "reason": route.Reason,
		"layers": route.Layers, "selectedSkills": route.SelectedSkills,
		"reviewCheckpoints": route.ReviewCheckpoints, "planningNotes": route.PlanningNotes,
	})
	return value
}

func adoptedRouteWorkflow(events []RuntimeEvent, routeID string) (string, bool) {
	record, found := adoptedRouteRecord(events, routeID)
	return record.WorkflowID, found
}

func adoptedRouteVersion(events []RuntimeEvent, routeID string) (string, bool) {
	record, found := adoptedRouteRecord(events, routeID)
	return record.WorkflowVersionID, found
}

type adoptedRoute struct {
	WorkflowID        string
	WorkflowVersionID string
	TemplateID        string
	FormalRunID       string
}

func adoptedRouteRecord(events []RuntimeEvent, routeID string) (adoptedRoute, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "research.route_adopted" {
			continue
		}
		var payload struct {
			RouteID           string `json:"routeId"`
			WorkflowID        string `json:"workflowId"`
			WorkflowVersionID string `json:"workflowVersionId"`
			TemplateID        string `json:"templateId"`
			FormalRunID       string `json:"formalRunId"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.RouteID == routeID {
			return adoptedRoute{
				WorkflowID: strings.TrimSpace(payload.WorkflowID), WorkflowVersionID: strings.TrimSpace(payload.WorkflowVersionID),
				TemplateID: strings.TrimSpace(payload.TemplateID), FormalRunID: strings.TrimSpace(payload.FormalRunID),
			}, true
		}
	}
	return adoptedRoute{}, false
}

func (s *StarterService) recordAdoptionEvent(ctx context.Context, starter RunDetail, routeID, templateID, workflowID, workflowVersionID, formalRunID string, recommended, availableNow bool) error {
	latest, found := adoptedRouteRecord(starter.Events, routeID)
	if found && latest.WorkflowID == strings.TrimSpace(workflowID) && latest.WorkflowVersionID == strings.TrimSpace(workflowVersionID) && latest.FormalRunID == strings.TrimSpace(formalRunID) {
		return nil
	}
	payload := map[string]any{
		"routeId": routeID, "templateId": templateID, "workflowId": workflowID, "workflowVersionId": workflowVersionID,
		"availableNow": availableNow, "recommended": recommended,
	}
	if formalRunID != "" {
		payload["formalRunId"] = formalRunID
	}
	event, err := s.runtime.event(starter.Run.ID, "research.route_adopted", payload, s.runtime.now())
	if err != nil {
		return err
	}
	if err := s.runtime.repository.RecordEvent(ctx, event); err != nil {
		return fmt.Errorf("record adopted research route: %w", err)
	}
	return nil
}

func (s *StarterService) snapshot(ctx context.Context, projectID string) (ResourceSnapshot, error) {
	return s.snapshotForTask(ctx, projectID, "")
}

func (s *StarterService) snapshotForTask(ctx context.Context, projectID, taskID string) (ResourceSnapshot, error) {
	var attachments []attachment.Attachment
	var err error
	if taskLister, ok := s.attachments.(interface {
		ListForTask(context.Context, string, string) ([]attachment.Attachment, error)
	}); ok && taskID != "" {
		attachments, err = taskLister.ListForTask(ctx, projectID, taskID)
	} else {
		attachments, err = s.attachments.List(ctx, projectID)
	}
	if err != nil {
		return ResourceSnapshot{}, fmt.Errorf("list project attachments: %w", err)
	}
	var documents []knowledge.Document
	if taskLister, ok := s.knowledge.(interface {
		ListDocumentsForTask(context.Context, string, string) ([]knowledge.Document, error)
	}); ok && taskID != "" {
		documents, err = taskLister.ListDocumentsForTask(ctx, projectID, taskID)
	} else if sharedLister, ok := s.knowledge.(interface {
		ListProjectSharedDocuments(context.Context, string) ([]knowledge.Document, error)
	}); ok {
		documents, err = sharedLister.ListProjectSharedDocuments(ctx, projectID)
	} else {
		documents, err = s.knowledge.ListDocuments(ctx, projectID)
	}
	if err != nil {
		return ResourceSnapshot{}, fmt.Errorf("list project knowledge documents: %w", err)
	}
	// Only explicitly shared material is eligible before a task exists. Unscoped
	// rows and rows owned by another task require an explicit user import or
	// promotion and must not influence route selection.
	sharedAttachments := make([]attachment.Attachment, 0, len(attachments))
	for _, value := range attachments {
		if value.ScopeKind == attachment.ScopeProjectShared || taskID != "" && value.ScopeKind == attachment.ScopeTask && value.ResearchTaskID == taskID {
			sharedAttachments = append(sharedAttachments, value)
		}
	}
	sharedDocuments := make([]knowledge.Document, 0, len(documents))
	for _, value := range documents {
		if value.ScopeKind == attachment.ScopeProjectShared || taskID != "" && value.ScopeKind == attachment.ScopeTask && value.ResearchTaskID == taskID {
			sharedDocuments = append(sharedDocuments, value)
		}
	}
	result := ResourceSnapshot{AttachmentFormats: map[string]int{}, AttachmentNames: []string{}, TabularFiles: []string{}}
	sort.Slice(sharedAttachments, func(i, j int) bool { return sharedAttachments[i].ID < sharedAttachments[j].ID })
	sort.Slice(sharedDocuments, func(i, j int) bool { return sharedDocuments[i].ID < sharedDocuments[j].ID })
	fingerprint, _ := json.Marshal(struct {
		Attachments []attachment.Attachment
		Documents   []knowledge.Document
	}{sharedAttachments, sharedDocuments})
	result.ResourceFingerprint = hashJSON(fingerprint)
	result.AttachmentCount, result.KnowledgeDocumentCount = len(sharedAttachments), len(sharedDocuments)
	for _, value := range sharedAttachments {
		result.AttachmentFormats[string(value.Format)]++
		if value.Status == attachment.StatusReady {
			result.ReadyAttachmentCount++
		}
		if len(result.AttachmentNames) < 100 {
			result.AttachmentNames = append(result.AttachmentNames, value.OriginalName)
		}
		if value.Status == attachment.StatusReady && len(result.TabularFiles) < 100 && (value.Format == document.FormatCSV || value.Format == document.FormatText && strings.EqualFold(filepath.Ext(value.OriginalName), ".tsv") || value.Format == document.FormatXLSX) {
			result.TabularFiles = append(result.TabularFiles, value.OriginalName)
		}
	}
	for _, value := range sharedDocuments {
		if value.Status == knowledge.DocumentReady {
			result.ReadyKnowledgeCount++
		}
	}
	sort.Strings(result.AttachmentNames)
	sort.Strings(result.TabularFiles)
	// Do not scan the project root while planning a new task. Root files have
	// no trustworthy owner and historically caused one task to ingest another
	// task's CSV. Users can explicitly import a file as task-scoped or shared;
	// the resulting attachment then appears in this snapshot.
	return result, nil
}

// loadStarterSkills returns the immutable Skill snapshots attached to the
// planner's Chat Run. A missing Chat Run is allowed only when the planner did
// not claim to use any Skill; the route-level validator will make that rule
// explicit for each selected candidate.
func (s *StarterService) loadStarterSkills(ctx context.Context, detail RunDetail) ([]skillrun.Snapshot, error) {
	execution := starterAIExecution(detail)
	if execution == nil || execution.ChatRunID == "" {
		return nil, nil
	}
	loaded, err := s.skills.ListRunSkillSnapshots(ctx, execution.ChatRunID)
	if err != nil {
		return nil, fmt.Errorf("读取研究规划 Skill 快照: %w", err)
	}
	return loaded, nil
}

// selectionStarterResult verifies the immutable planner envelope, then marks
// each route independently. It deliberately does not apply the strict
// all-routes verifier: a bad alternative must remain visible as unavailable
// without preventing a different, valid route from being adopted.
func (s *StarterService) selectionStarterResult(ctx context.Context, detail RunDetail) (ResearchStarterContext, ResearchStarterPlan, []skillrun.Snapshot, error) {
	inputs, plan, _, err := decodeStarterResult(detail)
	if err != nil {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, err
	}
	loaded, err := s.loadStarterSkills(ctx, detail)
	if err != nil {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, err
	}
	if err := annotateStarterRoutesForSelection(&plan, inputs, loaded); err != nil {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, err
	}
	return inputs, plan, loaded, nil
}

// annotateStarterRoutesForSelection is the host projection used by both the
// UI and AdoptResearchRoute. Structural errors belong to the candidate that
// contains them; only an ambiguous plan envelope (for example duplicate route
// IDs or a missing recommendation target) fails the whole projection.
func annotateStarterRoutesForSelection(plan *ResearchStarterPlan, context ResearchStarterContext, loadedSkills []skillrun.Snapshot) error {
	if plan != nil && plan.Clarification != nil {
		if err := validateResearchClarification(*plan.Clarification); err != nil {
			return err
		}
	}
	if plan == nil {
		return fmt.Errorf("研究启动 AI 没有返回候选路线")
	}
	seenIDs := map[string]bool{}
	recommended := false
	for _, route := range plan.Routes {
		if route.RouteID == plan.RecommendedRouteID {
			recommended = true
		}
		if !validRouteID(route.RouteID) || seenIDs[route.RouteID] {
			return fmt.Errorf("研究启动 AI 返回了无效或重复的候选路线")
		}
		seenIDs[route.RouteID] = true
	}
	if len(plan.Routes) == 0 || !recommended {
		return fmt.Errorf("研究启动 AI 推荐的路线不在候选列表中")
	}
	// The projection must not invent a new candidate or silently replace the
	// recommendation. Missing-data routes stay adoptable as blocked plans;
	// design alternatives must come from the planner's frozen research intent.
	for index := range plan.Routes {
		route := &plan.Routes[index]
		route.Validation = ""
		route.ValidationError = ""
		// New planner output is semantic only. Compile it before any route
		// validation so the model never controls executable order or ports.
		if materialized, materializeErr := materializeSemanticResearchRoute(*route); materializeErr != nil {
			markInvalidResearchRoute(route, materializeErr.Error())
			continue
		} else {
			*route = materialized
		}
		available, blockers, routeErr := validateDynamicResearchRoute(*route, context)
		if routeErr == nil {
			selectedSkills, skillErr := validatedResearchSkillsForRoute(*plan, *route, loadedSkills)
			if skillErr != nil {
				routeErr = skillErr
			} else {
				route.SelectedSkills = selectedSkills
				// Keep the read-only plan projection truthful when a planner
				// omitted a Skill from its summary but the run snapshot proves
				// that it was actually loaded.
				for _, selected := range selectedSkills {
					found := false
					for _, existing := range plan.SelectedSkills {
						if existing.Name == selected.Name {
							found = true
							break
						}
					}
					if !found {
						plan.SelectedSkills = append(plan.SelectedSkills, selected)
					}
				}
			}
		}
		if routeErr != nil {
			markInvalidResearchRoute(route, routeErr.Error())
			continue
		}
		// Equal topology is not equal research: a cross-sectional survey and a
		// longitudinal diary can legitimately use the same host-owned stages.
		if !available {
			route.AvailableNow = false
			route.Blockers = appendUniqueStrings(route.Blockers, blockers...)
		}
		if !routeNeedsTabular(*route) {
			route.AvailableNow = true
			// Design and evidence work can begin before recruitment, approvals
			// or data collection. Retain those caveats rather than deleting them
			// or presenting them as prerequisites for drafting a protocol.
			route.PlanningNotes = appendUniqueStrings(route.PlanningNotes, route.Blockers...)
			route.Blockers = []string{}
		}
		if !route.AvailableNow && len(route.Blockers) == 0 {
			route.Blockers = appendUniqueStrings(nil, blockers...)
			if len(route.Blockers) == 0 {
				route.Blockers = []string{"当前路线仍有待补充的研究资源"}
			}
		}
		if route.AvailableNow && available {
			route.Validation = "ready"
		} else {
			route.Validation = "blocked"
		}
	}
	return nil
}

func markInvalidResearchRoute(route *ResearchRoute, message string) {
	if route == nil {
		return
	}
	route.AvailableNow = false
	route.Validation = "invalid"
	route.ValidationError = strings.TrimSpace(message)
	route.Blockers = appendUniqueStrings(route.Blockers, route.ValidationError)
}

// decodeStarterResult verifies the immutable Run/AI execution envelope and
// decodes the planner payload. Route semantics are deliberately checked by the
// caller: adoption is strict, while the UI projection annotates each route so
// one malformed candidate can be shown as unavailable without hiding others.
func decodeStarterResult(detail RunDetail) (ResearchStarterContext, ResearchStarterPlan, *AIExecution, error) {
	var inputs struct {
		StarterContext ResearchStarterContext `json:"starter_context"`
	}
	if err := json.Unmarshal(detail.Run.Inputs, &inputs); err != nil || strings.TrimSpace(inputs.StarterContext.ResearchIdea) == "" {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动任务的冻结输入无效")
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != StepCompleted {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动任务没有已完成的规划步骤")
	}
	execution := starterAIExecution(detail)
	if execution == nil {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动任务当前尝试没有 AI 输出快照")
	}
	if execution.Status != "completed" || execution.OutputSHA256 == "" || hashJSON(execution.Output) != execution.OutputSHA256 {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动 AI 输出快照校验失败")
	}
	var outputs map[string]json.RawMessage
	if json.Unmarshal(detail.Run.Outputs, &outputs) != nil || !rawJSONEqual(outputs["plan"], execution.Output) {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动 Run 输出与 AI 冻结输出不一致")
	}
	var plan ResearchStarterPlan
	if err := json.Unmarshal(execution.Output, &plan); err != nil {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("decode research starter plan: %w", err)
	}
	if inputs.StarterContext.PlannerVersion != dynamicResearchPlannerVersion {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动任务的规划器版本无效")
	}
	if !researchStageCatalogEqual(inputs.StarterContext.StageCatalog, dynamicResearchStageCatalog()) {
		return ResearchStarterContext{}, ResearchStarterPlan{}, nil, fmt.Errorf("研究启动任务的阶段目录快照无效")
	}
	return inputs.StarterContext, plan, execution, nil
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(append([]string(nil), values...), additions...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func starterAIExecution(detail RunDetail) *AIExecution {
	if len(detail.Steps) != 1 {
		return nil
	}
	step := detail.Steps[0]
	// Repository reads are ordered by creation time, so retries appear after
	// their failed predecessor. Select the newest completed execution for the
	// step attempt; choosing the first match would make a successful retry look
	// like a stale failed planner output and hide every route.
	var fallback *AIExecution
	for index := len(detail.AIExecutions) - 1; index >= 0; index-- {
		candidate := &detail.AIExecutions[index]
		if candidate.WorkflowStepID != step.ID || candidate.Attempt != step.Attempt {
			continue
		}
		if candidate.Status == "completed" {
			return candidate
		}
		if fallback == nil {
			fallback = candidate
		}
	}
	return fallback
}

func routeDefinition(route ResearchRoute, starter ResearchStarterContext) (Template, json.RawMessage, bool, error) {
	if starter.PlannerVersion != dynamicResearchPlannerVersion {
		return Template{}, nil, false, fmt.Errorf("候选路线必须由当前动态研究规划器生成")
	}
	materialized, err := materializeSemanticResearchRoute(route)
	if err != nil {
		return Template{}, nil, false, err
	}
	route = materialized
	hostAvailable, _, err := validateDynamicResearchRoute(route, starter)
	if err != nil {
		return Template{}, nil, false, err
	}
	// The resource snapshot only proves hard prerequisites such as whether a
	// table exists. It cannot prove that an arbitrary table belongs to the
	// current research question, so it must never promote an AI-blocked route.
	available := route.AvailableNow && hostAvailable
	return dynamicResearchRouteTemplate(route, starter, available)
}
