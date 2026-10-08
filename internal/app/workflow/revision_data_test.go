package workflow

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
)

func revisionDataFixture() RunDetail {
	d := RunDetail{Run: Run{ResearchTaskID: "task", Inputs: raw(`{"input_paths":["research-inputs/old-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.csv"],"research_goal":"unchanged"}`), Compilation: Compilation{
		Inputs: []Port{{Name: "input_paths", Type: TypeArray, FileKind: "tabular", Required: true, MinItems: 1, MaxItems: 16}, {Name: "research_goal", Type: TypeString, Required: true}},
		Nodes:  []CompiledNode{{ID: "data_preflight", Kind: NodePython}, {ID: "method_implementation", Kind: NodeAgentStage}, {ID: "python_analysis", Kind: NodePython}, {ID: "report_drafting", Kind: NodeAgentStage}},
		Edges:  []Edge{{FromNode: "$input", FromPort: "input_paths", ToNode: "data_preflight", ToPort: "inputPaths"}, {FromNode: "$input", FromPort: "input_paths", ToNode: "python_analysis", ToPort: "inputPaths"}},
	}}, Steps: []Step{{NodeID: "data_preflight", Ordinal: 1}, {NodeID: "method_implementation", Ordinal: 2}, {NodeID: "python_analysis", Ordinal: 3}, {NodeID: "report_drafting", Ordinal: 4}}}
	for i := range d.Run.Compilation.Nodes {
		d.Run.Compilation.Nodes[i].Arguments = raw(`{}`)
	}
	return d
}

func TestNewDataRouteFreezesMethodBeforeReplaceableDataSuffix(t *testing.T) {
	template, _, _, err := dynamicResearchRouteTemplate(starterDataRoute(), ResearchStarterContext{ResearchIdea: "analysis", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := NewCompiler(referenceTemplateRegistry(t)).Compile(context.Background(), template.Definition)
	if err != nil {
		t.Fatal(err)
	}
	ordinals := map[string]int{}
	for i, id := range compiled.Order {
		ordinals[id] = i
	}
	if ordinals["method_selection"] >= ordinals["data_preflight"] {
		t.Fatal("method is replayed on a data-only update", compiled.Order)
	}
}

func TestConfirmedDataVersionsPersistAcrossUnrelatedRevisions(t *testing.T) {
	d := revisionDataFixture()
	original := string(d.Run.Inputs)
	sha := strings.Repeat("b", 64)
	path := "research-inputs/new-" + sha + ".csv"
	version := &ResearchDataVersion{PreviousPaths: exactWorkflowInputPaths(d.Run.Inputs), Files: []RevisionDataFile{{InputFile: InputFile{RelativePath: path, SHA256: sha, Name: "new.csv"}, AttachmentID: "sent"}}}
	p := ResearchRevisionProposal{ID: "update", NodeID: "data_preflight", DataVersion: version}
	d.RevisionProposals = []ResearchRevisionProposal{p}
	inputs, err := ResearchEffectiveInputs(d)
	if err != nil || string(inputs) != original {
		t.Fatal("pending proposal adopted", err)
	}
	d.Events = []RuntimeEvent{{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": p})}, {Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{ID: "wording", NodeID: "report_drafting"}})}}
	// Round-trip durable event JSON, as after restart.
	var restored RunDetail
	if err := json.Unmarshal(mustJSON(d), &restored); err != nil {
		t.Fatal(err)
	}
	inputs, err = ResearchEffectiveInputs(restored)
	if err != nil || !strings.Contains(string(inputs), path) || strings.Contains(string(inputs), "old-") || !strings.Contains(string(inputs), "unchanged") {
		t.Fatal(string(inputs), err)
	}
	if string(restored.Run.Inputs) != original {
		t.Fatal("original snapshot changed")
	}
	for _, id := range []string{"data_preflight", "python_analysis"} {
		input, err := bindNodeInput(restored, compilationNodeMap(restored.Run.Compilation)[id])
		if err != nil || !strings.Contains(string(input), path) || strings.Contains(string(input), "old-") {
			t.Fatal(id, string(input), err)
		}
	}
	input, err := bindNodeInput(restored, compilationNodeMap(restored.Run.Compilation)["report_drafting"])
	if err != nil || !strings.Contains(string(input), "researchDataVersion") {
		t.Fatal(string(input), err)
	}
	baseline := researchComparisonBaseline(restored)
	if !strings.Contains(string(baseline), path) {
		t.Fatal("comparison lost current input identity")
	}
	var next RunDetail
	if err := json.Unmarshal(mustJSON(restored), &next); err != nil {
		t.Fatal(err)
	}
	newerSHA := strings.Repeat("c", 64)
	newer := &ResearchDataVersion{Files: []RevisionDataFile{{InputFile: InputFile{RelativePath: "research-inputs/newer-" + newerSHA + ".csv", SHA256: newerSHA}}}}
	next.Events = append(next.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{DataVersion: newer}, "comparisonBaseline": baseline})})
	if !strings.Contains(trustedWorkspaceFilesInstruction(next), path) {
		t.Fatal("prior data not readable for comparison")
	}
	version.Files[0].SHA256 = strings.Repeat("c", 64)
	restored.Events = append(restored.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": p})})
	if _, err := ResearchEffectiveInputs(restored); err == nil {
		t.Fatal("mismatched file identity accepted")
	}
}

func TestDataSelectionIsExplicitAndCannotUseOtherPaths(t *testing.T) {
	d := revisionDataFixture()
	s := &RuntimeService{repository: &discussionFilesFixture{}}
	for _, selection := range []*RevisionDataSelection{
		{AttachmentIDs: []string{"sent"}}, {RetainedPaths: []string{}, AttachmentIDs: []string{}},
		{RetainedPaths: []string{"../other-task/data.csv"}, AttachmentIDs: []string{}},
		{RetainedPaths: []string{}, AttachmentIDs: []string{"not-sent"}},
		{RetainedPaths: []string{}, AttachmentIDs: []string{"sent", "sent"}},
	} {
		if _, err := s.prepareRevisionData(context.Background(), d, ProposeResearchRevisionCommand{NodeID: "data_preflight", DataInputs: selection}); err == nil {
			t.Fatal("invalid selection accepted", selection)
		}
	}
	if _, err := s.prepareRevisionData(context.Background(), d, ProposeResearchRevisionCommand{NodeID: "report_drafting", DataInputs: &RevisionDataSelection{RetainedPaths: []string{}, AttachmentIDs: []string{"sent"}}}); err == nil {
		t.Fatal("data update skipped calculation")
	}
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "other", FromPort: "paths", ToNode: "python_analysis", ToPort: "inputPaths"})
	if revisionDataChain(d) {
		t.Fatal("ambiguous input dependency accepted")
	}
}

