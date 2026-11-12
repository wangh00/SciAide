package workflow

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/reviewcontract"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// The existing data-review version is 62 characters; one token plus '-' keeps
// its new frozen identity within the compiler's 64-character limit.
const proportionateDataDeliveryVersion = "p"
const computationRecordVersion = "computation-record-v1"
const computationAppendixHeading = "## 附录：计算执行记录"

func usesProportionateDataDelivery(node CompiledNode) bool {
	for _, token := range strings.Split(node.PromptVersion, "-") {
		if token == proportionateDataDeliveryVersion {
			return true
		}
	}
	return false
}

// Only new compiled data routes adopt this division of responsibility. Neither
// source length nor report length is used as a scientific acceptance criterion.
func applyProportionateDataDelivery(node *Node) {
	var instruction string
	switch node.ID {
	case "question_refinement", "method_selection":
		instruction = " 按本轮问题和交付形式确定最小充分实现，不把 Skill 的通用论文模板、所有可选诊断或内部审计项目扩成新的验收条件。保留全部已承诺的科学检查、产物及真实局限。代码/环境/输入输出哈希与执行时间由 Kernel 保存，报告的计算执行记录由程序附加；不要求分析脚本再实现一套运行器或在每个产物中重抄这些宿主记录。用户明确要求额外审计内容时仍须满足。"
	case "method_implementation":
		instruction = " 实现规模与当前冻结问题相称：只实现实际采用的分析和已承诺的数据质量处理，不为未知文件格式、任意字段或未来分析编写通用框架，不给已冻结必需参数添加猜测性默认值。Kernel 已核验声明路径、记录实际代码/环境及输入输出哈希和执行时间；除非研究约定另有要求，不在分析脚本中重复生成这套宿主审计。数据语义、必要字段、配对/分组、有效样本量、缺失/非法值及约定重复检查仍由实际分析正确处理；预检不是科学正确性的证明。方法 Markdown 简明说明实际算法、参数、数据处理和局限，不重复逐人表或整份结果 JSON。一次完成当前所需代码即可，没有最低行数或必须分多段的要求；不得为缩短代码省略科学检查、改变算法或覆盖原始数据。"
	case "report_drafting":
		instruction = " 交付形式以用户要求为准。简短说明应先给结论、关键汇总、必要方法和解释边界，不机械展开成论文或长综述。独立 JSON、逐人表和实际执行代码是独立交付物；已真实提供时可指明其用途和位置，不为证明交付再次抄录全部记录，除非用户明确要求正文包含它们。程序将从本次计算的冻结记录附加“计算执行记录”附录；不要抄写、修改或自行伪造该附录。正文仍须保留科学方法、必要统计口径、真实局限及所有实际承诺，不以简短为由省略或把未交付说成已交付。"
	case "independent_review":
		instruction = " 按用户要求的交付形式核查正文和独立产物。程序管理的计算执行记录不等于科学批准；仍核对实际代码、数字、质量规则与真实文件。约定的 JSON、逐人表或代码已作为独立产物交付时，不仅为展示而要求在短说明中重复全量内容；明确要求正文包含的内容不得省略。不能为了缩短篇幅撤销实质问题，也不能仅因没有通用论文模板或未要求的额外分析而否决。"
	default:
		return
	}
	node.Prompt += instruction
	if node.ID != "method_implementation" {
		node.PromptVersion += "-" + proportionateDataDeliveryVersion
	}
}

// This projection contains execution provenance only, never results interpreted
// as correct, approvals, user decisions, or a model-supplied provenance claim.
type computationRecord struct {
	Version                string             `json:"version"`
	SourceStepID           string             `json:"sourceStepId"`
	Attempt                int                `json:"attempt"`
	BindingError           string             `json:"bindingError,omitempty"`
	CodeSHA256             string             `json:"codeSha256"`
	EnvironmentFingerprint string             `json:"environmentFingerprint"`
	ReproductionSHA256     string             `json:"reproductionSha256"`
	FinishedAt             string             `json:"finishedAt"`
	InputSHA256            map[string]string  `json:"inputSha256"`
	Artifacts              []tool.ArtifactRef `json:"artifacts"`
}

