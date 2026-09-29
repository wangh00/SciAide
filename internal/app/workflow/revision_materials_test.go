package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
)

func revisionEvidenceFixture(t *testing.T) RunDetail {
	d := reviewedDeliveryFixture(t)
	for i := range d.Steps {
		d.Steps[i].Ordinal += 4
	}
	ids := []string{"evidence_import", "evidence_sync", "evidence_search", "evidence_screening"}
	names := []string{"builtin.research.workflow.import", "builtin.research.workflow.sync", "builtin.knowledge.search"}
	for i, id := range ids {
		n := CompiledNode{ID: id, Name: id, Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion}
		if i < 3 {
			n.Kind = NodeTool
			n.Tool = &ToolSnapshot{QualifiedName: names[i]}
		}
		d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, n)
		d.Steps = append(d.Steps, Step{ID: id, NodeID: id, Ordinal: i, Status: StepCompleted, Input: raw(`{"selectedCandidateIds":[],"selectedAttachmentIds":[]}`)})
	}
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, []Edge{
		{FromNode: ids[0], FromPort: "structured.attachmentIds", ToNode: ids[1], ToPort: "attachmentIds"},
		{FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[2], ToPort: "documentIds"},
		{FromNode: ids[2], FromPort: "citations", ToNode: ids[3], ToPort: "candidates"},
		{FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[3], ToPort: "documentIds"},
		{FromNode: ids[0], FromPort: "structured.materials", ToNode: ids[3], ToPort: "importedMaterials"},
		{FromNode: ids[3], FromPort: "analysis", ToNode: "design", ToPort: "evidence"},
	}...)
	return d
}

type revisionFilesFixture struct{ promoted int }

func (*revisionFilesFixture) ReferenceMaterials(context.Context, string, string, []string) ([]attachment.Attachment, error) {
	return []attachment.Attachment{}, nil
}
func (*revisionFilesFixture) RevisionMaterials(_ context.Context, _, _ string, ids []string) ([]attachment.Attachment, error) {
	values := []attachment.Attachment{}
	for _, id := range ids {
		values = append(values, attachment.Attachment{ID: id, SHA256: "hash", OriginalName: "paper.md"})
	}
	return values, nil
}
func (f *revisionFilesFixture) PromoteRevisionMaterial(context.Context, string, string, string, string, string) (attachment.Attachment, error) {
	f.promoted++
	return attachment.Attachment{ID: "task-file", SHA256: "hash"}, nil
}

type discussionFilesFixture struct{ RuntimeRepository }

func (*discussionFilesFixture) DiscussionAttachmentIDs(context.Context, string) ([]string, error) {
	return []string{"sent"}, nil
}

func TestRevisionEvidenceTargetsAndMaterialConsent(t *testing.T) {
	d := revisionEvidenceFixture(t)
	for _, id := range []string{"evidence_import", "evidence_screening", "design"} {
		p, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: id, Summary: "Fix", Changes: []string{"Check source claim"}, Reason: "Correct responsible stage"})
		if err != nil {
			t.Fatal(id, err)
		}
		if id == "design" && strings.Contains(strings.Join(p.AffectedStages, " "), "evidence") {
			t.Fatal("wording fix replays evidence")
		}
	}
	f := &revisionFilesFixture{}
	s := &RuntimeService{materialLoader: f, repository: &discussionFilesFixture{}}
	for _, tc := range []struct {
		node, id string
		fail     bool
	}{{"evidence_import", "sent", false}, {"design", "sent", true}, {"evidence_import", "not-sent", true}} {
		_, err := s.prepareRevisionMaterials(context.Background(), d, ProposeResearchRevisionCommand{NodeID: tc.node, AttachmentIDs: []string{tc.id}})
		if (err != nil) != tc.fail {
			t.Fatal(tc, err)
		}
	}
	if f.promoted != 0 {
		t.Fatal("proposal adopted files before consent")
	}
	input := raw(`{"selectedCandidateIds":[],"selectedAttachmentIds":[]}`)
	_, err := s.bindRevisionMaterials(context.Background(), d, CompiledNode{ID: "evidence_import"}, input)
	if err != nil || f.promoted != 0 {
		t.Fatal("no confirmation", err)
	}
	p := ResearchRevisionProposal{NodeID: "evidence_import", Materials: []RevisionMaterial{{AttachmentID: "sent", SHA256: "hash", Name: "paper.md"}, {AttachmentID: "duplicate", SHA256: "hash", Name: "same.md"}}}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": p})})
	got, err := s.bindRevisionMaterials(context.Background(), d, CompiledNode{ID: "evidence_import"}, input)
	if err != nil || f.promoted != 1 || !strings.Contains(string(got), "task-file") {
		t.Fatal(string(got), f.promoted, err)
	}
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "untrusted", FromPort: "x", ToNode: "evidence_sync", ToPort: "attachmentIds"})
	if revisionMaterialChain(d) {
		t.Fatal("ambiguous evidence chain allowed")
	}
}

func TestImportProposalRequiresExplicitMaterialSelection(t *testing.T) {
	err := validateRevisionMaterialSelection(ProposeResearchRevisionCommand{NodeID: "evidence_import", Summary: "Import paper.pdf"})
	if err == nil || !strings.Contains(err.Error(), "attachmentIds") {
		t.Fatalf("omitted selection accepted: %v", err)
	}
	if err := validateRevisionMaterialSelection(ProposeResearchRevisionCommand{NodeID: "evidence_import", AttachmentIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := validateRevisionMaterialSelection(ProposeResearchRevisionCommand{NodeID: "report_drafting"}); err != nil {
		t.Fatal(err)
	}
}