func TestTrustedDataStagingUsesSharedFormatAndIntegrityChecks(t *testing.T) {
	workspace := t.TempDir()
	selected := project.Project{ID: "p", WorkspacePath: workspace}
	s := &Service{}
	for _, tc := range []struct {
		name, bytes, sha string
		fail             bool
	}{
		{"data.csv", "x\n1\n", hashBytes([]byte("x\n1\n")), false},
		{"data.tsv", "x\ty\n1\t2\n", hashBytes([]byte("x\ty\n1\t2\n")), false},
		{"bad.csv", "x\n1\x00\n", hashBytes([]byte("x\n1\x00\n")), true},
		{"fake.xlsx", "not a ZIP", hashBytes([]byte("not a ZIP")), true},
		{"changed.csv", "x\n9\n", hashBytes([]byte("x\n1\n")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "opaque-object")
			if err := os.WriteFile(path, []byte(tc.bytes), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			file, err := s.stageOpenedInputFile(selected, f, "", tc.name, "tabular", "task", tc.sha)
			if (err != nil) != tc.fail {
				t.Fatal(file, err)
			}
			if !tc.fail {
				root, err := project.ResearchTaskWorkspacePath(selected, "task")
				if err != nil {
					t.Fatal(err)
				}
				bytes, err := os.ReadFile(filepath.Join(root, file.RelativePath))
				if err != nil || string(bytes) != tc.bytes || file.SHA256 != tc.sha || file.Name != tc.name {
					t.Fatal(file, err)
				}
			}
		})
	}
}

func TestTrustedDataStagingAcceptsXLSXWithoutPrivatePathBypass(t *testing.T) {
	workspace := t.TempDir()
	selected := project.Project{ID: "p", WorkspacePath: workspace}
	private := filepath.Join(workspace, ".sciaide", "attachments", "objects", "table.xlsx")
	if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(private)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	entry, err := w.Create("xl/workbook.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("<workbook/>")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(private)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{projects: fixedProjectLoader{selected}}
	if _, err := service.StageInputFileForTask(context.Background(), "p", private, "tabular", "task"); err == nil {
		t.Fatal("ordinary stager can read private paths")
	}
	file, err := os.Open(private)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	staged, err := service.stageOpenedInputFile(selected, file, "", "table.xlsx", "tabular", "task", hashBytes(bytes))
	if err != nil || staged.SHA256 != hashBytes(bytes) || !strings.HasSuffix(staged.RelativePath, ".xlsx") {
		t.Fatal(staged, err)
	}
}
