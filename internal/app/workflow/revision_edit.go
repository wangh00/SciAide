package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/citation"
)

const RevisionEditVersion = "revision-edit-v1"

type revisionEditReceipt struct {
	Version                string          `json:"version"`
	BaseSHA256             string          `json:"baseSHA256"`
	SourceStepID           string          `json:"sourceStepId"`
	SourceAttempt          int             `json:"sourceAttempt"`
	BaselinePointer        string          `json:"baselinePointer"`
	PriorOutput            json.RawMessage `json:"priorOutput,omitempty"`
	CharacterSourceVersion string          `json:"characterSourceVersion,omitempty"`
	CharacterSourceSHA256  string          `json:"characterSourceSha256,omitempty"`
}

// RevisionEditBase is host-only. It is never decoded from model arguments.
type RevisionEditBase struct {
	Analysis json.RawMessage
	SHA256   string
	Pointer  string
	// Only host-verified current-Chat seeds may supply these aliases.
	CitationAliases map[string]string
	Characters      *RevisionCharacterSource
}

func supportsRevisionEdit(node CompiledNode) bool {
	return node.ID != "method_implementation" && (schemaDeclaresProperty(node.OutputSchema, "deliverables") || usesReportProvenance(node))
}

// Resolve the latest confirmed authority, not the last AI draft, a model-visible
// priorOutput preview, or the earliest user revision superseded by review.
func (s *RuntimeService) revisionEditBaseline(ctx context.Context, d RunDetail, step Step) (json.RawMessage, *Step, error) {
	p, snapshot, handled, err := s.designReviewWriteAuthority(ctx, d, step)
	if !handled && err == nil {
		p, err = currentDesignIsolationProposal(d, step)
		if p == nil && err == nil && usesReportProvenance(compilationNodeMap(d.Run.Compilation)[step.NodeID]) {
			p, err = confirmedReportEditProposal(d, step)
		}
		if p != nil && err == nil {
			reader, ok := s.repository.(interface {
				ReadResearchRevisionSnapshot(context.Context, string, string) (json.RawMessage, error)
			})
			if !ok {
				return nil, nil, fmt.Errorf("无法读取局部编辑的已确认原稿快照")
			}
			snapshot, err = reader.ReadResearchRevisionSnapshot(ctx, d.Run.ID, p.ID)
		}
	}
	if err != nil || p == nil {
		return nil, nil, err
	}
	var before RunDetail
	if json.Unmarshal(snapshot, &before) != nil || before.Run.ID != d.Run.ID || before.Run.CompilationSHA256 != d.Run.CompilationSHA256 || ResearchRevisionSnapshot(before) != p.SnapshotSHA256 {
		return nil, nil, fmt.Errorf("局部编辑原稿的任务、方案或快照摘要不一致")
	}
	prior := findStepByNode(before.Steps, step.NodeID)
	if prior == nil || prior.Status != StepCompleted {
		return nil, nil, nil // No completed draft exists; use normal full submission.
	}
	if prior.ID != step.ID || prior.Ordinal != step.Ordinal || prior.Attempt >= step.Attempt {
		return nil, nil, fmt.Errorf("局部编辑原稿的阶段或尝试不一致")
	}
	base, err := outputPort(prior.Output, "analysis")
	if err != nil {
		return nil, nil, err
	}
	if _, err := decodeIsolationValue(base); err != nil {
		return nil, nil, err
	}
	if usesReportProvenance(compilationNodeMap(d.Run.Compilation)[step.NodeID]) {
		base, err = revisionEditReportBase(base, prior.Input)
		if err != nil {
			return nil, nil, err
		}
	}
	return base, prior, nil
}

func confirmedReportEditProposal(d RunDetail, step Step) (*ResearchRevisionProposal, error) {
	// Report-only proposals need no design mask. Reuse their own confirmed
	// snapshot, without inventing a design-isolation version or reviving a
	// proposal superseded by a later independent-review generation.
	for i := len(d.Events) - 1; i >= 0; i-- {
		event := d.Events[i]
		if event.Type == "workflow.review_revision_queued" {
			var review reviewRevision
			if json.Unmarshal(event.Payload, &review) != nil {
				return nil, fmt.Errorf("后续报告审查返修事件无法读取")
			}
			producer := findStepByNode(d.Steps, review.ProducerNodeID)
			if producer == nil || producer.ID != review.ProducerStepID {
				return nil, fmt.Errorf("后续报告审查返修的起点不一致")
			}
			if producer.Ordinal <= step.Ordinal {
				return nil, nil
			}
		}
		if event.Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Proposal ResearchRevisionProposal `json:"proposal"`
			Start    *int                     `json:"startOrdinal"`
		}
		if json.Unmarshal(event.Payload, &saved) != nil || saved.Start == nil {
			return nil, fmt.Errorf("报告返修的确认事件无法读取")
		}
		if *saved.Start > step.Ordinal {
			continue
		}
		start := findStepByNode(d.Steps, saved.Proposal.NodeID)
		if event.WorkflowRunID != d.Run.ID || start == nil || start.Ordinal != *saved.Start || saved.Proposal.RunID != d.Run.ID {
			return nil, fmt.Errorf("报告返修确认事件与当前任务绑定不一致")
		}
		for _, p := range d.RevisionProposals {
			if p.ID == saved.Proposal.ID && p.Status == "confirmed" && sameIsolationProposal(p, saved.Proposal) {
				return &p, nil
			}
		}
		return nil, fmt.Errorf("报告返修缺少一致的已确认提案与原事件")
	}
	return nil, nil
}

