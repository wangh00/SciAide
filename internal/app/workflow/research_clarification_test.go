package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/document"
)

func TestClarificationSelectionContracts(t *testing.T) {
	for _, tc := range []struct {
		name, mode, text string
		ok               bool
	}{
		{"legacy single", "", "研究目标？", true},
		{"explicit single", "single", "请选择一个目标", true},
		{"multiple", "multiple", "选择关注的指标", true},
		{"legacy screenshot", "", "请确认光周期梯度与生长指标的范围（多选，确认后写入 question_refinement 与 data_preflight）。", false},
		{"conflicting multiple", "multiple", "选择指标（单选）", false},
		{"unknown mode", "checkbox", "指标", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := clarificationFixture()
			q.Questions[0].SelectionMode = tc.mode
			q.Questions[0].Text = tc.text
			err := validateResearchClarification(q)
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v, want valid=%v", err, tc.ok)
			}
		})
	}
	q := clarificationFixture()
	q.Questions[0].SelectionMode = "multiple"
	q.Questions[0].Options = []ResearchClarificationOption{{ID: "g1", Label: "梯度：8/12/16"}, {ID: "g2", Label: "梯度：12/16/20"}, {ID: "m1", Label: "指标：株高"}, {ID: "m2", Label: "指标：干重"}}
	if err := validateResearchClarification(q); err == nil || !strings.Contains(err.Error(), "独立维度") {
		t.Fatalf("compound question accepted: %v", err)
	}
}

func TestMultipleClarificationAnswersAreCanonicalAndRejectUnknownDuplicates(t *testing.T) {
	q := clarificationFixture()
	q.Questions[0].SelectionMode = "multiple"
	a, err := normalizeResearchClarificationAnswers(q, map[string][]string{"population": {"adults", "students"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := normalizeResearchClarificationAnswers(q, map[string][]string{"population": {"students", "adults"}})
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("noncanonical answers: %v %v %v", a, b, err)
	}
	for _, ids := range [][]string{{"students", "students"}, {"students", "unknown"}, {}} {
		if _, err := normalizeResearchClarificationAnswers(q, map[string][]string{"population": ids}); err == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	idea := clarifiedResearchIdea("目标", q, a)
	if !strings.Contains(idea, "在校大学生") || !strings.Contains(idea, "一般成年人") {
		t.Fatalf("answers lost: %s", idea)
	}
}

func TestNewClarificationSchemaRequiresExplicitModeButLegacySchemaUnchanged(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(semanticResearchStarterSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	question := schema["properties"].(map[string]any)["clarification"].(map[string]any)["properties"].(map[string]any)["questions"].(map[string]any)["items"]
	encoded, _ := json.Marshal(question)
	for _, tc := range []struct {
		mode  string
		valid bool
	}{{``, false}, {`,"selectionMode":"single"`, true}, {`,"selectionMode":"multiple"`, true}, {`,"selectionMode":"all"`, false}} {
		value := raw(`{"id":"q","kind":"research_direction","impact":"决定主要研究结局","text":"指标","required":true,"options":[{"id":"a","label":"株高"},{"id":"b","label":"干重"}]` + tc.mode + `}`)
		err := (tool.JSONSchemaValidator{}).Validate(encoded, value)
		if (err == nil) != tc.valid {
			t.Fatalf("mode=%s err=%v", tc.mode, err)
		}
	}
	if strings.Contains(string(legacyDynamicResearchStarterSchema()), "selectionMode") {
		t.Fatal("legacy schema rewritten")
	}
}

type clarificationAttachments struct {
	values    []attachment.Attachment
	taskCalls int
}

func (l *clarificationAttachments) List(context.Context, string) ([]attachment.Attachment, error) {
	return []attachment.Attachment{}, nil
}
func (l *clarificationAttachments) ListForTask(context.Context, string, string) ([]attachment.Attachment, error) {
	l.taskCalls++
	return l.values, nil
}

type clarificationKnowledge struct {
	values    []knowledge.Document
	taskCalls int
}

func (l *clarificationKnowledge) ListDocuments(context.Context, string) ([]knowledge.Document, error) {
	return nil, nil
}
func (l *clarificationKnowledge) ListDocumentsForTask(context.Context, string, string) ([]knowledge.Document, error) {
	l.taskCalls++
	return l.values, nil
}

func TestClarificationSnapshotIncludesOnlyCurrentTaskAndSharedReadyTables(t *testing.T) {
	a := &clarificationAttachments{values: []attachment.Attachment{
		{ID: "current", ScopeKind: attachment.ScopeTask, ResearchTaskID: "task", OriginalName: "study.csv", Format: document.FormatCSV, Status: attachment.StatusReady, SHA256: "one"},
		{ID: "other", ScopeKind: attachment.ScopeTask, ResearchTaskID: "other", OriginalName: "secret.csv", Format: document.FormatCSV, Status: attachment.StatusReady},
		{ID: "shared", ScopeKind: attachment.ScopeProjectShared, OriginalName: "shared.tsv", Format: document.FormatText, Status: attachment.StatusReady},
		{ID: "failed", ScopeKind: attachment.ScopeTask, ResearchTaskID: "task", OriginalName: "broken.csv", Format: document.FormatCSV, Status: attachment.StatusFailed},
		{ID: "legacy", OriginalName: "legacy.csv", Format: document.FormatCSV, Status: attachment.StatusReady},
	}}
	k := &clarificationKnowledge{values: []knowledge.Document{{ID: "current", ScopeKind: attachment.ScopeTask, ResearchTaskID: "task", Status: knowledge.DocumentReady}, {ID: "other", ScopeKind: attachment.ScopeTask, ResearchTaskID: "other", Status: knowledge.DocumentReady}}}
	s := &StarterService{attachments: a, knowledge: k}
	x, err := s.snapshotForTask(context.Background(), "p", "task")
	if err != nil {
		t.Fatal(err)
	}
	if a.taskCalls != 1 || k.taskCalls != 1 || x.AttachmentCount != 3 || x.ReadyAttachmentCount != 2 || x.KnowledgeDocumentCount != 1 || !reflect.DeepEqual(x.TabularFiles, []string{"shared.tsv", "study.csv"}) {
		t.Fatalf("wrong scope snapshot: %#v", x)
	}
	a.values[0].SHA256 = "two"
	y, err := s.snapshotForTask(context.Background(), "p", "task")
	if err != nil {
		t.Fatal(err)
	}
	if x.ResourceFingerprint == y.ResourceFingerprint {
		t.Fatal("same-name changed content invisible to replanning key")
	}
	a.values[0], a.values[4] = a.values[4], a.values[0]
	z, err := s.snapshotForTask(context.Background(), "p", "task")
	if err != nil {
		t.Fatal(err)
	}
	if y.ResourceFingerprint != z.ResourceFingerprint {
		t.Fatal("repository order changed fingerprint")
	}
}
