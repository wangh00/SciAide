package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestStageMaterialReadAddressesOneDecodedValue(t *testing.T) {
	s, f := stageMemoryService()
	f.detail.Run.Compilation.Nodes[0].AllowedTools[0].Version = "9"
	f.detail.Steps[0].Input = raw(`{"unrelated":"DO_NOT_FETCH","context":{"markdown":"中🙂文\n写作反馈"}}`)
	var command ResearchTaskReadCommand
	if err := json.Unmarshal([]byte(`{"section":"stage_input","pointer":"/context/markdown","offset":1}`), &command); err != nil {
		t.Fatal(err)
	}
	data, err := s.ReadResearchTask(context.Background(), "current-chat", command)
	if err != nil {
		t.Fatal(err)
	}
	var page struct{ Content, Pointer, Encoding string }
	if json.Unmarshal(data, &page) != nil || page.Content != "🙂文\n写作反馈" || page.Pointer != "/context/markdown" || page.Encoding != "text" {
		t.Fatal("semantic read returned whole JSON or wrong Unicode range", string(data))
	}
}

func BenchmarkStageMaterialMillionCharacterPage(b *testing.B) {
	input, _ := json.Marshal(map[string]string{"data": strings.Repeat("中", 1_000_000)})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ReadResearchStageMaterial(input, "/data", (i%167)*6000, "draft"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestStageMaterialPointerIdentityAndStrictTraversal(t *testing.T) {
	input := json.RawMessage(`{"a/b":{"~key":["中🙂\n",9007199254740993]},"01":"object key","empty":"","nil":null}`)
	for _, tc := range []struct{ pointer, content, encoding string }{{"/a~1b/~0key/0", "中🙂\n", "text"}, {"/a~1b/~0key/1", "9007199254740993", "json"}, {"/01", "object key", "text"}, {"/empty", "", "text"}, {"/nil", "null", "json"}, {"", string(input), "json"}} {
		m, err := ResolveResearchStageMaterial(input, tc.pointer)
		if err != nil || m.Content != tc.content || m.Encoding != tc.encoding || m.InputSHA256 != hashJSON(input) || m.ValueSHA256 != hashJSON([]byte(tc.content)) {
			t.Fatal("wrong addressed material or hash", tc.pointer, m, err)
		}
	}
	for _, pointer := range []string{"a/b", "/a~2b", "/a~", "/a~1b/~0key/01", "/a~1b/~0key/-1", "/a~1b/~0key/-", "/a~1b/~0key/9", "/01/nested"} {
		if _, err := ResolveResearchStageMaterial(input, pointer); err == nil {
			t.Fatal("invalid pointer accepted", pointer)
		}
	}
	if _, err := ResolveResearchStageMaterial(raw(`{"x":1,"x":2}`), "/x"); err == nil {
		t.Fatal("duplicate key resolved to an ambiguous value")
	}
	if _, err := ResolveResearchStageMaterial(raw(`{"x":{"y":1,"y":2}}`), "/x/y"); err == nil {
		t.Fatal("nested duplicate key accepted")
	}
}

func TestStageMaterialReaderPreservesScopeAndLegacyInterface(t *testing.T) {
	for _, version := range []string{"7", "8", "9"} {
		s, f := stageMemoryService()
		f.detail.Run.Compilation.Nodes[0].AllowedTools[0].Version = version
		pointer := "/current"
		_, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input", Pointer: &pointer})
		if (err == nil) != (version == "9") {
			t.Fatal("legacy route gained pointer scope", version, err)
		}
		if _, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input"}); err != nil {
			t.Fatal("legacy offset interface broke", version, err)
		}
	}
	s, f := stageMemoryService()
	f.detail.Run.Compilation.Nodes[0].AllowedTools[0].Version = "9"
	pointer := "/current"
	for _, tc := range []ResearchTaskReadCommand{{Section: "stage_output", Pointer: &pointer}, {Section: "planning_discussion", Pointer: &pointer}, {Section: "stage_input", Pointer: &pointer, NodeID: "another"}, {Section: "stage_input", Pointer: &pointer, RevisionID: "confirmed-revision"}, {Section: "stage_input", Pointer: &pointer, AttachmentID: "sent"}} {
		if _, err := s.ReadResearchTask(context.Background(), "current-chat", tc); err == nil {
			t.Fatal("pointer expanded stage scope", tc)
		}
	}
}

func TestStageMaterialReadPagingHandlesEscapesEmptyAndEOF(t *testing.T) {
	text := strings.Repeat("中🙂\n\"\\", 3000)
	input := mustJSON(map[string]string{"paper": text, "empty": ""})
	offset := 0
	var combined strings.Builder
	for {
		data, err := ReadResearchStageMaterial(input, "/paper", offset, "node")
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Content                     string
			NextOffset, TotalCharacters int
			HasMore                     bool
		}
		if json.Unmarshal(data, &page) != nil || page.NextOffset <= offset {
			t.Fatal("no forward page", string(data))
		}
		preview := tool.BuildModelContextSnapshot("page", "", tool.Result{Status: tool.ResultSuccess, Structured: data})
		if strings.Contains(preview, "tool result truncated") {
			t.Fatal("new metadata made a page exceed context envelope")
		}
		combined.WriteString(page.Content)
		offset = page.NextOffset
		if !page.HasMore {
			break
		}
	}
	if combined.String() != text {
		t.Fatal("decoded text lost Unicode or escape boundaries")
	}
	if _, err := ReadResearchStageMaterial(input, "/paper", offset, "node"); err != nil {
		t.Fatal("legal EOF read rejected", err)
	}
	if _, err := ReadResearchStageMaterial(input, "/paper", offset+1, "node"); err == nil {
		t.Fatal("out of bounds read accepted")
	}
	empty, err := ReadResearchStageMaterial(input, "/empty", 0, "node")
	if err != nil || !strings.Contains(string(empty), `"totalCharacters":0`) {
		t.Fatal("empty value could not be represented", err)
	}
}

func TestStageMaterialReadRejectsUnrepresentableEncodedPointer(t *testing.T) {
	key := strings.Repeat("\x00", 1600)
	input := mustJSON(map[string]string{key: "x"})
	if _, err := ResolveResearchStageMaterial(input, "/"+key); err != nil {
		t.Fatal("otherwise valid Unicode pointer rejected", err)
	}
	if data, err := ReadResearchStageMaterial(input, "/"+key, 0, "node"); err == nil || len(data) != 0 {
		t.Fatal("oversized metadata returned an unusable successful page")
	}
}
