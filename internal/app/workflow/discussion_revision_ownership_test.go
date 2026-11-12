package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func completedMaterialDiscussionFixture(t *testing.T) RunDetail {
	t.Helper()
	d := materialRevisionFixture(t)
	bindMaterialOwnershipFixture(t, &d)
	d.Run.Status = RunCompleted
	d.Steps[5].Status = StepCompleted
	return d
}

type discussionInputCommandFixture struct {
	RuntimeRepository
	ResearchDiscussionRepository
	detail RunDetail
	saved  int
}

func (f *discussionInputCommandFixture) GetRun(context.Context, string, string) (RunDetail, error) {
	return f.detail, nil
}
func (f *discussionInputCommandFixture) ResolveResearchDiscussion(context.Context, string) (ResearchDiscussionBinding, error) {
	return ResearchDiscussionBinding{ProjectID: f.detail.Run.ProjectID, WorkflowRunID: f.detail.Run.ID, UserMessageID: "human"}, nil
}
func (f *discussionInputCommandFixture) SaveResearchRevisionProposal(_ context.Context, p ResearchRevisionProposal) (ResearchRevisionProposal, error) {
	f.saved++
	return p, nil
}
func (*discussionInputCommandFixture) DiscussionAttachmentIDs(context.Context, string) ([]string, error) {
	return []string{"sent"}, nil
}

func TestDiscussionServiceNormalizesTargetBeforeExplicitInputConsent(t *testing.T) {
	for _, tt := range []struct {
		name string
		ids  []string
		bad  bool
	}{{"omitted must not become reuse", nil, true}, {"explicit reuse", []string{}, false}, {"explicit sent file", []string{"sent"}, false}} {
		t.Run(tt.name, func(t *testing.T) {
			r := &discussionInputCommandFixture{detail: revisionEvidenceFixture(t)}
			s := &RuntimeService{repository: r, materialLoader: &revisionFilesFixture{}, now: time.Now, newID: func() (string, error) { return "proposal", nil }}
			p, err := s.ProposeResearchRevision(context.Background(), "chat", "call", ProposeResearchRevisionCommand{NodeID: " evidence_import ", Summary: "Read the chosen source", Changes: []string{"Reassess actual evidence"}, Reason: "Explicitly selected materials", AttachmentIDs: tt.ids})
			if (err != nil) != tt.bad || tt.bad && r.saved != 0 || !tt.bad && (p.NodeID != "evidence_import" || r.saved != 1) {
				t.Fatal("input consent normalization mismatch", p, err, r.saved)
			}
		})
	}
}

func TestDiscussionConfirmationEventBindsExactReplayMetadata(t *testing.T) {
	d := reviewedDeliveryFixture(t)
	d.Steps[0].Output = raw(`{"analysis":{"large":9007199254740992,"precise":0.123456789012345678901}}`)
	p := ResearchRevisionProposal{RunID: d.Run.ID, NodeID: "design", Status: "pending", CanConfirm: true}
	payload, err := ResearchRevisionConfirmationPayload(d, p)
	if err != nil {
		t.Fatal(err)
	}
	event := RuntimeEvent{WorkflowRunID: d.Run.ID, Type: "workflow.user_revision_queued", Payload: mustJSON(payload)}
	if err := ValidateResearchRevisionConfirmationEvent(d, p, event); err != nil {
		t.Fatal("real host payload rejected", err)
	}
	for _, tt := range []struct {
		name string
		key  string
		data json.RawMessage
	}{
		{"wrong ordinal", "startOrdinal", raw(`999`)},
		{"missing ordinal", "startOrdinal", nil},
		{"wrong prior", "priorOutput", raw(`{}`)},
		{"large integer differs by one", "priorOutput", raw(`{"analysis":{"large":9007199254740993,"precise":0.123456789012345678901}}`)},
		{"low precision difference", "priorOutput", raw(`{"analysis":{"large":9007199254740992,"precise":0.123456789012345678902}}`)},
		{"changed baseline", "comparisonBaseline", raw(`{"injected":true}`)},
		{"unknown metadata", "extra", raw(`true`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(event.Payload, &fields)
			if tt.data == nil {
				delete(fields, tt.key)
			} else {
				fields[tt.key] = tt.data
			}
			modified := event
			modified.Payload = mustJSON(fields)
			if ValidateResearchRevisionConfirmationEvent(d, p, modified) == nil {
				t.Fatal("changed replay metadata accepted")
			}
		})
	}
}

func TestDiscussionMaterialRevisionRequiresWriteDeclaration(t *testing.T) {
	d := completedMaterialDiscussionFixture(t)
	_, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "report_drafting", Summary: "Correct spelling", Changes: []string{"Correct the frozen design appendix and report introduction"}, Reason: "Only spelling changes"})
	if err == nil {
		t.Fatal("report-only proposal without declared write objects can confirm edits to frozen design materials")
	}
}

