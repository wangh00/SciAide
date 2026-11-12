package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/wangh00/SciAide/internal/apperr"
)

const designWriteIsolationVersion = "design-material-isolation-v1"
const revisionWriteBaselineInvalid = "WORKFLOW_REVISION_WRITE_BASELINE_INVALID"

const designWriteIsolationInstruction = "For a selected design material owner, change only the selected material markdown fields. Unselected material bodies, titles, order and every other design field must remain exactly as in its confirmed original snapshot. A selected collection permits tail additions only. Only an explicit stage_result grants whole-analysis edits. A later host-confirmed _reviewRevision.designWriteIsolation or _reviewCorrections.designWriteIsolation supersedes the older _userRevision write scope: use its editTargets and the current review producer's priorSubject or that owner's _reviewCorrections.priorOutput, not the older user priorOutput. Without such a successor use the complete _userRevision.priorOutput or an already authorized revision_stage_output read with its proposalId. Use authorized paging when needed; never reconstruct from a truncated comparison preview or failed candidate. Ordinary retry does not widen scope. The host rejects out-of-scope differences without splicing old text back. This is not scientific approval or proof that the requested edits are correct."

func designIsolationRequired(c *revisionWriteContract, ids []string) bool {
	owners := map[string]bool{}
	for _, o := range c.Objects {
		if o.Kind == "frozen_design_material" || o.Kind == "design_material_collection" {
			owners[o.OwnerNodeID] = true
		}
	}
	for _, o := range c.Objects {
		if owners[o.OwnerNodeID] && containsString(ids, o.ID) {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, s := range values {
		if s == value {
			return true
		}
	}
	return false
}

func writeBaselineError(err error) error {
	return &apperr.Error{Code: revisionWriteBaselineInvalid, UserMessage: "返修写入范围的可信历史绑定无效，已停止；不会让模型猜测或补写原稿。", Details: err.Error(), Cause: err}
}

func sameIsolationProposal(a, b ResearchRevisionProposal) bool {
	a.Status, b.Status, a.CanConfirm, b.CanConfirm = "pending", "pending", false, false
	a.ConfirmationBlockedReason, b.ConfirmationBlockedReason = "", ""
	x, e1 := canonicalJSON(a)
	y, e2 := canonicalJSON(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// Resolve authority from immutable host records, never model-visible input or
// a failed candidate. A later explicit review revision replaces the old scope;
// a plain retry or automatic repair does not silently widen it.
func currentDesignIsolationProposal(d RunDetail, step Step) (*ResearchRevisionProposal, error) {
	var stored *ResearchRevisionProposal
	for i := len(d.RevisionProposals) - 1; i >= 0; i-- {
		p := d.RevisionProposals[i]
		start := findStepByNode(d.Steps, p.NodeID)
		if p.Status == "confirmed" && p.DesignWriteIsolationVersion != "" && start == nil {
			return nil, fmt.Errorf("已确认写隔离方案的起点不存在")
		}
		if p.Status == "confirmed" && start != nil && start.Ordinal <= step.Ordinal {
			stored = &p
			break
		}
	}
	var offered *ResearchRevisionProposal
	index := -1
	for i := len(d.Events) - 1; i >= 0; i-- {
		if d.Events[i].Type != "workflow.user_revision_queued" {
			continue
		}
		var event struct {
			Proposal ResearchRevisionProposal `json:"proposal"`
			Start    *int                     `json:"startOrdinal"`
		}
		if json.Unmarshal(d.Events[i].Payload, &event) != nil || event.Start == nil {
			if event.Proposal.DesignWriteIsolationVersion != "" || stored != nil && stored.DesignWriteIsolationVersion != "" {
				return nil, fmt.Errorf("已确认返修的事件无法还原")
			}
			continue
		}
		if *event.Start <= step.Ordinal {
			start := findStepByNode(d.Steps, event.Proposal.NodeID)
			if event.Proposal.DesignWriteIsolationVersion != "" && (d.Events[i].WorkflowRunID != d.Run.ID || start == nil || start.Ordinal != *event.Start) {
				return nil, fmt.Errorf("写隔离事件的起点或任务绑定不一致")
			}
			offered, index = &event.Proposal, i
			break
		}
	}
	if (stored == nil || stored.DesignWriteIsolationVersion == "") && (offered == nil || offered.DesignWriteIsolationVersion == "") {
		return nil, nil // Old confirmed generations are not retroactively upgraded.
	}
	if stored == nil || offered == nil || stored.DesignWriteIsolationVersion != designWriteIsolationVersion || !sameIsolationProposal(*stored, *offered) || stored.RunID != d.Run.ID {
		return nil, fmt.Errorf("写隔离授权缺少一致的已确认提案与原事件")
	}
	for _, event := range d.Events[index+1:] {
		if event.Type != "workflow.review_revision_queued" {
			continue
		}
		var revision reviewRevision
		if json.Unmarshal(event.Payload, &revision) != nil {
			return nil, fmt.Errorf("后续审查返修事件无法还原")
		}
		producer := findStepByNode(d.Steps, revision.ProducerNodeID)
		if producer == nil || producer.ID != revision.ProducerStepID {
			return nil, fmt.Errorf("后续审查返修起点无效")
		}
		if producer.Ordinal <= step.Ordinal {
			var c revisionWriteContract
			if json.Unmarshal(stored.WriteContract, &c) != nil {
				return nil, fmt.Errorf("原写隔离契约无法读取")
			}
			for _, o := range c.Objects {
				if o.OwnerNodeID == step.NodeID && containsString(stored.EditTargets, o.ID) {
					return nil, fmt.Errorf("后续审查返修缺少有效写隔离回执；不能把窄材料授权降级为整个阶段修改，请在同一任务重新制定方案")
				}
			}
			return nil, nil
		}
	}
	return stored, nil
}

type designWriteMask struct {
	baseline   json.RawMessage
	markdowns  map[int]bool
	appendOnly bool
	wholeStage bool
}

func (s *RuntimeService) designWriteMask(ctx context.Context, d RunDetail, step Step) (*designWriteMask, error) {
	p, raw, handled, err := s.designReviewWriteAuthority(ctx, d, step)
	if !handled && err == nil {
		p, err = currentDesignIsolationProposal(d, step)
	}
	if err != nil || p == nil {
		return nil, err
	}
	if len(p.WriteContract) == 0 || hashJSON(p.WriteContract) != p.WriteContractSHA256 || p.WriteContractVersion != revisionWriteVersion {
		return nil, fmt.Errorf("写隔离对象契约字节摘要不一致")
	}
	var contract revisionWriteContract
	if json.Unmarshal(p.WriteContract, &contract) != nil || contract.Version != revisionWriteVersion {
		return nil, fmt.Errorf("写隔离对象契约无法读取")
	}
	objects := map[string]revisionWriteObject{}
	materialOwner := false
	for _, o := range contract.Objects {
		if o.ID == "" || objects[o.ID].ID != "" {
			return nil, fmt.Errorf("写隔离对象身份重复或为空")
		}
		objects[o.ID] = o
		if o.OwnerNodeID == step.NodeID && (o.Kind == "frozen_design_material" || o.Kind == "design_material_collection") {
			materialOwner = true
		}
	}
	selected := []revisionWriteObject{}
	seen := map[string]bool{}
	for _, id := range p.EditTargets {
		o, ok := objects[id]
		if !ok || seen[id] {
			return nil, fmt.Errorf("写隔离对象选择未知或重复")
		}
		seen[id] = true
		if o.OwnerNodeID == step.NodeID && materialOwner {
			selected = append(selected, o)
		}
	}
	if len(selected) == 0 {
		return nil, nil // Downstream regeneration is not a material-owner write.
	}
	if !handled {
		reader, ok := s.repository.(interface {
			ReadResearchRevisionSnapshot(context.Context, string, string) (json.RawMessage, error)
		})
		if !ok {
			return nil, fmt.Errorf("无法读取已确认返修的完整历史快照")
		}
		raw, err = reader.ReadResearchRevisionSnapshot(ctx, d.Run.ID, p.ID)
		if err != nil {
			return nil, err
		}
	}
	var before RunDetail
	if json.Unmarshal(raw, &before) != nil || before.Run.ID != d.Run.ID || ResearchRevisionSnapshot(before) != p.SnapshotSHA256 || before.Run.CompilationSHA256 != d.Run.CompilationSHA256 {
		return nil, fmt.Errorf("返修完整历史快照的任务或摘要不一致")
	}
	owner := findStepByNode(before.Steps, step.NodeID)
	if owner == nil || owner.Status != StepCompleted || owner.ID != step.ID || owner.Ordinal != step.Ordinal || owner.Attempt >= step.Attempt {
		return nil, fmt.Errorf("返修材料的历史产出阶段或尝试不一致")
	}
	baseline, err := outputPort(owner.Output, "analysis")
	if err != nil {
		return nil, err
	}
	mask := &designWriteMask{baseline: baseline, markdowns: map[int]bool{}}
	for _, o := range selected {
		identity := o
		identity.ID, identity.ReviewRoots, identity.ReportCopyPolicy = "", nil, ""
		if o.ID != "edit-"+hashJSON(mustJSON(identity)) || o.StepID != owner.ID || o.Attempt != owner.Attempt || o.Ordinal != owner.Ordinal || o.OutputSHA256 != hashJSON(owner.Output) {
			return nil, fmt.Errorf("返修写对象未绑定完整历史产出")
		}
		switch o.Kind {
		case "stage_result":
			if o.OutputPointer != "/analysis" {
				return nil, fmt.Errorf("整个阶段写入指针不一致")
			}
			mask.wholeStage = true
		case "design_material_collection":
			if o.OutputPointer != "/analysis/deliverables" {
				return nil, fmt.Errorf("材料新增指针不一致")
			}
			mask.appendOnly = true
		case "frozen_design_material":
			parts := strings.Split(o.OutputPointer, "/")
			if len(parts) != 5 || parts[1] != "analysis" || parts[2] != "deliverables" || parts[4] != "markdown" {
				return nil, fmt.Errorf("材料写入指针无效")
			}
			i, err := strconv.Atoi(parts[3])
			if err != nil || i < 0 || strconv.Itoa(i) != parts[3] {
				return nil, fmt.Errorf("材料写入索引无效")
			}
			mask.markdowns[i] = true
		default:
			return nil, fmt.Errorf("设计材料不支持该类写对象")
		}
	}
	return mask, nil
}

func decodeIsolationValue(raw json.RawMessage) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("写隔离 JSON 无效")
	}
	if err := rejectIsolationDuplicateKeys(raw); err != nil {
		return nil, err
	}
	var v map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&v); err != nil || v == nil {
		return nil, fmt.Errorf("写隔离要求完整 JSON 对象")
	}
	return v, nil
}

// JSON consumers disagree about first/last duplicate keys. A frozen field
// cannot be proved unchanged if its submitted representation is ambiguous.
func rejectIsolationDuplicateKeys(raw json.RawMessage) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func() error
	read = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return fmt.Errorf("写隔离 JSON 含重复字段，无法证明原稿未变")
				}
				seen[k] = true
				if err := read(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := read(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	return read()
}

func isolationScopeError(paths []string) error {
	return fmt.Errorf("返修实际修改了未确认的设计字段：%s。只修改已选择材料的 markdown，保留其余原始字段、材料标题和顺序；不得用错误候选替代原稿基线。", strings.Join(paths, ", "))
}

func (m *designWriteMask) validate(candidate json.RawMessage) error {
	before, err := decodeIsolationValue(m.baseline)
	if err != nil {
		return writeBaselineError(err)
	}
	after, err := decodeIsolationValue(candidate)
	if err != nil {
		return isolationScopeError([]string{"/"})
	}
	if m.wholeStage {
		return nil
	} // Explicit broad authorization, not inferred intent.
	oldValue, oldExists := before["deliverables"]
	old, oldArray := oldValue.([]any)
	if oldExists && !oldArray {
		return writeBaselineError(fmt.Errorf("历史材料集合不是数组，不能将 null 或其他值当作空集合"))
	}
	if !oldExists {
		old = []any{}
	}
	newValue, newExists := after["deliverables"]
	current, newArray := newValue.([]any)
	unchangedOmission := !oldExists && !newExists
	if unchangedOmission {
		current = []any{}
	}
	if !unchangedOmission && (!newArray || (!oldExists && !m.appendOnly) || len(current) < len(old) || !m.appendOnly && len(current) != len(old)) {
		return isolationScopeError([]string{"/deliverables"})
	}
	for i := range m.markdowns {
		if i >= len(old) {
			return writeBaselineError(fmt.Errorf("所选材料不在历史集合中"))
		}
		a, ok1 := old[i].(map[string]any)
		b, ok2 := current[i].(map[string]any)
		if !ok1 {
			return writeBaselineError(fmt.Errorf("所选历史材料不是对象"))
		}
		if !ok2 {
			return isolationScopeError([]string{fmt.Sprintf("/deliverables/%d", i)})
		}
		if _, ok := a["markdown"].(string); !ok {
			return writeBaselineError(fmt.Errorf("所选历史材料正文不是字符串"))
		}
		if _, ok := b["markdown"].(string); !ok {
			return isolationScopeError([]string{fmt.Sprintf("/deliverables/%d/markdown", i)})
		}
		delete(a, "markdown")
		delete(b, "markdown")
	}
	before["deliverables"], after["deliverables"] = old, current[:len(old)]
	paths := []string{}
	var walk func(any, any, string)
	walk = func(a, b any, path string) {
		if len(paths) >= 8 || reflect.DeepEqual(a, b) {
			return
		}
		x, mx := a.(map[string]any)
		y, my := b.(map[string]any)
		if mx && my {
			keys := map[string]bool{}
			for k := range x {
				keys[k] = true
			}
			for k := range y {
				keys[k] = true
			}
			order := make([]string, 0, len(keys))
			for k := range keys {
				order = append(order, k)
			}
			sort.Strings(order)
			for _, k := range order {
				av, ae := x[k]
				bv, be := y[k]
				p := path + "/" + pointerSegment(k)
				if ae != be {
					paths = append(paths, boundedWorkflowText(p, 180))
				} else {
					walk(av, bv, p)
				}
				if len(paths) >= 8 {
					return
				}
			}
			return
		}
		xa, ax := a.([]any)
		ya, ay := b.([]any)
		if ax && ay && len(xa) == len(ya) {
			for i := range xa {
				walk(xa[i], ya[i], path+"/"+strconv.Itoa(i))
			}
			return
		}
		paths = append(paths, boundedWorkflowText(path, 180))
	}
	walk(before, after, "")
	if len(paths) > 0 {
		return isolationScopeError(paths)
	}
	return nil
}

func (s *RuntimeService) validateDesignRevisionWrites(ctx context.Context, d RunDetail, step Step, candidate json.RawMessage) error {
	mask, err := s.designWriteMask(ctx, d, step)
	if err != nil {
		return writeBaselineError(err)
	}
	if mask == nil {
		return nil
	}
	return mask.validate(candidate)
}

func (s *RuntimeService) failDesignRevisionWrite(ctx context.Context, d RunDetail, step Step, execution AIExecution, finish bool, err error) error {
	code := "WORKFLOW_REVISION_WRITE_SCOPE_INVALID"
	var classified *apperr.Error
	if errors.As(err, &classified) && classified.Code == revisionWriteBaselineInvalid {
		code = revisionWriteBaselineInvalid
	}
	if finish {
		execution.Status, execution.ErrorCode, execution.ErrorMessage = "failed", code, err.Error()
		execution.Output, execution.OutputSHA256 = raw(`{}`), ""
		execution.UpdatedAt = s.now()
		completed := execution.UpdatedAt
		execution.CompletedAt = &completed
		if persistErr := s.repository.FinishAIExecution(context.Background(), execution); persistErr != nil {
			return persistErr
		}
	}
	return s.failDrive(ctx, d, step, code, err.Error(), StepFailed, RunFailed)
}