func bindComputationRecord(d RunDetail, node CompiledNode, input json.RawMessage) *computationRecord {
	if !usesReportProvenance(node) || !usesProportionateDataDelivery(node) {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return nil
	}
	for _, edge := range d.Run.Compilation.Edges {
		if edge.ToNode != node.ID || edge.ToPort != "computedResults" || edge.FromPort != "structured" {
			continue
		}
		producer := compilationNodeMap(d.Run.Compilation)[edge.FromNode]
		if producer.Kind != NodePython {
			continue
		}
		record := &computationRecord{Version: computationRecordVersion}
		fail := func(reason string) *computationRecord { record.BindingError = reason; return record }
		step := findStepByNode(d.Steps, edge.FromNode)
		if step == nil || step.WorkflowRunID != d.Run.ID || step.Status != StepCompleted || step.Attempt < 1 || step.InputSHA256 == "" || hashJSON(step.Input) != step.InputSHA256 {
			return fail("前序计算阶段或冻结输入身份不一致")
		}
		record.SourceStepID, record.Attempt = step.ID, step.Attempt
		result, err := resolveWorkflowEdgeValue(d, edge)
		if err != nil || !reviewcontract.Equal(result, fields["computedResults"]) {
			return fail("computedResults 与本次计算输出不一致")
		}
		var kernel struct {
			Status                 string            `json:"status"`
			CodeSHA256             string            `json:"codeSha256"`
			EnvironmentFingerprint string            `json:"environmentFingerprint"`
			ReproductionSHA256     string            `json:"reproductionSha256"`
			FinishedAt             string            `json:"finishedAt"`
			InputSHA256            map[string]string `json:"inputSha256"`
			OutputSHA256           map[string]string `json:"outputSha256"`
		}
		var implementation struct {
			Code                           string          `json:"code"`
			InputPaths                     []string        `json:"inputPaths"`
			InputData                      json.RawMessage `json:"inputData"`
			ExpectedEnvironmentFingerprint string          `json:"expectedEnvironmentFingerprint"`
		}
		if json.Unmarshal(result, &kernel) != nil || json.Unmarshal(step.Input, &implementation) != nil || kernel.Status != "success" || !computationDigest(kernel.CodeSHA256) || hashBytes([]byte(strings.TrimSpace(implementation.Code))) != kernel.CodeSHA256 {
			return fail("成功状态或 codeSha256 与实际执行源码不一致")
		}
		if !computationDigest(kernel.EnvironmentFingerprint) || !computationDigest(kernel.ReproductionSHA256) || kernel.FinishedAt == "" || len(kernel.InputSHA256) == 0 {
			return fail("缺少真实环境、复现或输入记录")
		}
		for _, digest := range kernel.InputSHA256 {
			if !computationDigest(digest) {
				return fail("输入文件摘要无效")
			}
		}
		expectedInputs := map[string]bool{}
		for _, path := range implementation.InputPaths {
			if !validStageMaterialPath(path) {
				return fail("声明输入路径无效")
			}
			path = filepath.ToSlash(filepath.Clean(path))
			if expectedInputs[path] || !computationDigest(kernel.InputSHA256[path]) {
				return fail("声明输入缺少或重复摘要")
			}
			expectedInputs[path] = true
			if digest, ok := workflowSnapshotExpectedHash(path); ok && digest != kernel.InputSHA256[path] {
				return fail("输入快照摘要与内容寻址身份不一致")
			}
		}
		if len(implementation.InputData) > 0 && string(implementation.InputData) != "null" {
			expectedInputs["$data"] = true
			if kernel.InputSHA256["$data"] != pythonenv.KernelInputDataSHA256(implementation.InputData) {
				return fail("inputData 与实际执行参数摘要不一致")
			}
		}
		if len(expectedInputs) != len(kernel.InputSHA256) {
			return fail("实际输入摘要集合与声明文件及参数不一致")
		}
		if implementation.ExpectedEnvironmentFingerprint != "" && implementation.ExpectedEnvironmentFingerprint != kernel.EnvironmentFingerprint {
			return fail("执行环境与冻结环境指纹不一致")
		}
		var artifacts []tool.ArtifactRef
		for _, source := range d.Run.Compilation.Edges {
			if source.ToNode == node.ID && source.ToPort == "sourceArtifacts" && source.FromNode == edge.FromNode && source.FromPort == "artifacts" {
				value, err := resolveWorkflowEdgeValue(d, source)
				if err != nil || !reviewcontract.Equal(value, fields["sourceArtifacts"]) || json.Unmarshal(value, &artifacts) != nil {
					return fail("sourceArtifacts 与本次计算产物不一致")
				}
			}
		}
		if len(artifacts) == 0 || len(artifacts) != len(kernel.OutputSHA256) {
			return fail("计算产物清单不完整")
		}
		seen := map[string]bool{}
		for _, artifact := range artifacts {
			if !validStageMaterialPath(artifact.WorkspacePath) || seen[artifact.WorkspacePath] || artifact.SizeBytes < 0 || !computationDigest(artifact.SHA256) || kernel.OutputSHA256[artifact.WorkspacePath] != artifact.SHA256 {
				return fail("产物路径或 SHA256 与实际计算输出不一致")
			}
			seen[artifact.WorkspacePath] = true
		}
		record.CodeSHA256, record.EnvironmentFingerprint, record.ReproductionSHA256 = kernel.CodeSHA256, kernel.EnvironmentFingerprint, kernel.ReproductionSHA256
		record.FinishedAt, record.InputSHA256, record.Artifacts = kernel.FinishedAt, kernel.InputSHA256, artifacts
		return record
	}
	return nil
}

func computationDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 32
}

func reportComputationAppendix(input json.RawMessage) (string, error) {
	// An arbitrary similarly named argument on a historical/custom workflow
	// is not an opt-in. Only the new host binding supplies this policy.
	if decodeObject(input)["computationRecordPolicy"] != computationRecordVersion {
		return "", nil
	}
	var stage struct {
		Record *computationRecord `json:"computationRecord"`
	}
	if err := json.Unmarshal(input, &stage); err != nil {
		return "", err
	}
	r := stage.Record
	if r == nil {
		return "", fmt.Errorf("报告计算执行记录缺失")
	}
	if r.Version != computationRecordVersion || r.BindingError != "" {
		return "", fmt.Errorf("报告计算执行记录无法核验：%s", r.BindingError)
	}
	if r.SourceStepID == "" || r.Attempt < 1 || !computationDigest(r.CodeSHA256) || !computationDigest(r.EnvironmentFingerprint) || !computationDigest(r.ReproductionSHA256) || len(r.Artifacts) == 0 {
		return "", fmt.Errorf("报告计算执行记录字段不完整")
	}
	var b strings.Builder
	b.WriteString(computationAppendixHeading + "\n\n以下为程序依据本次成功计算的冻结记录生成的执行追溯，不表示方法或结论已获科学批准。\n")
	fmt.Fprintf(&b, "\n代码 SHA256：`%s`\n\n环境指纹：`%s`\n\n复现摘要：`%s`\n\n完成时间（UTC）：%s\n", r.CodeSHA256, r.EnvironmentFingerprint, r.ReproductionSHA256, researchTableText(r.FinishedAt))
	b.WriteString("\n### 输入摘要\n\n| 输入 | SHA256 |\n| --- | --- |\n")
	keys := make([]string, 0, len(r.InputSHA256))
	for key := range r.InputSHA256 {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "| %s | %s |\n", researchTableText(key), researchTableText(r.InputSHA256[key]))
	}
	b.WriteString("\n### 本次输出\n\n| 文件 | 字节数 | SHA256 |\n| --- | ---: | --- |\n")
	for _, file := range r.Artifacts {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", researchTableText(file.WorkspacePath), file.SizeBytes, file.SHA256)
	}
	return strings.TrimSpace(b.String()), nil
}

func computationRecordSection(input json.RawMessage) (string, error) {
	part, err := reportComputationAppendix(input)
	if err == nil && part != "" && usesExplicitReportAssembly(input) {
		part = managedReportSection(part)
	}
	return part, err
}

// The original schema continues to bound authored content. Only the exact,
// current deterministic suffix (v2 design assembly, or legacy computation) is
// outside that allowance. No
// model-authored text, similar heading, or altered record gets extra capacity.
func validateReportOutputSchema(node CompiledNode, value, input json.RawMessage) error {
	projected := value
	if usesReportProvenance(node) {
		var fields map[string]json.RawMessage
		var markdown string
		_ = json.Unmarshal(value, &fields)
		_ = json.Unmarshal(fields["markdown"], &markdown)
		section, err := reportParsingSuffix(input, markdown)
		if err != nil {
			return err
		}
		if section != "" {
			if json.Unmarshal(value, &fields) == nil && json.Unmarshal(fields["markdown"], &markdown) == nil && strings.HasSuffix(strings.TrimSpace(markdown), section) {
				body := strings.TrimSpace(markdown)
				if body == section {
					body = ""
				} else if strings.HasSuffix(body, "\n\n"+section) {
					body = strings.TrimSuffix(body, "\n\n"+section)
				} else {
					return fmt.Errorf("计算执行记录与报告正文缺少明确分界")
				}
				fields["markdown"], _ = json.Marshal(body)
				projected = mustJSON(fields)
			}
		}
	}
	return (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, projected)
}

