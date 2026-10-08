package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestSourceReferenceIsConfirmedSeparateAndCanBeCleared(t *testing.T) {
	d := revisionDataFixture()
	source := &ResearchSourceVersion{Files: []RevisionSourceFile{{InputFile: InputFile{RelativePath: "research-inputs/code-" + strings.Repeat("b", 64) + ".py", SHA256: strings.Repeat("b", 64)}, AttachmentID: "sent"}}}
	d.RevisionProposals = []ResearchRevisionProposal{{SourceVersion: source}}
	if v, err := CurrentResearchSourceVersion(d); err != nil || v != nil {
		t.Fatal("pending reference adopted", err)
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{SourceVersion: source}})})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{NodeID: "report_drafting"}})})
	v, err := CurrentResearchSourceVersion(d)
	if err != nil || v == nil || v.Files[0].AttachmentID != "sent" {
		t.Fatal("later revision lost reference", err)
	}
	inputs, err := ResearchEffectiveInputs(d)
	if err != nil || string(inputs) != string(d.Run.Inputs) {
		t.Fatal("source altered table inputs", err)
	}
	saved := rawObject(map[string]any{"sourceVersion": v})
	page, err := researchRevisionRecord(saved, ResearchTaskReadCommand{Section: "revision_source_inputs", RevisionID: "history"})
	if err != nil || !strings.Contains(string(page), v.Files[0].SHA256) {
		t.Fatal("source provenance lost", err)
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{SourceVersion: &ResearchSourceVersion{Files: []RevisionSourceFile{}}}})})
	v, err = CurrentResearchSourceVersion(d)
	if err != nil || v == nil || len(v.Files) != 0 {
		t.Fatal("explicit clear did not override prior reference", err)
	}
}

func TestSourceSelectionRespectsFrozenRouteAndUserSentFiles(t *testing.T) {
	d := revisionDataFixture()
	d.Run.Compilation.Nodes[1].PromptVersion = dynamicImplementationPromptVersion
	d.Run.Compilation.Nodes[1].AllowedTools = []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "method_implementation", FromPort: "analysis.code", ToNode: "python_analysis", ToPort: "code"})
	s := &RuntimeService{repository: &discussionFilesFixture{}}
	for _, command := range []ProposeResearchRevisionCommand{
		{NodeID: "report_drafting", SourceInputs: &RevisionSourceSelection{AttachmentIDs: []string{"sent"}}},
		{NodeID: "method_implementation", SourceInputs: &RevisionSourceSelection{}},
		{NodeID: "method_implementation", SourceInputs: &RevisionSourceSelection{AttachmentIDs: []string{"not-sent"}}},
		{NodeID: "method_implementation", SourceInputs: &RevisionSourceSelection{AttachmentIDs: []string{"sent", "another"}}},
	} {
		if _, err := s.prepareRevisionSource(context.Background(), d, command); err == nil {
			t.Fatal("invalid source selection accepted", command)
		}
	}
	clear := ProposeResearchRevisionCommand{NodeID: "method_implementation", SourceInputs: &RevisionSourceSelection{AttachmentIDs: []string{}}}
	if _, err := s.prepareRevisionSource(context.Background(), d, clear); err != nil {
		t.Fatal(err)
	}
	d.Run.Compilation.Nodes[1].AllowedTools = nil
	if _, err := s.prepareRevisionSource(context.Background(), d, clear); err == nil {
		t.Fatal("old route silently gained source capability")
	}
}

