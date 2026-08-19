package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const ReadSkillResourceName = "builtin.skill.resource.read_text"

type SkillResourceLoader interface {
	ReadRunResource(ctx context.Context, runID, skillID, resourcePath string, maxBytes int) (skill.ResourceContent, error)
}

type ReadSkillResource struct{ skills SkillResourceLoader }

func NewReadSkillResource(skills SkillResourceLoader) *ReadSkillResource {
	return &ReadSkillResource{skills: skills}
}

func (*ReadSkillResource) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ReadSkillResourceName,
		Description:   "按需读取本次 Run 已选中 Skill 包内由 SKILL.md 引用的 UTF-8 文本资源。只能使用 Skill ID 和包内相对路径，不能读取未选中的 Skill 或任意主机路径。",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["skillId","path"],"properties":{"skillId":{"type":"string","minLength":1,"maxLength":64},"path":{"type":"string","minLength":1,"maxLength":4096},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["skillId","path","bytesRead","originalBytes","truncated"],"properties":{"skillId":{"type":"string"},"path":{"type":"string"},"bytesRead":{"type":"integer","minimum":0},"originalBytes":{"type":"integer","minimum":0},"truncated":{"type":"boolean"}}}`),
		Risk:          tool.RiskLow,
		Permissions:   []tool.PermissionRequirement{},
		Idempotent:    true,
		Version:       "1",
	}, nil
}

func (t *ReadSkillResource) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.skills == nil {
		return tool.Result{}, fmt.Errorf("Skill resource loader is not configured")
	}
	var args struct {
		SkillID  string `json:"skillId"`
		Path     string `json:"path"`
		MaxBytes int    `json:"maxBytes"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, err := t.skills.ReadRunResource(ctx, invocation.RunID, args.SkillID, args.Path, args.MaxBytes)
	if err != nil {
		return tool.Result{}, err
	}
	payload := struct {
		SkillID      string `json:"skillId"`
		Path         string `json:"path"`
		BytesRead    int    `json:"bytesRead"`
		OriginalSize int64  `json:"originalBytes"`
		Truncated    bool   `json:"truncated"`
	}{SkillID: args.SkillID, Path: value.Path, BytesRead: len(value.Content), OriginalSize: value.OriginalBytes, Truncated: value.Truncated}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: string(value.Content), Structured: structured, Truncated: value.Truncated, Meta: tool.ResultMeta{OriginalBytes: value.OriginalBytes}}, nil
}