func TestDiscussionMaterialRevisionSelectsActualOwner(t *testing.T) {
	for _, tt := range []struct {
		name, node string
		kinds      []string
		bad        bool
	}{
		{"material spelling is not report-only", "report_drafting", []string{"frozen_design_material"}, true},
		{"material producer", "method_implementation", []string{"frozen_design_material"}, false},
		{"authored wording only", "report_drafting", []string{"report_authored_body"}, false},
		{"normalized host target", " report_drafting ", []string{"report_authored_body"}, false},
		{"mixed owner declarations", "method_implementation", []string{"report_authored_body", "frozen_design_material"}, false},
		{"unnecessary earlier restart", "method_selection", []string{"frozen_design_material"}, true},
		{"duplicate declaration", "method_implementation", []string{"frozen_design_material", "frozen_design_material"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := completedMaterialDiscussionFixture(t)
			contract, err := discussionRevisionWriteContract(d)
			if err != nil {
				t.Fatal(err)
			}
			c := ProposeResearchRevisionCommand{NodeID: tt.node, Summary: "Correct spelling", Changes: []string{"Correct requested typos without scientific changes"}, Reason: "Current write ownership"}
			for _, kind := range tt.kinds {
				c.EditTargets = append(c.EditTargets, writeObjectID(t, contract, kind))
			}
			p, err := newResearchRevisionProposal(d, c)
			if (err != nil) != tt.bad {
				t.Fatalf("proposal=%+v err=%v", p, err)
			}
			if !tt.bad {
				if len(p.WriteContract) == 0 || p.WriteContractVersion != revisionWriteVersion || p.WriteContractSHA256 != hashJSON(p.WriteContract) || !reflect.DeepEqual(p.EditTargets, c.EditTargets) {
					t.Fatal("declaration not frozen", p)
				}
				if err := ValidateResearchRevisionWriteBinding(d, p); err != nil {
					t.Fatal("new binding invalid", err)
				}
				c.EditTargets[0] = "mutated-command"
				if p.EditTargets[0] == c.EditTargets[0] {
					t.Fatal("proposal aliases caller declaration")
				}
			}
		})
	}
}

func TestDiscussionRevisionBindingAndProjectionFailClosed(t *testing.T) {
	d := completedMaterialDiscussionFixture(t)
	contract, _ := discussionRevisionWriteContract(d)
	p, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "report_drafting", Summary: "Fix introduction", Changes: []string{"Fix report-authored typo"}, Reason: "No material edits", EditTargets: []string{writeObjectID(t, contract, "report_authored_body")}})
	if err != nil {
		t.Fatal(err)
	}
	p.CanConfirm = true
	for _, tt := range []struct {
		name   string
		change func(*RunDetail, *ResearchRevisionProposal)
	}{
		{"missing receipt", func(_ *RunDetail, p *ResearchRevisionProposal) {
			p.WriteContract = nil
			p.WriteContractSHA256 = ""
			p.WriteContractVersion = ""
		}},
		{"old unbound card", func(_ *RunDetail, p *ResearchRevisionProposal) {
			p.WriteContract = nil
			p.WriteContractSHA256 = ""
			p.WriteContractVersion = ""
			p.EditTargets = nil
		}},
		{"wrong version", func(_ *RunDetail, p *ResearchRevisionProposal) { p.WriteContractVersion = "unknown" }},
		{"wrong hash", func(_ *RunDetail, p *ResearchRevisionProposal) { p.WriteContractSHA256 = "stale" }},
		{"equivalent reordered receipt bytes", func(_ *RunDetail, p *ResearchRevisionProposal) {
			p.WriteContract = mustJSON(decodeObject(p.WriteContract))
		}},
		{"corrupt receipt", func(_ *RunDetail, p *ResearchRevisionProposal) {
			p.WriteContract = raw(`{}`)
			p.WriteContractSHA256 = hashJSON(p.WriteContract)
		}},
		{"unknown selection", func(_ *RunDetail, p *ResearchRevisionProposal) { p.EditTargets = []string{"edit-stale"} }},
		{"wrong owner", func(_ *RunDetail, p *ResearchRevisionProposal) {
			p.EditTargets = []string{writeObjectID(t, contract, "frozen_design_material")}
		}},
		{"changed attempt", func(d *RunDetail, _ *ResearchRevisionProposal) {
			d.Steps = append([]Step(nil), d.Steps...)
			d.Steps[1].Attempt++
		}},
		{"stale snapshot", func(_ *RunDetail, p *ResearchRevisionProposal) { p.SnapshotSHA256 = "stale" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail, proposal := d, p
			tt.change(&detail, &proposal)
			before := string(mustJSON(proposal))
			got := ProjectResearchRevisionProposals(detail, []ResearchRevisionProposal{proposal})
			if len(got) != 1 || got[0].CanConfirm || got[0].Status != "pending" || got[0].ConfirmationBlockedReason == "" {
				t.Fatalf("invalid proposal still ready or unexplained: %+v", got)
			}
			if before != string(mustJSON(proposal)) {
				t.Fatal("projection mutated history")
			}
			if tt.name != "stale snapshot" && ValidateResearchRevisionWriteBinding(detail, proposal) == nil {
				t.Fatal("confirmation-boundary validator accepted broken binding")
			}
		})
	}
	got := ProjectResearchRevisionProposals(d, []ResearchRevisionProposal{p})
	if !got[0].CanConfirm || got[0].ConfirmationBlockedReason != "" {
		t.Fatal("valid pending proposal blocked", got)
	}
	confirmed := p
	confirmed.Status, confirmed.WriteContract = "confirmed", nil
	if ProjectResearchRevisionProposals(d, []ResearchRevisionProposal{confirmed})[0].ConfirmationBlockedReason != "" {
		t.Fatal("confirmed historical proposal regraded")
	}
}

