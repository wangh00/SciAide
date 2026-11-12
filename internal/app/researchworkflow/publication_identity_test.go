package researchworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type legacyPublicationFixture struct {
	workflowArtifactsFixture
	version artifact.Version
	found   bool
}

func (f *legacyPublicationFixture) LegacyWorkflowReportPublication(context.Context, string, string) (artifact.Version, bool, error) {
	return f.version, f.found, nil
}

func publicationIdentityFixture(t *testing.T) (*Service, ReportRequest, []tool.Call, *legacyPublicationFixture) {
	t.Helper()
	r := ReportRequest{ProjectID: "project", ResearchTaskID: "task", WorkflowRunID: "run", ToolCallID: "new-call", IdempotencyKey: "run:workflow:publish", Name: "Report", Markdown: "# Report\nVerified.", Analysis: json.RawMessage(`{"markdown":"# Report\nVerified.","confidence":"medium"}`), Citations: []tool.CitationRef{{Reference: "[K-A]", Quote: "original"}}, SourceArtifacts: []tool.ArtifactRef{}, ReviewGate: json.RawMessage(`{"approved":true,"reviewSha256":"review-1"}`)}
	args, err := publicationSnapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "old-call", RunID: "run", SubjectKind: tool.SubjectWorkflowRun, ProviderCallID: "workflow:publish:1", ToolName: ReportToolName, Status: tool.CallFailed, Arguments: args}
	h := sha256.Sum256([]byte(r.Markdown + "\n"))
	f := &legacyPublicationFixture{found: true, version: artifact.Version{ID: "old-version", SourceKind: artifact.SourceTool, SHA256: fmt.Sprintf("%x", h), Provenance: artifact.Provenance{WorkflowRunID: "run", ToolCallID: "old-call", ToolName: ReportToolName, Extra: map[string]string{"workflowReport": "true", "researchTaskId": "task"}}}}
	return &Service{artifacts: f}, r, []tool.Call{call}, f
}

func TestPublicationIdentityKeepsLegacyPartialExportAndVersionsRevisions(t *testing.T) {
	s, r, calls, f := publicationIdentityFixture(t)
	key, err := s.reportPublicationOperationKey(context.Background(), r, calls)
	if err != nil || key != r.IdempotencyKey {
		t.Fatal("same frozen publication must reuse legacy receipt", key, err)
	}
	// Retry attempt/CallID are not publication identity; old partial ToolCall
	// can be failed while its Markdown receipt already exists.
	r.ToolCallID = "retry-call"
	key, err = s.reportPublicationOperationKey(context.Background(), r, calls)
	if err != nil || key != r.IdempotencyKey {
		t.Fatal("retry duplicated legacy publication", key, err)
	}
	r.Analysis = json.RawMessage(`{"confidence":"medium","markdown":"# Revised\nVerified."}`)
	r.Markdown = "# Revised\nVerified."
	revised, err := s.reportPublicationOperationKey(context.Background(), r, calls)
	if err != nil || revised == key || !strings.HasPrefix(revised, "publication-v2:") || len(revised) > 256 {
		t.Fatal("revision kept old receipt", revised, err)
	}
	f.found = false
	r.ToolCallID = "another-retry"
	again, err := s.reportPublicationOperationKey(context.Background(), r, calls)
	if err != nil || again != revised {
		t.Fatal("revision recovery changed identity", again, err)
	}
	r.Analysis = json.RawMessage("{\n\"markdown\":\"# Revised\\nVerified.\",\"confidence\":\"medium\"}")
	again, err = s.reportPublicationOperationKey(context.Background(), r, calls)
	if err != nil || again != revised {
		t.Fatal("object key order changed publication identity", again, err)
	}
}

func TestPublicationIdentityDoesNotReuseSameProseWithChangedEvidenceOrReview(t *testing.T) {
	for _, field := range []string{"review", "citations", "artifacts", "name", "project", "node"} {
		t.Run(field, func(t *testing.T) {
			s, r, calls, f := publicationIdentityFixture(t)
			original, err := s.reportPublicationOperationKey(context.Background(), r, calls)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "review":
				r.ReviewGate = json.RawMessage(`{"approved":true,"reviewSha256":"review-2"}`)
			case "citations":
				r.Citations[0].Quote = "different"
			case "artifacts":
				r.SourceArtifacts = []tool.ArtifactRef{{WorkspacePath: "changed.csv"}}
			case "name":
				r.Name = "Changed title"
			case "project":
				r.ProjectID = "another-project"
				f.found = false
			case "node":
				r.IdempotencyKey = "run:workflow:other-publish"
				f.found = false
			}
			key, err := s.reportPublicationOperationKey(context.Background(), r, calls)
			if err != nil || key == original || key == r.IdempotencyKey {
				t.Fatal("changed publication reused old identity", key, err)
			}
		})
	}
}

func TestPublicationIdentityUnverifiableLegacyStopsWithoutGuessing(t *testing.T) {
	for _, kind := range []string{"missing-call", "wrong-node", "wrong-task", "bad-input", "byte-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			s, r, calls, f := publicationIdentityFixture(t)
			switch kind {
			case "missing-call":
				calls = nil
			case "wrong-node":
				calls[0].ProviderCallID = "workflow:other:1"
			case "wrong-task":
				f.version.Provenance.Extra["researchTaskId"] = "other-task"
			case "bad-input":
				calls[0].Arguments = json.RawMessage(`{}`)
			case "byte-mismatch":
				f.version.SHA256 = strings.Repeat("0", 64)
			}
			if _, err := s.reportPublicationOperationKey(context.Background(), r, calls); !errors.Is(err, ErrPublicationIdentityUnavailable) {
				t.Fatal("unverified legacy receipt silently bypassed", err)
			}
		})
	}
}
