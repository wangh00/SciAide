package workflow

import (
	"context"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"os"
	"path/filepath"
	"testing"
)

type taskFilesFixture struct{ discussionFilesFixture }

func (*taskFilesFixture) TaskDiscussionAttachmentIDs(context.Context, string) ([]string, error) {
	return []string{"earlier", "sent"}, nil
}
func (*taskFilesFixture) TaskDiscussionAttachmentOwner(_ context.Context, _ string, id string) (string, error) {
	if id == "earlier" {
		return "old-conversation", nil
	}
	if id == "sent" {
		return "current-conversation", nil
	}
	return "", fmt.Errorf("not sent in task")
}

type ownerFilesFixture struct {
	revisionFilesFixture
	owners        []string
	promotedOwner string
}

func (f *ownerFilesFixture) RevisionMaterials(ctx context.Context, p, owner string, ids []string) ([]attachment.Attachment, error) {
	want := "current-conversation"
	if ids[0] == "earlier" {
		want = "old-conversation"
	}
	if owner != want {
		return nil, fmt.Errorf("incorrect owner %s", owner)
	}
	f.owners = append(f.owners, owner)
	return []attachment.Attachment{{ID: ids[0], SHA256: ids[0] + "-hash", OriginalName: ids[0] + ".md"}}, nil
}
func (f *ownerFilesFixture) PromoteRevisionMaterial(ctx context.Context, p, owner, task, id, sha string) (attachment.Attachment, error) {
	if owner != "old-conversation" {
		return attachment.Attachment{}, fmt.Errorf("wrong promotion owner")
	}
	f.promotedOwner = owner
	return attachment.Attachment{ID: "promoted", SHA256: sha}, nil
}
func TestRevisionMaterialsUseTaskScopeAndOriginalOwner(t *testing.T) {
	d := revisionEvidenceFixture(t)
	d.Run.ConversationID = "current-conversation"
	files := &ownerFilesFixture{}
	runtime := &RuntimeService{repository: &taskFilesFixture{}, materialLoader: files}
	cmd := ProposeResearchRevisionCommand{NodeID: "evidence_import", AttachmentIDs: []string{"earlier", "sent"}}
	got, err := runtime.prepareRevisionMaterials(context.Background(), d, cmd)
	if err != nil || len(got) != 2 || got[0].AttachmentID != "earlier" || got[1].AttachmentID != "sent" {
		t.Fatal(got, err)
	}
	if files.promotedOwner != "" {
		t.Fatal("promoted before confirmation")
	}
	for _, ids := range [][]string{{"earlier", "earlier"}, {"foreign"}, make([]string, 17)} {
		if _, err := runtime.prepareRevisionMaterials(context.Background(), d, ProposeResearchRevisionCommand{NodeID: "evidence_import", AttachmentIDs: ids}); err == nil {
			t.Fatal("invalid selection accepted", ids)
		}
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{Materials: got[:1]}})})
	if _, err := runtime.bindRevisionMaterials(context.Background(), d, CompiledNode{ID: "evidence_import"}, raw(`{"selectedAttachmentIds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if files.promotedOwner != "old-conversation" {
		t.Fatal("original owner lost")
	}
}

// Source and table promotion must use the same original owner as literature.
type taskSourceFixture struct {
	revisionFilesFixture
	path  string
	owner string
}

func (f *taskSourceFixture) OpenRevisionSource(_ context.Context, _, owner, id string) (attachment.Attachment, *os.File, error) {
	f.owner = owner
	if owner != "old-conversation" {
		return attachment.Attachment{}, nil, fmt.Errorf("wrong source owner")
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return attachment.Attachment{}, nil, err
	}
	file, err := os.Open(f.path)
	return attachment.Attachment{ID: id, OriginalName: "analysis.py", SHA256: hashBytes(data), SizeBytes: int64(len(data))}, file, err
}
func TestRevisionSourceUsesEarlierTaskConversationWithoutExecutingUpload(t *testing.T) {
	d := revisionDataFixture()
	d.Run.ConversationID = "current-conversation"
	d.Run.Compilation.Nodes[1].PromptVersion = dynamicImplementationPromptVersion
	d.Run.Compilation.Nodes[1].AllowedTools = []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "method_implementation", FromPort: "analysis.code", ToNode: "python_analysis", ToPort: "code"})
	f := &taskSourceFixture{path: filepath.Join(t.TempDir(), "analysis.py")}
	source := []byte("raise RuntimeError('uploaded reference must not execute')\n")
	if err := os.WriteFile(f.path, source, 0600); err != nil {
		t.Fatal(err)
	}
	s := &RuntimeService{repository: &taskFilesFixture{}, materialLoader: f, projects: fixedProjectLoader{project.Project{WorkspacePath: t.TempDir()}}}
	version, err := s.prepareRevisionSource(context.Background(), d, ProposeResearchRevisionCommand{NodeID: "method_implementation", SourceInputs: &RevisionSourceSelection{AttachmentIDs: []string{"earlier"}}})
	if err != nil || len(version.Files) != 1 || version.Files[0].SHA256 != hashBytes(source) || f.owner != "old-conversation" {
		t.Fatal(version, err)
	}
	if err := s.validateRevisionSource(context.Background(), d, ResearchRevisionProposal{NodeID: "method_implementation", SourceVersion: version}); err != nil {
		t.Fatal("source cannot survive proposal validation", err)
	}
	if _, err := s.prepareRevisionSource(context.Background(), d, ProposeResearchRevisionCommand{NodeID: "method_implementation", SourceInputs: &RevisionSourceSelection{AttachmentIDs: []string{"foreign"}}}); err == nil {
		t.Fatal("foreign source allowed")
	}
}