// The normal parser still selects the candidate, envelope and trailing syntax.
// This optional projection removes only its exact current record string token,
// preserving EVERY original byte elsewhere before the original size check.
func computationRecordParsingProjection(node CompiledNode, input json.RawMessage) ([]stageJSONProjection, func(json.RawMessage) bool, func() []AIStageNormalization) {
	lastProjected, selectedProjected := false, false
	var lastChanges, selectedChanges []AIStageNormalization
	contains := func(json.RawMessage) bool { return selectedProjected }
	changes := func() []AIStageNormalization { return append([]AIStageNormalization(nil), selectedChanges...) }
	if !usesReportProvenance(node) {
		return nil, contains, changes
	}
	section, err := computationRecordSection(input)
	if err != nil || (section == "" && !usesDesignMaterialAssembly(input)) {
		return nil, contains, changes
	}
	project := func(candidate json.RawMessage) json.RawMessage {
		lastProjected = false
		lastChanges = nil
		if !json.Valid(candidate) {
			return candidate
		}
		decoder := json.NewDecoder(bytes.NewReader(candidate))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			return candidate
		}
		start, end, cut := -1, -1, -1
		var dedup []AIStageNormalization
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return candidate
			}
			var value json.RawMessage
			if decoder.Decode(&value) != nil {
				return candidate
			}
			if key != "markdown" {
				continue
			}
			start, end, cut = -1, -1, -1
			dedup = nil
			var markdown string
			if json.Unmarshal(value, &markdown) != nil {
				continue
			}
			section, err = reportParsingSuffix(input, markdown)
			if err != nil || section == "" {
				continue
			}
			body, copies := markdown, 0
			for body == section || strings.HasSuffix(body, "\n\n"+section) {
				copies++
				if body == section {
					body = ""
					break
				}
				body = strings.TrimSuffix(body, "\n\n"+section)
			}
			if copies == 0 {
				continue
			}
			if copies > 1 {
				dedup = []AIStageNormalization{{Path: "$.markdown", Rule: "deduplicate_identical_host_appendices", BeforeSHA256: hashBytes([]byte(markdown)), AfterSHA256: hashBytes([]byte(body + "\n\n" + section))}}
			}
			cut = jsonStringRuneOffset(value, utf8.RuneCountInString(body))
			if cut < 0 {
				continue
			}
			end = int(decoder.InputOffset())
			start = end - len(value)
		}
		if cut < 0 || start < 0 {
			return candidate
		}
		projected := append([]byte(nil), candidate[:start+cut]...)
		projected = append(projected, '"')
		projected = append(projected, candidate[end:]...)
		if len(projected) > maxStageJSONCandidateBytes || !json.Valid(projected) {
			return candidate
		}
		lastProjected = true
		lastChanges = dedup
		return projected
	}
	return []stageJSONProjection{{project: project, selected: func() { selectedProjected = lastProjected; selectedChanges = lastChanges }}}, contains, changes
}

// Input is an already valid JSON string token. Locate a decoded rune boundary
// without re-encoding the prefix: Unicode escapes, surrogate pairs, HTML chars,
// optional escaping and raw multibyte text retain their original byte costs.
func jsonStringRuneOffset(value []byte, runes int) int {
	if len(value) < 2 || value[0] != '"' {
		return -1
	}
	at := 1
	for i := 0; i < runes; i++ {
		if at >= len(value)-1 {
			return -1
		}
		if value[at] != '\\' {
			_, width := utf8.DecodeRune(value[at:])
			at += width
			continue
		}
		if at+1 >= len(value)-1 {
			return -1
		}
		if value[at+1] != 'u' {
			at += 2
			continue
		}
		if at+6 > len(value)-1 {
			return -1
		}
		high, err := strconv.ParseUint(string(value[at+2:at+6]), 16, 16)
		if err != nil {
			return -1
		}
		at += 6
		if high >= 0xd800 && high <= 0xdbff && at+6 <= len(value)-1 && string(value[at:at+2]) == "\\u" {
			low, err := strconv.ParseUint(string(value[at+2:at+6]), 16, 16)
			if err == nil && low >= 0xdc00 && low <= 0xdfff {
				at += 6
			}
		}
	}
	return at
}

func restoreComputationRecordAfterParsing(value, input json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	var markdown string
	if json.Unmarshal(value, &fields) != nil || json.Unmarshal(fields["markdown"], &markdown) != nil {
		return value
	}
	section, err := reportParsingSuffix(input, markdown)
	if err != nil || section == "" {
		return value
	}
	fields["markdown"], _ = json.Marshal(markdown + "\n\n" + section)
	return mustJSON(fields)
}