func TestConfirmedSourceReadPagesExactUTF8AndRejectsChangedBytes(t *testing.T) {
	s, f := stageMemoryService()
	selected := project.Project{ID: "p", WorkspacePath: t.TempDir()}
	s.projects = fixedProjectLoader{selected}
	f.detail.Run.ResearchTaskID = "task"
	code := strings.Repeat("# 中文源码与缩进保持原样\n", 900) + "def method(x):\n    return x*x\n"
	path := filepath.Join(t.TempDir(), "source.py")
	if err := os.WriteFile(path, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	staged, err := (&Service{}).stageOpenedInputFile(selected, handle, "", "source.py", "python_source", "task", hashBytes([]byte(code)))
	if err != nil {
		t.Fatal(err)
	}
	f.detail.Events = []RuntimeEvent{{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{SourceVersion: &ResearchSourceVersion{Files: []RevisionSourceFile{{InputFile: staged, AttachmentID: "sent"}}}}})}}
	command := ResearchTaskReadCommand{Section: "source_content", AttachmentID: "sent"}
	var recovered strings.Builder
	pages := 0
	for {
		value, err := s.ReadResearchTask(context.Background(), "current-chat", command)
		if err != nil {
			t.Fatal(err)
		}
		if model := tool.BuildModelContextSnapshot("call", "", tool.Result{Status: tool.ResultSuccess, Structured: value}); strings.Contains(model, "tool result truncated") {
			t.Fatal("source page exceeds model-visible envelope")
		}
		var page struct {
			Content string `json:"content"`
			Next    int    `json:"nextOffset"`
			More    bool   `json:"hasMore"`
		}
		if err := json.Unmarshal(value, &page); err != nil {
			t.Fatal(err)
		}
		recovered.WriteString(page.Content)
		pages++
		if !page.More {
			break
		}
		command.Offset = page.Next
	}
	var record struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(recovered.String()), &record); err != nil || record.Content != code || pages < 2 {
		t.Fatal("exact source not paged", err, pages)
	}
	command.Offset = 0
	command.AttachmentID = "unsent"
	if _, err := s.ReadResearchTask(context.Background(), "current-chat", command); err == nil {
		t.Fatal("unadopted source read")
	}
	root, err := project.ResearchTaskWorkspacePath(selected, "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, staged.RelativePath), []byte(strings.Replace(code, "x*x", "x+x", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	command.AttachmentID = "sent"
	if _, err := s.ReadResearchTask(context.Background(), "current-chat", command); err == nil {
		t.Fatal("tampered source read")
	}
	service := &Service{projects: fixedProjectLoader{selected}}
	if _, err := service.StageInputFileForTask(context.Background(), "p", path, "python_source", "task"); err == nil {
		t.Fatal("ordinary table selector broadened to source")
	}
}

func TestSourceStagingRejectsNonPythonAndNonTextWithoutRunning(t *testing.T) {
	selected := project.Project{ID: "p", WorkspacePath: t.TempDir()}
	for _, tc := range []struct{ name, content string }{{"method.r", "x <- 1\n"}, {"method.py", "x=1\x00"}, {"method.py", string([]byte{0xff, 0xfe, 'x'})}} {
		path := filepath.Join(t.TempDir(), "opaque")
		if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = (&Service{}).stageOpenedInputFile(selected, file, "", tc.name, "python_source", "task", hashBytes([]byte(tc.content)))
		file.Close()
		if err == nil {
			t.Fatal("incompatible source staged", tc.name)
		}
	}
}

func TestSourceStageCarriesIdentityNotEntireCodeAndReviewRetainsProvenance(t *testing.T) {
	d := revisionDataFixture()
	ref := RevisionSourceFile{InputFile: InputFile{RelativePath: "research-inputs/code-" + strings.Repeat("b", 64) + ".py", Name: "method.py", SizeBytes: 10000, SHA256: strings.Repeat("b", 64), Staged: true}, AttachmentID: "sent"}
	d.Events = []RuntimeEvent{{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{SourceVersion: &ResearchSourceVersion{Files: []RevisionSourceFile{ref}}}})}}
	node := CompiledNode{ID: "method_implementation", Kind: NodeAgentStage, Arguments: raw(`{}`), PromptVersion: dynamicImplementationPromptVersion, AllowedTools: []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}}
	d.Run.Compilation.Edges = nil
	bound, err := bindNodeInput(d, node)
	if err != nil || !strings.Contains(string(bound), ref.SHA256) || !strings.Contains(string(bound), `"researchSourceVersion"`) || strings.Contains(string(bound), `"code"`) {
		t.Fatal("stage lost identity or embedded source", err, string(bound))
	}
	prompt := buildAIStagePrompt(d, Step{Input: bound}, node)
	if !strings.Contains(prompt, researchSourceInstruction) {
		t.Fatal("source adaptation guidance missing")
	}
	node.AllowedTools = nil
	if strings.Contains(buildAIStagePrompt(d, Step{Input: raw(`{}`)}, node), researchSourceInstruction) {
		t.Fatal("unfrozen source read advertised")
	}
	if audit := independentReviewHostAudit(d); !strings.Contains(audit, ref.SHA256) || !strings.Contains(audit, "sourceVersion") {
		t.Fatal("review lost source provenance")
	}
	node.AllowedTools = []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{SourceVersion: &ResearchSourceVersion{Files: []RevisionSourceFile{}}}})})
	if strings.Contains(buildAIStagePrompt(d, Step{Input: raw(`{}`)}, node), researchSourceInstruction) {
		t.Fatal("cleared reference advertised as adopted source")
	}
}
