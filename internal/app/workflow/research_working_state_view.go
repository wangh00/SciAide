package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Only host metadata is pinned to the system context. Working-note contents
// are untrusted data and remain in the existing task store/read tool.
func ResearchWorkingStateMetadata(note *ResearchWorkingState) json.RawMessage {
	value := map[string]any{"version": 0, "status": "not_recorded"}
	if note != nil {
		value = map[string]any{"version": note.Version, "status": "working_note", "contentOmitted": true, "reader": ResearchStateReadTool}
	}
	raw, _ := json.Marshal(value)
	return raw
}

type ResearchWorkingStateReadCommand struct {
	ExpectedVersion int    `json:"expectedVersion"`
	Field           string `json:"field"`
	Offset          int    `json:"offset,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

const ResearchWorkingStateReadSchema = `{"type":"object","additionalProperties":false,"required":["expectedVersion","field"],"properties":{"expectedVersion":{"type":"integer","minimum":1},"field":{"type":"string","enum":["currentFocus","rationale","nextStep","unknowns","provenance"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":400}}}`

func ParseResearchWorkingStateRead(raw json.RawMessage) (ResearchWorkingStateReadCommand, error) {
	var command ResearchWorkingStateReadCommand
	if err := rejectIsolationDuplicateKeys(raw); err != nil {
		return command, fmt.Errorf("研究工作记录读取参数包含重复字段")
	}
	if err := (tool.JSONSchemaValidator{}).Validate([]byte(ResearchWorkingStateReadSchema), raw); err != nil {
		return command, err
	}
	err := json.Unmarshal(raw, &command)
	return command, err
}

type researchWorkingStateReadValidator interface {
	ValidateResearchWorkingStateRead(context.Context, string, string, string, string) error
}

func (s *RuntimeService) ReadResearchWorkingState(ctx context.Context, projectID, taskID, chatRunID, callID string, c ResearchWorkingStateReadCommand) (json.RawMessage, error) {
	validator, ok := s.repository.(researchWorkingStateReadValidator)
	if !ok {
		return nil, fmt.Errorf("研究工作记录读取未配置")
	}
	if err := validator.ValidateResearchWorkingStateRead(ctx, projectID, taskID, chatRunID, callID); err != nil {
		return nil, err
	}
	if c.Field == "provenance" {
		value, err := s.GetResearchWorkingStateProvenance(ctx, projectID, taskID, c.ExpectedVersion)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return researchWorkingStateFieldPage(value.Version, string(raw), c)
	}
	note, err := s.GetResearchWorkingState(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if note == nil || note.Version != c.ExpectedVersion {
		return nil, fmt.Errorf("研究工作记录版本已变化，请使用当前版本读取")
	}
	text := ""
	switch c.Field {
	case "currentFocus":
		text = note.CurrentFocus
	case "rationale":
		text = note.Rationale
	case "nextStep":
		text = note.NextStep
	case "unknowns":
		raw, _ := json.Marshal(note.Unknowns)
		text = string(raw)
	default:
		return nil, fmt.Errorf("未知研究工作记录字段")
	}
	return researchWorkingStateFieldPage(note.Version, text, c)
}

func researchWorkingStateFieldPage(version int, text string, c ResearchWorkingStateReadCommand) (json.RawMessage, error) {
	runes := []rune(text)
	if c.Offset < 0 || c.Offset > len(runes) || c.Limit < 0 || c.Limit > 400 {
		return nil, fmt.Errorf("研究工作记录读取范围无效")
	}
	if c.Limit == 0 {
		c.Limit = 240
	}
	end := min(len(runes), c.Offset+c.Limit)
	next := -1
	if end < len(runes) {
		next = end
	}
	hash := sha256.Sum256([]byte(text))
	return json.Marshal(map[string]any{"version": version, "status": "working_note", "field": c.Field, "sha256": hex.EncodeToString(hash[:]), "offset": c.Offset, "nextOffset": next, "totalRunes": len(runes), "content": string(runes[c.Offset:end])})
}
