package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
)

type resourceMemoryRepository struct {
	sessions map[string]resource.Session
	actions  map[string]resource.Action
	fail     bool
}

func newResourceMemoryRepository() *resourceMemoryRepository {
	return &resourceMemoryRepository{map[string]resource.Session{}, map[string]resource.Action{}, false}
}
func (m *resourceMemoryRepository) GetSession(_ context.Context, id string) (resource.Session, bool, error) {
	s, ok := m.sessions[id]
	return s, ok, nil
}
func (m *resourceMemoryRepository) CreateSession(_ context.Context, s resource.Session, actions []resource.Action) error {
	if m.fail {
		return errors.New("storage failed")
	}
	m.sessions[s.Scope.RunID] = s
	for _, a := range actions {
		m.actions[a.RunID+":"+a.ID] = a
	}
	return nil
}
func (m *resourceMemoryRepository) PutActions(_ context.Context, s resource.Scope, actions []resource.Action) error {
	if m.fail {
		return errors.New("storage failed")
	}
	for _, a := range actions {
		if err := a.Validate(s); err != nil {
			return err
		}
		m.actions[a.RunID+":"+a.ID] = a
	}
	return nil
}
func (m *resourceMemoryRepository) GetAction(_ context.Context, r, id string) (resource.Action, bool, error) {
	a, ok := m.actions[r+":"+id]
	return a, ok, nil
}
func (m *resourceMemoryRepository) ListActions(_ context.Context, r string, limit int) ([]resource.Action, error) {
	out := []resource.Action{}
	for _, a := range m.actions {
		if a.RunID == r {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type resourceFixtureScope struct {
	root string
	task string
}

func (s *resourceFixtureScope) ProjectIDForRun(context.Context, string) (string, error) {
	return "project", nil
}
func (s *resourceFixtureScope) ResearchTaskIDForSubject(context.Context, tool.SubjectKind, string) (string, error) {
	return s.task, nil
}
func (s *resourceFixtureScope) WorkspaceRootForSubject(context.Context, tool.SubjectKind, string) (string, error) {
	return s.root, nil
}

func resourceFixture(t *testing.T) (*ResourceActions, *resourceMemoryRepository, *resourceFixtureScope, resource.Request) {
	t.Helper()
	root := t.TempDir()
	reg := tool.NewRegistry()
	p := projectFixture{project.Project{ID: "project", WorkspacePath: root}}
	for _, impl := range []tool.Tool{NewListWorkspace(p), NewReadText(p)} {
		if err := reg.Register(context.Background(), impl); err != nil {
			t.Fatal(err)
		}
	}
	repo := newResourceMemoryRepository()
	scope := &resourceFixtureScope{root, "task"}
	engine := NewResourceActions(repo, reg, scope, &compatibleSkillFixture{})
	for _, impl := range []tool.Tool{engine.OpenTool(), engine.SearchTool()} {
		if err := reg.Register(context.Background(), impl); err != nil {
			t.Fatal(err)
		}
	}
	defs, _ := reg.Definitions(context.Background())
	return engine, repo, scope, resource.Request{RunID: "run", ProjectID: "project", WorkflowStepID: "step", Definitions: defs}
}
func invokeResource(t *testing.T, r *ResourceActions, s *resourceFixtureScope, id string) (tool.Result, error) {
	t.Helper()
	return r.OpenTool().Invoke(context.Background(), tool.Invocation{RunID: "run", CallID: "call", SubjectKind: tool.SubjectChatRun, ProjectID: "project", ResearchTaskID: s.task, WorkspaceRoot: s.root, Arguments: resourceArgs(map[string]string{"actionId": id})})
}
func resourceActionByLabel(t *testing.T, repo *resourceMemoryRepository, label string) resource.Action {
	t.Helper()
	for _, a := range repo.actions {
		if strings.Contains(a.Seed.Label, label) {
			return a
		}
	}
	t.Fatalf("missing action %s", label)
	return resource.Action{}
}
func TestResourceInterfaceEmptyTaskHasNoPathOrReadActions(t *testing.T) {
	r, repo, _, req := resourceFixture(t)
	view, err := r.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OpenIDs) != 0 || len(view.Definitions) != 0 || len(repo.actions) != 0 {
		t.Fatalf("empty task exposes operations: %+v", view)
	}
	if !strings.Contains(view.Context, `"inputState":"no_supplied_resources"`) {
		t.Fatal("missing host input state")
	}
}
func TestResourceInterfaceDiscoversExactFilesAndHostOwnedPagination(t *testing.T) {
	r, repo, s, req := resourceFixture(t)
	text := strings.Repeat("幼苗", 2000) + "END"
	if err := os.WriteFile(filepath.Join(s.root, "中文 数据.csv"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := r.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range view.Definitions {
		if resource.WrappedTool(d.QualifiedName) {
			t.Fatal("free locator adapter leaked")
		}
		if strings.Contains(string(d.InputSchema), `"path"`) {
			t.Fatal("model still supplies paths")
		}
	}
	a := resourceActionByLabel(t, repo, "读取文件")
	if !view.OpenIDs[a.ID] {
		t.Fatal("real action not supplied")
	}
	result, err := invokeResource(t, r, s, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Actions         []resource.Option
		SourceArguments map[string]any
		SourceResult    struct{ Content string }
	}
	if err = json.Unmarshal(result.Structured, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceArguments["path"] != "中文 数据.csv" || len(payload.Actions) != 1 {
		t.Fatalf("bad resolved locator or continuation: %s", result.Structured)
	}
	combined := payload.SourceResult.Content
	for len(payload.Actions) > 0 {
		result, err = invokeResource(t, r, s, payload.Actions[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		payload.Actions = nil
		if err = json.Unmarshal(result.Structured, &payload); err != nil {
			t.Fatal(err)
		}
		combined += payload.SourceResult.Content
	}
	if combined != text {
		t.Fatal("host continuation lost UTF-8 text")
	}
	d, _ := r.OpenTool().Definition(context.Background())
	if err = (tool.JSONSchemaValidator{}).Validate(d.OutputSchema, result.Structured); err != nil {
		t.Fatal(err)
	}
}
func TestResourceReferenceCannotBecomeArbitraryPathOrCrossTaskAccess(t *testing.T) {
	r, repo, s, req := resourceFixture(t)
	os.WriteFile(filepath.Join(s.root, "a.txt"), []byte("allowed"), 0600)
	_, err := r.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	a := resourceActionByLabel(t, repo, "读取文件")
	for _, id := range []string{"/workspace", "../a.txt", "research-design", "res_" + strings.Repeat("a", 32)} {
		if _, err = invokeResource(t, r, s, id); err == nil {
			t.Fatalf("guessed %s accepted", id)
		}
	}
	// The same action ID does not resolve under another Run, even in one project.
	_, err = r.OpenTool().Invoke(context.Background(), tool.Invocation{RunID: "other", SubjectKind: tool.SubjectChatRun, ProjectID: "project", ResearchTaskID: s.task, WorkspaceRoot: s.root, Arguments: resourceArgs(map[string]string{"actionId": a.ID})})
	if err == nil {
		t.Fatal("cross-run reference accepted")
	}
	s.task = "other-task"
	if _, err = invokeResource(t, r, s, a.ID); err == nil {
		t.Fatal("rebound task accepted")
	}
	s.task = "task"
	changed := a
	changed.Seed.Arguments = json.RawMessage(`{"path":"../outside.txt"}`)
	repo.actions[a.RunID+":"+a.ID] = changed
	if _, err = invokeResource(t, r, s, a.ID); err == nil {
		t.Fatal("tampered action accepted")
	}
}
func TestResourceInterfaceOnlyIssuesActualEnabledSkillChoices(t *testing.T) {
	r, repo, s, req := resourceFixture(t)
	f := &compatibleSkillFixture{names: []opensciskill.Info{{Name: "statistical-analysis", Entry: true, Enabled: true, ContentHash: "content", PackageHash: "package"}, {Name: "disabled", Entry: true}, {Name: "unavailable", Entry: true, Enabled: true, Capability: opensciskill.CapabilityUnavailable}}}
	r.skills = f
	if err := r.registry.(*tool.MemoryRegistry).Register(context.Background(), NewSkillLoad(f)); err != nil {
		t.Fatal(err)
	}
	req.Definitions, _ = r.registry.Definitions(context.Background())
	req.SkillDiscovery = true
	view, err := r.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view.Context, "加载 Skill · disabled") || strings.Contains(view.Context, "research-design") || strings.Contains(view.Context, "加载 Skill · unavailable") {
		t.Fatal("nonexistent/unavailable skill exposed")
	}
	a := resourceActionByLabel(t, repo, "加载 Skill")
	result, err := invokeResource(t, r, s, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.loaded != "statistical-analysis" || !strings.Contains(string(result.Structured), `"sourceTool":"builtin.skill.load"`) {
		t.Fatal("resource choice did not load exact skill")
	}
	f.names[0].ContentHash = "changed"
	if _, err = invokeResource(t, r, s, a.ID); err == nil {
		t.Fatal("changed Skill silently substituted")
	}
}
func TestResourceInterfacePreparationFailsClosedOnPersistenceOrScopeChange(t *testing.T) {
	r, repo, _, req := resourceFixture(t)
	repo.fail = true
	if _, err := r.Prepare(context.Background(), req); err == nil {
		t.Fatal("failed persistence ignored")
	}
	repo.fail = false
	if _, err := r.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.WorkflowStepID = "other"
	if _, err := r.Prepare(context.Background(), req); err == nil {
		t.Fatal("old scope reused")
	}
}
func TestResourceMenusPublishOnlyBoundedRealChildren(t *testing.T) {
	children := []resource.Seed{}
	for i := 0; i < 60; i++ {
		children = append(children, resource.Seed{Kind: "menu", Label: fmt.Sprint(i)})
	}
	page := menuPage(children, "menu")
	if len(page) != 25 || len(page[24].Children) != 36 {
		t.Fatal("menu pagination is not bounded")
	}
}

type resourceChangedAdapter struct {
	definition tool.Definition
	invoked    *bool
}

func (t resourceChangedAdapter) Definition(context.Context) (tool.Definition, error) {
	return t.definition, nil
}
func (t resourceChangedAdapter) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	*t.invoked = true
	return tool.Result{Status: tool.ResultSuccess}, nil
}
func TestResourceInterfaceRejectsAdapterContractDriftBeforeIO(t *testing.T) {
	r, repo, s, req := resourceFixture(t)
	if err := os.WriteFile(filepath.Join(s.root, "data.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	action := resourceActionByLabel(t, repo, "读取文件")
	read, _ := r.registry.Definition(context.Background(), ReadTextName)
	list, _ := r.registry.Definition(context.Background(), ListWorkspaceName)
	read.Version = "changed"
	invoked := false
	reg := r.registry.(*tool.MemoryRegistry)
	if err := reg.ReplaceNamespace(context.Background(), "builtin.workspace.", []tool.Tool{resourceChangedAdapter{read, &invoked}, resourceChangedAdapter{list, &invoked}}); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeResource(t, r, s, action.ID); err == nil || invoked {
		t.Fatal("changed adapter executed through old handle")
	}
	req.Definitions, _ = reg.Definitions(context.Background())
	if _, err := r.Prepare(context.Background(), req); err == nil {
		t.Fatal("old catalog survived new adapter contract")
	}
}

func TestIssuedResourceActionsSurviveDiscoveryWindow(t *testing.T) {
	ctx := context.Background()
	engine, repo, resolver, req := resourceFixture(t)
	if _, err := engine.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	session, _, _ := repo.GetSession(ctx, req.RunID)
	actions := []resource.Action{}
	for i := 0; i < 200; i++ {
		actions = append(actions, resource.NewAction(session.Scope, resource.Seed{Kind: "menu", Label: fmt.Sprintf("menu-%03d", i)}, false, engine.now()))
	}
	if err := repo.PutActions(ctx, session.Scope, actions); err != nil {
		t.Fatal(err)
	}
	view, err := engine.Prepare(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		args := resourceArgs(map[string]string{"actionId": a.ID})
		if !view.Allows(resource.OpenTool, args) {
			t.Fatalf("issued action expired: %s", a.ID)
		}
		for _, d := range view.Definitions {
			if d.QualifiedName == resource.OpenTool {
				if err := (tool.JSONSchemaValidator{}).Validate(d.InputSchema, args); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// A choice outside the 128-label discovery view still resolves without IO.
	if _, err = invokeResource(t, engine, resolver, actions[199].ID); err != nil {
		t.Fatal(err)
	}
	if strings.Count(view.Context, `"actionId":`) > 128 {
		t.Fatal("discovery labels are unbounded")
	}
}