func TestDiscussionRevisionOverviewAndStageContextPreserveBinding(t *testing.T) {
	d := completedMaterialDiscussionFixture(t)
	overview := decodeObject(discussionRevisionOverview(d))
	if overview["revisionWriteContract"] == nil || overview["revisionTargetCapabilities"] == nil || overview["snapshotSha256"] != ResearchRevisionSnapshot(d) || overview["revisionWriteContractSha256"] == nil {
		t.Fatal("discussion has no actual write navigation", overview)
	}
	contract, _ := discussionRevisionWriteContract(d)
	p, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "method_implementation", Summary: "Fix typos", Changes: []string{"Correct both the frozen material and report introduction"}, Reason: "The material has an earlier owner", EditTargets: []string{writeObjectID(t, contract, "frozen_design_material"), writeObjectID(t, contract, "report_authored_body")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"method_implementation", "report_drafting", "independent_review"} {
		context := userRevisionStageContext(d, compilationNodeMap(d.Run.Compilation)[id], p)
		if !rawJSONEqual(mustJSON(context["writeContract"]), p.WriteContract) || context["writeContractSha256"] != p.WriteContractSHA256 || !reflect.DeepEqual(context["editTargets"], p.EditTargets) || !reflect.DeepEqual(context["changes"], p.Changes) {
			t.Fatal("downstream declaration was dropped", id)
		}
	}
}

func TestDiscussionRevisionHistoricalRoutesAreNotSilentlyUpgraded(t *testing.T) {
	d := completedMaterialDiscussionFixture(t)
	fields := decodeObject(d.Steps[4].Input)
	delete(fields, "revisionWriteContract")
	d.Steps[4].Input = mustJSON(fields)
	d.Steps[4].InputSHA256 = hashJSON(d.Steps[4].Input)
	before := string(mustJSON(d.Run.Compilation))
	contract, err := discussionRevisionWriteContract(d)
	if err != nil || contract == nil {
		t.Fatal("historical material route lacks real-object navigation", err)
	}
	_, err = newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "method_implementation", Summary: "Fix old material", Changes: []string{"Correct actual design material"}, Reason: "Write at original owner", EditTargets: []string{writeObjectID(t, contract, "frozen_design_material")}})
	if err != nil || string(mustJSON(d.Run.Compilation)) != before || decodeObject(d.Steps[4].Input)["revisionWriteContract"] != nil {
		t.Fatal("history was changed or valid new proposal rejected", err)
	}
	nonMaterial := reviewedDeliveryFixture(t)
	p, err := newResearchRevisionProposal(nonMaterial, ProposeResearchRevisionCommand{NodeID: "design", Summary: "Clarify", Changes: []string{"Clarify the design"}, Reason: "User request"})
	if err != nil || len(p.WriteContract) != 0 || ValidateResearchRevisionWriteBinding(nonMaterial, p) != nil {
		t.Fatal("unrelated legacy route blocked", err)
	}
	_, err = newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "report_drafting", Summary: "Fake source bypass", Changes: []string{"Edit design"}, Reason: "Source", SourceInputs: &RevisionSourceSelection{}})
	if err == nil || !strings.Contains(err.Error(), "editTargets") {
		t.Fatal("invalid source selection bypassed object binding", err)
	}
	var old ResearchRevisionProposal
	if json.Unmarshal(mustJSON(p), &old) != nil || ValidateResearchRevisionWriteBinding(nonMaterial, old) != nil {
		t.Fatal("legacy round trip broken")
	}
}
