package researchworkflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/tool"
)

var ErrPublicationIdentityUnavailable = errors.New("legacy report publication identity cannot be verified")

// A publication node is a stream, not one immutable report. Bind a receipt to
// the frozen report, selected citations/artifacts and review, never to attempt
// or CallID (which change during publication-only recovery).
func (s *Service) reportPublicationOperationKey(ctx context.Context, request ReportRequest, calls []tool.Call) (string, error) {
	base := strings.TrimSpace(request.IdempotencyKey)
	if base == "" {
		return "", nil
	}
	if len(base) > 256 || strings.IndexByte(base, 0) >= 0 {
		return "", fmt.Errorf("Workflow report operation key is invalid")
	}
	current, err := publicationSnapshot(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(request.ProjectID+"\x00"+request.WorkflowRunID+"\x00"+base+"\x00"), current...))
	key := fmt.Sprintf("publication-v2:%x", digest)
	prior, found, err := s.artifacts.LegacyWorkflowReportPublication(ctx, request.ProjectID, base)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPublicationIdentityUnavailable, err)
	}
	if !found {
		return key, nil
	}
	if prior.SourceKind != artifact.SourceTool || prior.Provenance.WorkflowRunID != request.WorkflowRunID || prior.Provenance.ToolName != ReportToolName || prior.Provenance.Extra["workflowReport"] != "true" || prior.Provenance.Extra["researchTaskId"] != request.ResearchTaskID {
		return "", ErrPublicationIdentityUnavailable
	}
	for _, call := range calls {
		if call.ID != prior.Provenance.ToolCallID {
			continue
		}
		provider := strings.TrimSpace(call.ProviderCallID)
		if index := strings.LastIndex(provider, ":"); index > 0 {
			provider = provider[:index]
		}
		if call.RunID != request.WorkflowRunID || tool.NormalizeSubjectKind(call.SubjectKind) != tool.SubjectWorkflowRun || call.ToolName != ReportToolName || call.RunID+":"+provider != base {
			return "", ErrPublicationIdentityUnavailable
		}
		var previous struct {
			Name            string             `json:"name"`
			Analysis        json.RawMessage    `json:"reportDraft"`
			Citations       []tool.CitationRef `json:"citations"`
			SourceArtifacts []tool.ArtifactRef `json:"sourceArtifacts"`
			ReviewGate      json.RawMessage    `json:"reviewGate"`
		}
		if json.Unmarshal(call.Arguments, &previous) != nil || previous.Name == "" || len(previous.Analysis) == 0 || len(previous.ReviewGate) == 0 || len(previous.Citations) == 0 {
			return "", ErrPublicationIdentityUnavailable
		}
		old, err := publicationSnapshot(ReportRequest{Name: previous.Name, Analysis: previous.Analysis, Citations: previous.Citations, SourceArtifacts: previous.SourceArtifacts, ReviewGate: previous.ReviewGate})
		if err != nil {
			return "", ErrPublicationIdentityUnavailable
		}
		if !bytes.Equal(old, current) {
			return key, nil
		}
		// Upgrades may have written Markdown but failed during DOCX/PDF. Only
		// verified semantic equality AND exact bytes can reuse that old receipt.
		content := sha256.Sum256([]byte(strings.TrimSpace(request.Markdown) + "\n"))
		if prior.SHA256 != fmt.Sprintf("%x", content) {
			return "", ErrPublicationIdentityUnavailable
		}
		return base, nil
	}
	return "", ErrPublicationIdentityUnavailable
}

func publicationSnapshot(request ReportRequest) ([]byte, error) {
	encoded, err := json.Marshal(map[string]any{
		"name": strings.TrimSpace(request.Name), "reportDraft": request.Analysis,
		"citations": request.Citations, "sourceArtifacts": request.SourceArtifacts, "reviewGate": request.ReviewGate,
	})
	if err != nil {
		return nil, err
	}
	// Preserve numeric precision while normalizing JSON object key order.
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