func revisionEditReportBase(base, sourceInput json.RawMessage) (json.RawMessage, error) {
	// Only exactly regenerated host records are removed. All author metadata
	// and body bytes stay in the edit base; the normal validator assembles the
	// CURRENT records after the explicit submission. Never strip by heading.
	var fields map[string]json.RawMessage
	var markdown string
	if json.Unmarshal(base, &fields) != nil || json.Unmarshal(fields["markdown"], &markdown) != nil || !usesExplicitReportAssembly(sourceInput) {
		return base, nil
	}
	parts, err := assembledReportAppendices(markdown, sourceInput)
	if err != nil {
		return nil, err
	}
	suffix := strings.Join(parts, "\n\n")
	endTrimmed := strings.TrimRightFunc(markdown, unicode.IsSpace)
	if suffix == "" || !strings.HasSuffix(endTrimmed, suffix) {
		return base, nil
	}
	body := strings.TrimSuffix(endTrimmed, suffix)
	// Remove only the proven separator adjacent to the exact generated
	// suffix. A same-looking heading or changed appendix is never removed.
	if strings.HasSuffix(body, "\n\n") {
		body = strings.TrimSuffix(body, "\n\n")
	} else if strings.HasSuffix(body, "\n") {
		body = strings.TrimSuffix(body, "\n")
	} else {
		return base, nil
	}
	fields["markdown"] = mustJSON(body)
	return json.Marshal(fields)
}

func equalRevisionEditBase(a, b json.RawMessage) bool {
	x, e1 := decodeIsolationValue(a)
	y, e2 := decodeIsolationValue(b)
	if e1 != nil || e2 != nil {
		return false
	}
	left, e1 := json.Marshal(x)
	right, e2 := json.Marshal(y)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}

