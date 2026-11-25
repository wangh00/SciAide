package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// An explicit model-selected relation to a saved abstract page, not a trusted
// citation or host judgement that the page supports the working-note content.
type ResearchWorkingBasisSource struct {
	ReadCallID  string `json:"readCallId"`
	Status      string `json:"status"`
	QueryID     string `json:"queryId,omitempty"`
	CandidateID string `json:"candidateId,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Offset      int    `json:"offset"`
	EndOffset   int    `json:"endOffset"`
	TotalRunes  int    `json:"totalRunes"`
}

type ResearchWorkingBasisRead struct {
	ProjectID      string                     `json:"projectId"`
	ResearchTaskID string                     `json:"researchTaskId"`
	StateID        string                     `json:"stateId"`
	Version        int                        `json:"version"`
	Field          string                     `json:"field"`
	Basis          ResearchWorkingBasisSource `json:"basis"`
	Content        string                     `json:"content"`
}

func normalizeResearchWorkingBasis(c ResearchWorkingStateUpdateCommand) (ResearchWorkingStateUpdateCommand, error) {
	if c.Basis == nil {
		return c, nil
	}
	if len(c.Basis) == 0 || len(c.Basis) > 4 {
		return c, fmt.Errorf("摘要页关联需要明确的变化字段")
	}
	copyBasis := map[string][]string{}
	total := 0
	for field, ids := range c.Basis {
		known := false
		for _, name := range researchStateFields {
			known = known || field == name
		}
		if !known || !c.HasField(field) || len(ids) < 1 || len(ids) > 2 {
			return c, fmt.Errorf("每个显式变化字段最多关联两条已成功读取的摘要页；不能给省略字段另换依据")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || id != strings.TrimSpace(id) || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 100 || seen[id] {
				return c, fmt.Errorf("摘要页读取回执身份无效或重复")
			}
			seen[id] = true
		}
		total += len(ids)
		copyBasis[field] = append([]string{}, ids...)
	}
	if total > 4 {
		return c, fmt.Errorf("单个工作记录补丁最多关联四条摘要页读取回执")
	}
	c.Basis = copyBasis
	return c, nil
}

// This is the original host-generated discovery field page. The repository
// separately verifies task ownership, call ordering and the frozen snapshot.
type ResearchWorkingAbstractPage struct {
	Status       string `json:"status"`
	SnapshotView string `json:"snapshotView"`
	QueryID      string `json:"queryId"`
	CandidateID  string `json:"candidateId"`
	Field        string `json:"field"`
	SHA256       string `json:"sha256"`
	Available    bool   `json:"available"`
	Offset       int    `json:"offset"`
	NextOffset   int    `json:"nextOffset"`
	TotalRunes   int    `json:"totalRunes"`
	Content      string `json:"content"`
}

func ParseResearchWorkingAbstractPage(raw json.RawMessage) (ResearchWorkingAbstractPage, error) {
	var p ResearchWorkingAbstractPage
	if len(raw) > 2048 || rejectIsolationDuplicateKeys(raw) != nil || json.Unmarshal(raw, &p) != nil {
		return p, fmt.Errorf("摘要页读取回执无效")
	}
	n := utf8.RuneCountInString(p.Content)
	end := p.Offset + n
	if p.Status != "unverified_discovery_field" || p.SnapshotView != "preferred_record" || p.Field != "abstract" || !p.Available || p.QueryID == "" || p.CandidateID == "" || n < 1 || n > 400 || p.Offset < 0 || p.TotalRunes < p.Offset || n > p.TotalRunes-p.Offset ||
		(end == p.TotalRunes && p.NextOffset != -1) || (end < p.TotalRunes && p.NextOffset != end) {
		return p, fmt.Errorf("只能关联实际成功读取的非空摘要页，目录、题名和未读内容不是摘要依据")
	}
	return p, nil
}

// Parse the exact host-generated model snapshot, not a UI result or a text
// substring that happens to resemble a page. Reused by live reads and restore.
func ParseResearchWorkingAbstractReceipt(raw json.RawMessage) (string, ResearchWorkingAbstractPage, error) {
	var envelope struct {
		Status     string          `json:"status"`
		ToolCallID string          `json:"toolCallId"`
		Truncated  bool            `json:"truncated"`
		Structured json.RawMessage `json:"structured"`
	}
	if rejectIsolationDuplicateKeys(raw) != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Status != "success" || envelope.ToolCallID == "" || envelope.Truncated {
		return "", ResearchWorkingAbstractPage{}, fmt.Errorf("原模型回执缺失、歧义或截断，不推断已提供摘要内容")
	}
	p, err := ParseResearchWorkingAbstractPage(envelope.Structured)
	return envelope.ToolCallID, p, err
}

func ValidateResearchWorkingAbstractSelector(raw json.RawMessage, p ResearchWorkingAbstractPage) error {
	var c struct {
		QueryID     string `json:"queryId"`
		CandidateID string `json:"candidateId"`
		Field       string `json:"field"`
		Offset      int    `json:"offset"`
		Limit       int    `json:"limit"`
	}
	var keys map[string]json.RawMessage
	if rejectIsolationDuplicateKeys(raw) != nil || json.Unmarshal(raw, &c) != nil || json.Unmarshal(raw, &keys) != nil {
		return fmt.Errorf("摘要页读取参数无效")
	}
	for k := range keys {
		if k != "queryId" && k != "candidateId" && k != "field" && k != "offset" && k != "limit" {
			return fmt.Errorf("摘要页读取参数超出原约定")
		}
	}
	if c.Field == "" {
		c.Field = "abstract"
	}
	if c.Limit == 0 {
		c.Limit = 128
	}
	if c.QueryID != p.QueryID || c.CandidateID != p.CandidateID || c.Field != "abstract" || c.Offset != p.Offset || c.Limit < 1 || c.Limit > 400 || utf8.RuneCountInString(p.Content) > c.Limit {
		return fmt.Errorf("摘要页与原读取选择不一致")
	}
	return nil
}

type researchWorkingBasisReader interface {
	GetResearchWorkingStateBasis(context.Context, string, string, int, string, string) (ResearchWorkingBasisRead, error)
}

func (s *RuntimeService) GetResearchWorkingStateBasis(ctx context.Context, projectID, taskID string, version int, field, readCallID string) (ResearchWorkingBasisRead, error) {
	reader, ok := s.repository.(researchWorkingBasisReader)
	if !ok {
		return ResearchWorkingBasisRead{}, fmt.Errorf("工作记录关联摘要页读取未配置")
	}
	return reader.GetResearchWorkingStateBasis(ctx, projectID, taskID, version, field, readCallID)
}
