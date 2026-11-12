package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func readJSONResourcePages(t *testing.T, contents []byte, oldIssuedLimit int, filenames ...string) (int, []byte) {
	t.Helper()
	r, repo, scope, req := resourceFixture(t)
	filename := "arbitrary-scientific-results.JSON"
	if len(filenames) > 0 {
		filename = filenames[0]
	}
	if err := os.WriteFile(filepath.Join(scope.root, filename), contents, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := r.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	action := resourceActionByLabel(t, repo, "读取文件")
	if !view.OpenIDs[action.ID] {
		t.Fatal("initial host resource action not offered")
	}
	var args struct {
		MaxBytes int `json:"maxBytes"`
	}
	_ = json.Unmarshal(action.Seed.Arguments, &args)
	if strings.EqualFold(filepath.Ext(filename), ".json") && len(contents) > 6000 && len(contents) <= maxReadBytes && args.MaxBytes != workspaceJSONResourceReadBytes {
		t.Fatal("bounded machine-readable JSON did not get a larger page", args.MaxBytes)
	}
	if oldIssuedLimit > 0 {
		// An already issued legacy recipe remains sealed and callable. The
		// scope/version isn't bumped and IDs are not reconstructed by the model.
		session := repo.sessions[req.RunID]
		seed := action.Seed
		seed.Arguments = resourceArgs(map[string]any{"path": filename, "maxBytes": oldIssuedLimit})
		action = resource.NewAction(session.Scope, seed, true, r.now())
		if err = repo.PutActions(context.Background(), session.Scope, []resource.Action{action}); err != nil {
			t.Fatal(err)
		}
	}
	var audit, visible strings.Builder
	id := action.ID
	pages, offset := 0, 0
	for {
		result, err := invokeResource(t, r, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Label           string
			SourceArguments struct{ Offset, MaxBytes int }
			SourceResult    struct {
				Content                  string
				BytesRead, OriginalBytes int
				Truncated                bool
			}
			Actions []resource.Option
		}
		if err = json.Unmarshal(result.Structured, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.SourceArguments.Offset != offset || payload.SourceResult.BytesRead != len([]byte(payload.SourceResult.Content)) || payload.SourceResult.OriginalBytes != len(contents) || payload.SourceArguments.MaxBytes > workspaceJSONResourceReadBytes {
			t.Fatal("read framing/precision provenance changed", string(result.Structured))
		}
		snapshot := tool.BuildModelContextSnapshot(strings.Repeat("c", 36), "", result)
		var model struct {
			Text       string
			Structured struct{ Actions []resource.Option }
		}
		if !json.Valid([]byte(snapshot)) || strings.Contains(snapshot, "...[tool result truncated for model context]...") || json.Unmarshal([]byte(snapshot), &model) != nil {
			t.Fatal("a success page exceeded the unchanged model envelope", snapshot)
		}
		prefix := payload.Label + "\n\n"
		if !strings.HasPrefix(model.Text, prefix) {
			t.Fatal("resource model framing lost")
		}
		if string(resourceArgs(model.Structured.Actions)) != string(resourceArgs(payload.Actions)) {
			t.Fatal("model did not see the same sealed continuation actions")
		}
		audit.WriteString(payload.SourceResult.Content)
		visible.WriteString(strings.TrimPrefix(model.Text, prefix))
		pages++
		offset += payload.SourceResult.BytesRead
		if len(payload.Actions) == 0 {
			if offset != len(contents) || payload.SourceResult.Truncated {
				t.Fatal("missing continuation silently lost scientific content")
			}
			break
		}
		if len(payload.Actions) != 1 || payload.SourceResult.BytesRead <= 0 || len([]rune(payload.Actions[0].Label)) > 240 || strings.Count(payload.Actions[0].Label, "继续读取 · ") != 1 {
			t.Fatal("unbounded label or stalled paging", payload.Actions)
		}
		child, ok, err := repo.GetAction(context.Background(), req.RunID, payload.Actions[0].ID)
		if err != nil || !ok {
			t.Fatal("continuation not durably issued", err)
		}
		var childArgs struct{ Offset, MaxBytes int }
		_ = json.Unmarshal(child.Seed.Arguments, &childArgs)
		if childArgs.Offset != offset || childArgs.MaxBytes != payload.SourceArguments.MaxBytes {
			t.Fatal("continuation did not follow the exact fitted byte range")
		}
		id = payload.Actions[0].ID
	}
	if audit.String() != string(contents) || visible.String() != string(contents) {
		t.Fatal("JSON bytes or visible numeric spellings changed")
	}
	return pages, []byte(visible.String())
}

func TestWorkspaceJSONResourcePagesReduceRoundTripsWithoutChangingNumbers(t *testing.T) {
	contents := []byte(`{"precise":0.6852791878172588,"large":9007199254740993,"exponent":1.234567890123456789e-42,"escaped":"\u003cscript\u003e","body":"` + strings.Repeat("bounded-result,", 2200) + `"}`)
	pages, visible := readJSONResourcePages(t, contents, 0)
	if pages != (len(contents)+workspaceJSONResourceReadBytes-1)/workspaceJSONResourceReadBytes || pages >= (len(contents)+5999)/6000 || !json.Valid(visible) {
		t.Fatal("no narrow JSON pagination saving", len(contents), pages)
	}
}

func TestEscapedJSONPagesRemainCompleteAtTheExistingModelLimit(t *testing.T) {
	for _, text := range []string{strings.Repeat(`\"`, 12000), strings.Repeat("\t\n中文", 4000)} {
		contents, err := json.Marshal(map[string]string{"untrusted": text})
		if err != nil {
			t.Fatal(err)
		}
		readJSONResourcePages(t, contents, 0)
		// Old 6000-byte signed handles must also remain usable even when their
		// escaping needed more space than the old model envelope allowed.
		readJSONResourcePages(t, contents, 6000)
	}
}

func TestEscapedNonJSONResourcePagesStayCompleteAndReplayable(t *testing.T) {
	for _, filename := range []string{"notes.md", "analysis.py", "transcript.txt"} {
		t.Run(filename, func(t *testing.T) {
			contents := []byte(strings.Repeat("\\<\"\n🧪", 1800))
			pages, visible := readJSONResourcePages(t, contents, 6000, filename)
			if pages < 2 || string(visible) != string(contents) {
				t.Fatal("escaped text lost its actual continuation")
			}
		})
	}
}

func TestJSONResourceSizingDoesNotExpandOtherFilesOrLargeResources(t *testing.T) {
	scope := resource.Scope{ToolContracts: map[string]string{ReadTextName: "contract"}}
	entries := []map[string]any{}
	for _, name := range []string{"source.py", "paper.md", "measurements.csv", "table.tsv", "manual.txt", "huge.json", "unknown-size.json", "small.json"} {
		size := int64(31418)
		switch name {
		case "huge.json":
			size = maxReadBytes + 1
		case "unknown-size.json":
			size = 0
		case "small.json":
			size = 42
		}
		entries = append(entries, map[string]any{"name": name, "path": name, "kind": "file", "size": size})
	}
	seeds, err := workspaceResourceSeeds(scope, tool.Result{Structured: resourceArgs(map[string]any{"entries": entries})})
	if err != nil || len(seeds) != len(entries) {
		t.Fatal(err)
	}
	for _, seed := range seeds {
		var args struct {
			MaxBytes int `json:"maxBytes"`
		}
		_ = json.Unmarshal(seed.Arguments, &args)
		if args.MaxBytes != 6000 {
			t.Fatal("non-target resource pagination changed", seed.Label, args.MaxBytes)
		}
	}
}

func TestCapturedMachineResultJSONPagesRemainVerbatim(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REPORT_QUALITY_REPLAY")
	if dir == "" {
		t.Skip("no task-scoped offline capture supplied")
	}
	contents, err := os.ReadFile(filepath.Join(dir, "artifact-original.json"))
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := readJSONResourcePages(t, contents, 0)
	legacyPages := (len(contents) + 5999) / 6000
	if pages >= legacyPages {
		t.Fatal("captured JSON result still requires as many model reads", pages, legacyPages)
	}
	metrics := resourceArgs(map[string]any{"bytes": len(contents), "legacyReadActions": legacyPages, "newReadActions": pages, "modelEnvelopeRunesUnchanged": tool.MaxModelContextRunes, "auditAndVisibleBytesIdentical": true})
	if err = os.WriteFile(filepath.Join(dir, "resource-replay-metrics.json"), metrics, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured JSON %d bytes: %d -> %d read actions; full bytes identical", len(contents), legacyPages, pages)
}
