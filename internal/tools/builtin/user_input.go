package builtin

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
)

type UserInputRequest struct{}

func NewUserInputRequest() *UserInputRequest { return &UserInputRequest{} }
func (*UserInputRequest) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: userinput.ToolName, Version: "1", Risk: tool.RiskLow, Idempotent: true, Description: "Ask the user a necessary question and wait for their explicit answer in the current conversation, before or after delivery. Prefer 1–3 short questions, with useful options and brief tradeoffs, or omit options for free-form input. Do not include an Other option: the UI always allows free text. Answers are not execution authorization. Do not repeat answered questions or use for routine progress. If you say you need to confirm something, call this tool now with the actual questions; an unfinished prose introduction does not create a card.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["questions"],"properties":{"questions":{"type":"array","minItems":1,"maxItems":12,"items":{"type":"object","additionalProperties":false,"required":["id","question","required"],"properties":{"id":{"type":"string","minLength":1,"maxLength":128},"header":{"type":"string","maxLength":80},"question":{"type":"string","minLength":1,"maxLength":4000},"selectionMode":{"type":"string","enum":["single","multiple"]},"required":{"type":"boolean"},"options":{"type":"array","maxItems":12,"items":{"type":"object","additionalProperties":false,"required":["id","label"],"properties":{"id":{"type":"string","minLength":1,"maxLength":128},"label":{"type":"string","minLength":1,"maxLength":2000},"description":{"type":"string","maxLength":2000}}}}}}}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}, nil
}

// The Agent intercepts this control tool before permission/executor processing.
func (*UserInputRequest) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, tool.NewUserFacingError("此问题须由当前对话中的用户回答，不能由执行器代答")
}
