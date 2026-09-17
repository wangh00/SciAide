package workflow

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const researchEmptyResultCode = "WORKFLOW_RESEARCH_RESULT_EMPTY"

func hasResearchAcceptance(compilation Compilation) bool {
	for _, node := range compilation.Nodes {
		if isResearchAcceptanceSchema(node.OutputSchema) {
			return true
		}
	}
	return false
}

// A successful transport envelope is insufficient for a research computation.
// New routes opt into this check through their frozen acceptance schema; old
// compiled workflows retain their original execution contract.
func validateResearchToolResult(detail RunDetail, step Step, call tool.Call) (string, error) {
	if !hasResearchAcceptance(detail.Run.Compilation) || step.NodeKind != NodePython {
		return "", nil
	}
	fail := func(message string) (string, error) {
		return "WORKFLOW_RESEARCH_RESULT_INVALID", fmt.Errorf("科研计算结果校验失败：%s", message)
	}
	if call.Result == nil || call.Result.Status != tool.ResultSuccess {
		return fail("工具没有返回成功结果")
	}
	var result struct {
		Status       string            `json:"status"`
		Value        json.RawMessage   `json:"value"`
		CodeHash     string            `json:"codeSha256"`
		Inputs       map[string]string `json:"inputSha256"`
		Outputs      map[string]string `json:"outputSha256"`
		Environment  string            `json:"environmentFingerprint"`
		Reproduction string            `json:"reproductionSha256"`
	}
	if json.Unmarshal(call.Result.Structured, &result) != nil || result.Status != "success" {
		return fail("Python 实际状态不是 success，不能按工具调用成功推进")
	}
	var args struct {
		Code        string   `json:"code"`
		Inputs      []string `json:"inputPaths"`
		Outputs     []string `json:"outputPaths"`
		Environment string   `json:"expectedEnvironmentFingerprint"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return fail("冻结工具参数无效")
	}
	if result.CodeHash != hashBytes([]byte(strings.TrimSpace(args.Code))) || cleanReviewHash(result.Environment) == "" || cleanReviewHash(result.Reproduction) == "" {
		return fail("代码、环境或复现摘要缺失或不一致")
	}
	if args.Environment != "" && args.Environment != result.Environment {
		return fail("执行环境与冻结环境不一致")
	}
	for _, path := range args.Inputs {
		hash := result.Inputs[filepath.ToSlash(path)]
		if cleanReviewHash(hash) == "" {
			return fail("缺少声明输入的数据摘要")
		}
		if expected, frozen := FrozenInputSHA256(path); frozen && expected != hash {
			return fail("实际输入与冻结文件摘要不一致")
		}
	}
	artifacts := make(map[string]tool.ArtifactRef, len(call.Result.Artifacts))
	for _, artifact := range call.Result.Artifacts {
		artifacts[filepath.ToSlash(artifact.WorkspacePath)] = artifact
	}
	for _, path := range args.Outputs {
		path = strings.NewReplacer("{{runId}}", detail.Run.ID, "{{attempt}}", strconv.Itoa(step.Attempt)).Replace(filepath.ToSlash(path))
		hash := result.Outputs[path]
		artifact, exists := artifacts[path]
		if cleanReviewHash(hash) == "" || !exists || artifact.SHA256 != hash || artifact.SizeBytes <= 0 {
			return fail("声明的输出文件为空、缺失或未形成一致的产物快照")
		}
	}
	var value map[string]json.RawMessage
	if len(result.Value) == 0 || strings.TrimSpace(string(result.Value)) == "null" {
		return researchEmptyResultCode, fmt.Errorf("Python 已完成且声明产物已核验，但未向工作流返回结构化结果。请在代码末尾使用字典表达式（例如 summary），或明确以 result = summary 结束；保存 JSON 文件或打印结果不等于返回结果")
	}
	if json.Unmarshal(result.Value, &value) != nil || len(value) == 0 {
		return researchEmptyResultCode, fmt.Errorf("Python 返回的结果不是非空字典；请返回包含实际分析内容的 dict，不能返回空字典、字符串或列表")
	}
	return "", nil
}