// Opt in only while assembling a NEW frozen stage input. Existing AI executions
// and their input/prompt hashes are not hot-upgraded.
func (s *RuntimeService) bindRevisionEditContext(ctx context.Context, d RunDetail, node CompiledNode, input json.RawMessage) (json.RawMessage, error) {
	if !supportsRevisionEdit(node) {
		return input, nil
	}
	step := findStepByNode(d.Steps, node.ID)
	if step == nil || step.Status != StepQueued {
		return input, nil
	}
	future := *step
	future.Attempt++
	base, prior, err := s.revisionEditBaseline(ctx, d, future)
	if err != nil {
		return nil, writeBaselineError(err)
	}
	if prior == nil {
		return input, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil || fields == nil {
		return nil, writeBaselineError(fmt.Errorf("无法绑定局部编辑的阶段输入"))
	}
	receipt := revisionEditReceipt{Version: RevisionEditVersion, BaseSHA256: hashJSON(base), SourceStepID: prior.ID, SourceAttempt: prior.Attempt}
	// Reuse an already-present complete base instead of copying it again. The
	// pointer is navigation only; the live host rechecks the immutable source.
	for _, pointer := range []string{"/_reviewCorrections/priorOutput", "/_userRevision/priorOutput/analysis"} {
		material, err := ResolveResearchStageMaterial(input, pointer)
		if err == nil && material.Encoding == "json" && equalRevisionEditBase(json.RawMessage(material.Content), base) {
			receipt.BaselinePointer = pointer
			break
		}
	}
	if receipt.BaselinePointer == "" {
		receipt.BaselinePointer, receipt.PriorOutput = "/_revisionEdit/priorOutput", cloneRaw(base)
	}
	if source := revisionCharacterSource(input, base); source != nil {
		receipt.CharacterSourceVersion, receipt.CharacterSourceSHA256 = source.Version, source.SHA256
	}
	fields["_revisionEdit"] = mustJSON(receipt)
	return json.Marshal(fields)
}

// StageRevisionEdit shares the real Workflow/Chat binding and validator; it
// neither starts an executor nor grants permission to write any new field.
func (s *RuntimeService) StageRevisionEdit(ctx context.Context, command StageMaterialRequest) (*RevisionEditBase, error) {
	d, err := s.repository.GetRun(ctx, command.ProjectID, command.WorkflowRunID)
	if err != nil {
		return nil, err
	}
	step := currentStep(d)
	if step == nil {
		return nil, fmt.Errorf("局部编辑阶段已不再运行")
	}
	var fields map[string]json.RawMessage
	var receipt revisionEditReceipt
	if json.Unmarshal(step.Input, &fields) != nil {
		return nil, fmt.Errorf("局部编辑阶段输入无效")
	}
	if len(fields["_revisionEdit"]) == 0 {
		return nil, nil // Legacy frozen inputs keep the original protocol.
	}
	if json.Unmarshal(fields["_revisionEdit"], &receipt) != nil || receipt.Version != RevisionEditVersion {
		return nil, writeBaselineError(fmt.Errorf("局部编辑原稿回执无效"))
	}
	node := compilationNodeMap(d.Run.Compilation)[step.NodeID]
	if !supportsRevisionEdit(node) || d.Run.ID != command.WorkflowRunID || d.Run.ProjectID != command.ProjectID || d.Run.ConversationID != command.ConversationID || d.Run.Status != RunRunning || d.Run.CancelRequested || step.Status != StepRunning || step.ID != command.WorkflowStepID || step.WorkflowRunID != d.Run.ID || step.Attempt != command.Attempt || command.Attempt < 1 || step.InputSHA256 != command.InputSHA256 || hashJSON(step.Input) != command.InputSHA256 {
		return nil, writeBaselineError(fmt.Errorf("局部编辑与当前任务、阶段或冻结输入绑定不一致"))
	}
	compilation := d.Run.Compilation
	if d.Run.InputsSHA256 == "" || hashJSON(d.Run.Inputs) != d.Run.InputsSHA256 || d.Run.CompilationSHA256 == "" || compilation.CompilationSHA256 != d.Run.CompilationSHA256 {
		return nil, writeBaselineError(fmt.Errorf("局部编辑的冻结任务或方案摘要不一致"))
	}
	compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(compilation)
	if err != nil || hashBytes(encoded) != d.Run.CompilationSHA256 {
		return nil, writeBaselineError(fmt.Errorf("局部编辑的冻结方案校验失败"))
	}
	execution, exists, err := s.repository.GetAIExecutionForStep(ctx, step.ID, step.Attempt)
	if err != nil {
		return nil, err
	}
	if !exists || command.ChatRunID == "" || execution.ChatRunID != command.ChatRunID || execution.WorkflowRunID != d.Run.ID || execution.WorkflowStepID != step.ID || execution.Attempt != step.Attempt || execution.InputSHA256 != command.InputSHA256 {
		return nil, writeBaselineError(fmt.Errorf("局部编辑的 AI 执行绑定已变化"))
	}
	base, prior, err := s.revisionEditBaseline(ctx, d, *step)
	if err != nil {
		return nil, writeBaselineError(err)
	}
	if prior == nil || receipt.BaseSHA256 != hashJSON(base) || receipt.SourceStepID != prior.ID || receipt.SourceAttempt != prior.Attempt {
		return nil, writeBaselineError(fmt.Errorf("局部编辑原稿已变化或缺失，不能改用失败候选"))
	}
	material, err := ResolveResearchStageMaterial(step.Input, receipt.BaselinePointer)
	if err != nil || material.Encoding != "json" || !equalRevisionEditBase(json.RawMessage(material.Content), base) {
		return nil, writeBaselineError(fmt.Errorf("局部编辑可见原稿与已确认快照不一致"))
	}
	seeds, err := s.workflowAICitationSeeds(ctx, d, *step)
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	if len(seeds) > 0 {
		reissued, err := citation.ReissueKnowledgeRefs(d.Run.ID, command.ChatRunID, d.Run.ProjectID, seeds)
		if err != nil {
			return nil, writeBaselineError(err)
		}
		for i, ref := range reissued {
			if prior, exists := aliases[ref.Reference]; exists && prior != seeds[i].Reference {
				return nil, writeBaselineError(fmt.Errorf("局部编辑的当前引用域存在歧义"))
			}
			aliases[ref.Reference] = seeds[i].Reference
		}
	}
	var characters *RevisionCharacterSource
	if receipt.CharacterSourceVersion != "" || receipt.CharacterSourceSHA256 != "" {
		characters = revisionCharacterSource(step.Input, base)
		if characters == nil || receipt.CharacterSourceVersion != revisionCharacterSourceVersion || characters.SHA256 != receipt.CharacterSourceSHA256 {
			return nil, writeBaselineError(fmt.Errorf("局部编辑的原文字形来源或摘要不一致"))
		}
	}
	return &RevisionEditBase{Analysis: cloneRaw(base), SHA256: hashJSON(base), Pointer: receipt.BaselinePointer, CitationAliases: aliases, Characters: characters}, nil
}
