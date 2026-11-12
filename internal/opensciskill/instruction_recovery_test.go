package opensciskill

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestStructuredReadFailureDoesNotRecordSkillAndValidReadFreezesSnapshot(t *testing.T) {
	ctx := context.Background()
	service, store, projects := newTestService(t)
	defer store.Close()
	project, err := projects.Create(ctx, "Skill recovery", "")
	if err != nil {
		t.Fatal(err)
	}
	body := "# Overview\n" + strings.Repeat("说明", 50) + "\n## Method\n原方法"
	directory := filepath.Join(project.WorkspacePath, ".openscience", "skills", "recovery")
	writeTestSkill(t, directory, "recovery", "research", body)
	runID, callID := createRunAndToolCall(t, store, project)
	service.Invalidate()
	for _, args := range []struct {
		section string
		offset  int
		limit   int
		kind    string
	}{
		{"section-0", 0, 30, UsageSection},
		{"section-1", 1000, 30, UsageOffset},
		{"", 1000, 30, UsageOffset},
		{"", 0, MaxReadRunes + 1, UsageLimit},
	} {
		_, _, created, err := service.LoadStructuredForRun(ctx, runID, project.ID, callID, "recovery", args.section, args.offset, args.limit)
		var usage *UsageError
		if !errors.As(err, &usage) || usage.Kind != args.kind || created {
			t.Fatalf("invalid read: created=%v error=%v", created, err)
		}
		if _, e := service.GetRunSkill(ctx, runID, "recovery"); !errors.Is(e, ErrRunSkillNotFound) {
			t.Fatalf("failed read wrote snapshot: %v", e)
		}
	}
	_, index, created, err := service.LoadStructuredForRun(ctx, runID, project.ID, callID, "recovery", "", 0, 30)
	if err != nil || !created || index.Mode != "index" || len(index.Sections) != 2 {
		t.Fatalf("index: %+v %v %v", index, created, err)
	}
	// Changing source/policy after first valid read must not silently replace
	// the immutable Run's instructions or reject its continuation.
	writeTestSkill(t, directory, "recovery", "research", "# Changed\nnew source")
	service.Invalidate()
	if _, err = service.SetEnabled(ctx, project.ID, "recovery", false); err != nil {
		t.Fatal(err)
	}
	_, section, created, err := service.LoadStructuredForRun(ctx, runID, project.ID, callID, "recovery", "section-2", 0, 30)
	if err != nil || created || section.Content != "## Method\n原方法" {
		t.Fatalf("snapshot continuation drifted: %+v %v %v", section, created, err)
	}
	_, _, _, err = service.LoadStructuredForRun(ctx, runID, "wrong-project", callID, "recovery", "", 0, 30)
	if err == nil {
		t.Fatal("cross-project read accepted")
	}
}

func TestStructuredShortReadHonorsOffsetAndIntroductionIdentity(t *testing.T) {
	body := "# Short\n说明"
	chunk, err := structuredInstructionChunk("fixture", OriginUser, body, "", 3, 100)
	if err != nil || chunk.Offset != 3 || chunk.Content != string([]rune(body)[3:]) {
		t.Fatalf("offset ignored: %+v %v", chunk, err)
	}
	if _, err := structuredInstructionChunk("fixture", OriginUser, body, "section-0", 0, 100); err == nil {
		t.Fatal("section-0 invented for heading-first body")
	}
	intro := "前言\n" + body
	chunk, err = structuredInstructionChunk("fixture", OriginUser, intro, "section-0", 0, 100)
	if err != nil || chunk.Content != "前言\n" || chunk.Section != "section-0" {
		t.Fatalf("real introduction lost: %+v %v", chunk, err)
	}
}

func TestCallableCandidatesAreTheSameAsBrowseAndRouting(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newTestService(t)
	defer store.Close()
	if err := service.SetToolRegistry(tool.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	snapshot := mustCatalog(t, service, "")
	for _, category := range snapshot.Categories {
		actual, err := service.Browse(ctx, "", category.Name)
		if err != nil {
			t.Fatal(err)
		}
		expected := []string{}
		for _, info := range snapshot.Skills {
			if info.Callable() && normalizedCategory(info.Category) == category.Name {
				expected = append(expected, info.Name)
			}
		}
		if strings.Join(expected, ",") != strings.Join(skillNames(actual), ",") {
			t.Fatalf("browse diverged: %s", category.Name)
		}
	}
	prompt, err := service.RoutingPrompt(ctx, "", "use research-workflows")
	if err != nil || strings.Contains(prompt, "- research-workflows:") || !strings.Contains(prompt, "On first load omit section") {
		t.Fatalf("unsafe or incomplete routing: %s %v", prompt, err)
	}
}
