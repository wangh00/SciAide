package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const ResearchStateUpdateTool = "builtin.research.state.update"
const ResearchStateReadTool = "builtin.research.state.read"
const MaxResearchStateUpdateBytes = 2048

// The model supplies working notes only. All task and source identities come
// from the actual persisted Chat Run and ToolCall.
// The dictionary's keys, patch presence mask and link bounds are validated
// together by normalizeResearchWorkingBasis, not four repeated wire schemas.
const ResearchWorkingStateUpdateSchema = `{"type":"object","additionalProperties":false,"required":["expectedVersion"],"properties":{"expectedVersion":{"type":"integer","minimum":0},"currentFocus":{"type":"string","minLength":1,"maxLength":1000},"rationale":{"type":"string","minLength":1,"maxLength":1000},"nextStep":{"type":"string","maxLength":1000},"unknowns":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1,"maxLength":400}},"basis":{"type":"object","minProperties":1}}}`

type ResearchWorkingState struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"projectId"`
	ResearchTaskID      string    `json:"researchTaskId"`
	Version             int       `json:"version"`
	CurrentFocus        string    `json:"currentFocus"`
	Rationale           string    `json:"rationale"`
	NextStep            string    `json:"nextStep"`
	Unknowns            []string  `json:"unknowns"`
	SourceChatRunID     string    `json:"sourceChatRunId"`
	SourceUserMessageID string    `json:"sourceUserMessageId"`
	SourceCallID        string    `json:"sourceCallId"`
	CreatedAt           time.Time `json:"createdAt"`
	Status              string    `json:"status"`
}

type ResearchWorkingStateUpdateCommand struct {
	ExpectedVersion int                 `json:"expectedVersion"`
	CurrentFocus    string              `json:"currentFocus"`
	Rationale       string              `json:"rationale"`
	NextStep        string              `json:"nextStep"`
	Unknowns        []string            `json:"unknowns"`
	Basis           map[string][]string `json:"basis,omitempty"`
	// Host-only presence mask. Omitted fields are preserved, never reset.
	Fields []string `json:"-"`
}

var researchStateFields = []string{"currentFocus", "rationale", "nextStep", "unknowns"}

func (c ResearchWorkingStateUpdateCommand) HasField(name string) bool {
	if c.Fields == nil {
		return true
	} // Trusted Go callers supply full values.
	for _, field := range c.Fields {
		if field == name {
			return true
		}
	}
	return false
}

func (c ResearchWorkingStateUpdateCommand) MarshalJSON() ([]byte, error) {
	value := map[string]any{"expectedVersion": c.ExpectedVersion}
	if c.HasField("currentFocus") {
		value["currentFocus"] = c.CurrentFocus
	}
	if c.HasField("rationale") {
		value["rationale"] = c.Rationale
	}
	if c.HasField("nextStep") {
		value["nextStep"] = c.NextStep
	}
	if c.HasField("unknowns") {
		value["unknowns"] = c.Unknowns
	}
	if c.Basis != nil {
		value["basis"] = c.Basis
	}
	return json.Marshal(value)
}

// Parse also rejects duplicate keys: treating the first and last model values
// differently must never silently alter a persisted working note.
func ParseResearchWorkingStateUpdate(raw json.RawMessage) (ResearchWorkingStateUpdateCommand, error) {
	var command ResearchWorkingStateUpdateCommand
	if len(raw) > MaxResearchStateUpdateBytes {
		return command, fmt.Errorf("单次研究工作记录补丁最多 %d 字节；仅提交变化字段，长内容保留在研究材料或产物中", MaxResearchStateUpdateBytes)
	}
	if err := rejectIsolationDuplicateKeys(raw); err != nil {
		return command, fmt.Errorf("研究工作记录参数包含重复字段")
	}
	if err := (tool.JSONSchemaValidator{}).Validate([]byte(ResearchWorkingStateUpdateSchema), raw); err != nil {
		return command, fmt.Errorf("研究工作记录参数不符合约定: %w", err)
	}
	if err := json.Unmarshal(raw, &command); err != nil {
		return command, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	command.Fields = []string{}
	for _, name := range researchStateFields {
		if _, ok := fields[name]; ok {
			command.Fields = append(command.Fields, name)
		}
	}
	return NormalizeResearchWorkingStateUpdate(command)
}

func NormalizeResearchWorkingStateUpdate(command ResearchWorkingStateUpdateCommand) (ResearchWorkingStateUpdateCommand, error) {
	command.CurrentFocus = strings.TrimSpace(command.CurrentFocus)
	command.Rationale = strings.TrimSpace(command.Rationale)
	command.NextStep = strings.TrimSpace(command.NextStep)
	valid := func(value string, min, max int) bool {
		return utf8.ValidString(value) && utf8.RuneCountInString(value) >= min && utf8.RuneCountInString(value) <= max
	}
	if command.Fields == nil {
		command.Fields = append([]string{}, researchStateFields...)
	}
	if len(command.Fields) == 0 {
		return command, fmt.Errorf("研究工作记录补丁没有变化字段")
	}
	for _, field := range command.Fields {
		known := false
		for _, name := range researchStateFields {
			known = known || field == name
		}
		if !known {
			return command, fmt.Errorf("未知的研究工作记录字段")
		}
	}
	if command.ExpectedVersion < 0 || command.ExpectedVersion == math.MaxInt || command.HasField("currentFocus") && !valid(command.CurrentFocus, 1, 1000) || command.HasField("rationale") && !valid(command.Rationale, 1, 1000) || command.HasField("nextStep") && !valid(command.NextStep, 0, 1000) || len(command.Unknowns) > 8 {
		return command, fmt.Errorf("研究工作记录内容或版本无效")
	}
	if command.ExpectedVersion == 0 && (!command.HasField("currentFocus") || !command.HasField("rationale")) {
		return command, fmt.Errorf("首次记录需要当前关注及工作理由")
	}
	unknowns := make([]string, 0, len(command.Unknowns))
	for _, unknown := range command.Unknowns {
		unknown = strings.TrimSpace(unknown)
		if !valid(unknown, 1, 400) {
			return command, fmt.Errorf("研究工作记录中的未知项无效")
		}
		unknowns = append(unknowns, unknown)
	}
	command.Unknowns = unknowns
	command, err := normalizeResearchWorkingBasis(command)
	if err != nil {
		return command, err
	}
	encoded, err := json.Marshal(command)
	if err != nil || len(encoded) > MaxResearchStateUpdateBytes {
		return command, fmt.Errorf("单次研究工作记录补丁最多 %d 字节；仅提交变化字段", MaxResearchStateUpdateBytes)
	}
	return command, nil
}

// Merge only explicitly supplied fields, under the repository's version CAS.
// The complete record can grow within its field bounds without copying it
// through every model request or silently dropping unseen unknowns.
func ApplyResearchWorkingStateUpdate(base *ResearchWorkingState, c ResearchWorkingStateUpdateCommand) ResearchWorkingState {
	var value ResearchWorkingState
	if base != nil {
		value = *base
		value.Unknowns = append([]string{}, base.Unknowns...)
	}
	if c.HasField("currentFocus") {
		value.CurrentFocus = c.CurrentFocus
	}
	if c.HasField("rationale") {
		value.Rationale = c.Rationale
	}
	if c.HasField("nextStep") {
		value.NextStep = c.NextStep
	}
	if c.HasField("unknowns") {
		value.Unknowns = append([]string{}, c.Unknowns...)
	}
	if value.Unknowns == nil {
		value.Unknowns = []string{}
	}
	return value
}

type researchWorkingStateReader interface {
	GetResearchWorkingState(context.Context, string, string) (*ResearchWorkingState, error)
}

type researchWorkingStateWriter interface {
	SaveResearchWorkingState(context.Context, string, string, string, string, string, time.Time, ResearchWorkingStateUpdateCommand) (ResearchWorkingState, error)
}

// Unsupported old repository stubs have no working-state projection; this
// does not alter the behavior of existing frozen workflow executions.
func (s *RuntimeService) GetResearchWorkingState(ctx context.Context, projectID, taskID string) (*ResearchWorkingState, error) {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return nil, fmt.Errorf("project and research task are required")
	}
	reader, ok := s.repository.(researchWorkingStateReader)
	if !ok {
		return nil, nil
	}
	return reader.GetResearchWorkingState(ctx, projectID, taskID)
}

func (s *RuntimeService) UpdateResearchWorkingState(ctx context.Context, projectID, taskID, chatRunID, callID string, command ResearchWorkingStateUpdateCommand) (ResearchWorkingState, error) {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	chatRunID, callID = strings.TrimSpace(chatRunID), strings.TrimSpace(callID)
	if projectID == "" || taskID == "" || chatRunID == "" || callID == "" {
		return ResearchWorkingState{}, fmt.Errorf("需要当前科研任务和真实讨论工具调用")
	}
	command, err := NormalizeResearchWorkingStateUpdate(command)
	if err != nil {
		return ResearchWorkingState{}, err
	}
	writer, ok := s.repository.(researchWorkingStateWriter)
	if !ok {
		return ResearchWorkingState{}, fmt.Errorf("研究工作记录存储未配置")
	}
	stateID, err := s.newID()
	if err != nil {
		return ResearchWorkingState{}, err
	}
	return writer.SaveResearchWorkingState(ctx, projectID, taskID, chatRunID, callID, stateID, s.now(), command)
}
