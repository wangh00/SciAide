package workflow

import "encoding/json"

// ReferenceTemplates returns fresh copies of SciAide-owned Workflow
// definitions. Templates are still untrusted data at execution time and must
// pass the current compiler before they can be saved or run.
func ReferenceTemplates() []Template {
	return []Template{researchClosureTemplate(), xlsxAnalysisTemplate(), pythonAnalysisTemplate(), researchDesignTemplate()}
}

func independentReviewNode(id, name, promptVersion string) Node {
	return Node{
		ID: id, Name: name, Kind: NodeAIAnalysis, Arguments: raw(`{}`), PromptVersion: promptVersion, ReviewPolicy: AIReviewAuto,
		Prompt:       "你是独立的科研交付审查者，不是上一阶段结果的续写者。逐项核对当前冻结输入与 Workflow 中已完成的证据、Python 结果、方法约束和产物快照。检查不受支持的主张、不可追溯数字、无效或越界引用、方法错误、因果夸大和遗漏局限。问题数组只能写尚未解决且可执行修正的实质缺陷；不得把‘未发现问题’、已通过检查、一般提醒或已接受的局限写进问题数组。通过项写入 verifiedClaims，残余局限写入 limitations。approved=true 时所有问题数组和 requiredCorrections 必须为空；任一问题数组非空时 approved 必须为 false，并在 requiredCorrections 中归纳对应修正动作。reviewedInputSha256 必须原样复制宿主在当前阶段指令中给出的 stage_input SHA-256。不要用行文流畅度代替事实核验。",
		OutputSchema: withRevisionPlanSchema(independentReviewSchema()),
	}
}

func independentReviewSchema() json.RawMessage {
	return raw(`{"type":"object","additionalProperties":false,"required":["approved","reviewedInputSha256","verifiedClaims","unsupportedClaims","citationIssues","numericIssues","methodIssues","requiredCorrections","confidence","limitations"],"properties":{"approved":{"type":"boolean"},"reviewedInputSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"verifiedClaims":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"unsupportedClaims":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"citationIssues":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"numericIssues":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"methodIssues":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"requiredCorrections":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"confidence":{"type":"string","enum":["low","medium","high"]},"limitations":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}}}}`)
}

func reviewGateNode(id, name string) Node {
	return Node{ID: id, Name: name, Kind: NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: raw(`{}`)}
}

// ResearchStarterTemplate is a system Workflow. It is persisted so its AI
// exploration can be cancelled, resumed, audited, and reopened like any other
// research task, but it is intentionally not exposed as a reusable plan.
func ResearchStarterTemplate() Template {
	return dynamicResearchStarterTemplate()
}

func researchDesignTemplate() Template {
	return Template{
		ID:          "research-design",
		Name:        "研究设计与开题方案",
		Description: "把尚无数据或证据不足的研究想法转化为可执行、可审查的研究设计。",
		Definition: Definition{
			SchemaVersion: SchemaVersion,
			Name:          "研究设计与开题方案",
			Description:   "AI 在只读科研工具和动态 Skill 辅助下形成研究问题、采样、数据收集、分析与风险计划；输出是设计而非实证结论。",
			Inputs:        []Port{{Name: "research_goal", Type: TypeString, Description: "原始研究想法或希望解决的问题", Required: true}},
			Nodes: []Node{
				{
					ID: "design", Name: "形成可执行研究设计", Kind: NodeAgentStage, Arguments: raw(`{}`),
					PromptVersion: "research-design-v1", ReviewPolicy: AIReviewAuto, SkillRouting: true,
					AllowedTools: []string{"builtin.knowledge.search", "builtin.workspace.read_text"},
					Prompt:       "依据原始研究目标、当前项目资料以及实际检索到的公开发现记录，形成一份可执行且可审查的研究设计。明确区分已知事实、待验证假设和方法建议。必须覆盖研究问题、可检验假设或探索目标、对象与采样、变量或材料、数据收集、分析计划、质量控制、伦理与风险、执行里程碑以及当前证据缺口。没有可信本地引用时必须如实披露；这是研究设计产物，不是数据结论或证据已经充分的报告。",
					OutputSchema: raw(`{"type":"object","additionalProperties":false,"required":["title","researchQuestion","objectives","hypotheses","populationAndSampling","variablesOrMaterials","dataCollectionPlan","analysisPlan","qualityControls","ethicsAndRisks","milestones","evidenceGaps","limitations","status"],"properties":{"title":{"type":"string","minLength":1,"maxLength":240},"researchQuestion":{"type":"string","minLength":1,"maxLength":4000},"objectives":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"hypotheses":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"populationAndSampling":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"variablesOrMaterials":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"string","maxLength":2000}},"dataCollectionPlan":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"string","maxLength":2000}},"analysisPlan":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"string","maxLength":2000}},"qualityControls":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"ethicsAndRisks":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"milestones":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"evidenceGaps":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"limitations":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"status":{"type":"string","const":"research_design_not_empirical_result"}}}`),
				},
				independentReviewNode("review", "独立复核研究设计", "research-design-review-v1"),
				reviewGateNode("review_gate", "核验研究设计交付条件"),
			},
			Edges: []Edge{
				{FromNode: "$input", FromPort: "research_goal", ToNode: "design", ToPort: "context"},
				{FromNode: "design", FromPort: "analysis", ToNode: "review", ToPort: "context"},
				{FromNode: "design", FromPort: "analysis", ToNode: "review_gate", ToPort: "subject"},
				{FromNode: "review", FromPort: "analysis", ToNode: "review_gate", ToPort: "review"},
			},
			Outputs: []Output{
				{Name: "research_design", Type: TypeObject, FromNode: "design", FromPort: "analysis", Required: true, Description: "结构化研究设计，不代表已有实证结论"},
				{Name: "independent_review", Type: TypeObject, FromNode: "review", FromPort: "analysis", Required: true, Description: "独立二次审查快照"},
				{Name: "delivery_gate", Type: TypeObject, FromNode: "review_gate", FromPort: "structured", Required: true, Description: "宿主确定性交付门禁"},
			},
		},
	}
}

const xlsxAnalysisScript = `import csv
import html
import math
import posixpath
import statistics
import sys
import zipfile
from pathlib import Path
from xml.etree import ElementTree as ET

MAIN_NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"
DOC_REL_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
MAX_ROWS = 200000
MAX_COLUMNS = 256
MAX_CELLS = 2000000
MAX_CELL_CHARACTERS = 10000


def _archive_path(base, target):
    if target.startswith("/"):
        value = target.lstrip("/")
    else:
        value = posixpath.normpath(posixpath.join(posixpath.dirname(base), target))
    if value == ".." or value.startswith("../"):
        raise ValueError("XLSX relationship escapes the archive")
    return value


def _column_index(reference):
    letters = "".join(character for character in reference if character.isalpha()).upper()
    value = 0
    for character in letters:
        value = value * 26 + ord(character) - 64
    return value - 1


def _cell_value(cell, shared):
    cell_type = cell.attrib.get("t", "")
    if cell_type == "inlineStr":
        result = "".join(node.text or "" for node in cell.findall(".//{%s}t" % MAIN_NS))
        if len(result) > MAX_CELL_CHARACTERS:
            raise ValueError("an XLSX cell exceeds 10000 characters")
        return result
    value_node = cell.find("{%s}v" % MAIN_NS)
    value = "" if value_node is None or value_node.text is None else value_node.text
    if cell_type == "s" and value:
        index = int(value)
        result = shared[index] if 0 <= index < len(shared) else ""
        if len(result) > MAX_CELL_CHARACTERS:
            raise ValueError("an XLSX cell exceeds 10000 characters")
        return result
    if cell_type == "b":
        return "TRUE" if value == "1" else "FALSE"
    if len(value) > MAX_CELL_CHARACTERS:
        raise ValueError("an XLSX cell exceeds 10000 characters")
    return value


def read_first_sheet(path):
    with zipfile.ZipFile(path) as archive:
        workbook = ET.fromstring(archive.read("xl/workbook.xml"))
        relationships = ET.fromstring(archive.read("xl/_rels/workbook.xml.rels"))
        targets = {item.attrib["Id"]: item.attrib["Target"] for item in relationships.findall("{%s}Relationship" % REL_NS)}
        sheet = workbook.find("{%s}sheets/{%s}sheet" % (MAIN_NS, MAIN_NS))
        if sheet is None:
            raise ValueError("XLSX contains no worksheet")
        relation_id = sheet.attrib.get("{%s}id" % DOC_REL_NS, "")
        if relation_id not in targets:
            raise ValueError("XLSX worksheet relationship is missing")
        sheet_path = _archive_path("xl/workbook.xml", targets[relation_id])
        shared = []
        if "xl/sharedStrings.xml" in archive.namelist():
            strings_root = ET.fromstring(archive.read("xl/sharedStrings.xml"))
            for item in strings_root.findall("{%s}si" % MAIN_NS):
                value = "".join(node.text or "" for node in item.findall(".//{%s}t" % MAIN_NS))
                if len(value) > MAX_CELL_CHARACTERS:
                    raise ValueError("an XLSX shared string exceeds 10000 characters")
                shared.append(value)
        root = ET.fromstring(archive.read(sheet_path))
        rows = []
        cell_count = 0
        for row in root.findall("{%s}sheetData/{%s}row" % (MAIN_NS, MAIN_NS)):
            values = {}
            for cell in row.findall("{%s}c" % MAIN_NS):
                index = _column_index(cell.attrib.get("r", ""))
                if index >= MAX_COLUMNS:
                    raise ValueError("the XLSX worksheet exceeds 256 columns")
                if index >= 0:
                    cell_count += 1
                    if cell_count > MAX_CELLS:
                        raise ValueError("the XLSX worksheet exceeds 2000000 cells")
                    values[index] = _cell_value(cell, shared)
            if values:
                rows.append([values.get(index, "") for index in range(max(values) + 1)])
                if len(rows) > MAX_ROWS + 1:
                    raise ValueError("the XLSX worksheet exceeds 200000 rows")
        return sheet.attrib.get("name", "Sheet1"), rows


def _clean_text(value):
    result = " ".join(str(value).strip().split())
    if len(result) > MAX_CELL_CHARACTERS:
        raise ValueError("an XLSX cell exceeds 10000 characters")
    return result


def _csv_safe(value):
    text = str(value)
    if text.startswith(("=", "+", "@")) or (text.startswith("-") and _number(text) is None):
        return "'" + text
    return text


def _number(value):
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    return number if math.isfinite(number) else None


def analyze(workbook_path, cleaned_path, summary_path, chart_path):
    sheet_name, source_rows = read_first_sheet(workbook_path)
    source_rows = [[_clean_text(value) for value in row] for row in source_rows]
    source_rows = [row for row in source_rows if any(row)]
    if not source_rows:
        raise ValueError("the first XLSX worksheet contains no data")
    width = max(len(row) for row in source_rows)
    source_rows = [row + [""] * (width - len(row)) for row in source_rows]
    raw_headers = source_rows[0]
    headers, used = [], {}
    for index, value in enumerate(raw_headers):
        base = value or "column_%d" % (index + 1)
        used[base] = used.get(base, 0) + 1
        headers.append(base if used[base] == 1 else "%s_%d" % (base, used[base]))
    rows = [row for row in source_rows[1:] if any(row)]

    for output in (cleaned_path, summary_path, chart_path):
        Path(output).parent.mkdir(parents=True, exist_ok=True)
    with open(cleaned_path, "w", newline="", encoding="utf-8-sig") as stream:
        writer = csv.writer(stream, lineterminator="\n")
        writer.writerow([_csv_safe(value) for value in headers])
        writer.writerows([_csv_safe(value) for value in row] for row in rows)

    statistics_rows = []
    for index, header in enumerate(headers):
        values = [row[index] for row in rows]
        present = [value for value in values if value != ""]
        numeric = [_number(value) for value in present]
        if present and all(value is not None for value in numeric):
            numbers = [value for value in numeric if value is not None]
            statistics_rows.append({
                "column": header,
                "count": len(numbers),
                "missing": len(values) - len(numbers),
                "mean": sum(numbers) / len(numbers),
                "median": statistics.median(numbers),
                "minimum": min(numbers),
                "maximum": max(numbers),
                "pstdev": statistics.pstdev(numbers),
            })
    if not statistics_rows:
        raise ValueError("the first XLSX worksheet contains no numeric columns")

    def formatted(value):
        return format(value, ".10g")

    with open(summary_path, "w", newline="", encoding="utf-8-sig") as stream:
        writer = csv.writer(stream, lineterminator="\n")
        writer.writerow(["column", "count", "missing", "mean", "median", "minimum", "maximum", "pstdev"])
        for item in statistics_rows:
            writer.writerow([item["column"], item["count"], item["missing"], formatted(item["mean"]), formatted(item["median"]), formatted(item["minimum"]), formatted(item["maximum"]), formatted(item["pstdev"])])

    maximum = max(abs(item["mean"]) for item in statistics_rows) or 1.0
    width, height = 760, max(180, 95 + 42 * len(statistics_rows))
    bars = []
    for index, item in enumerate(statistics_rows):
        y = 58 + index * 42
        bar_width = int(380 * abs(item["mean"]) / maximum)
        color = "#58756a" if item["mean"] >= 0 else "#a45e52"
        bars.append('<text x="20" y="%d" font-size="14" fill="#343b46">%s</text>' % (y + 17, html.escape(item["column"][:42])))
        bars.append('<rect x="270" y="%d" width="%d" height="22" fill="%s"/>' % (y, bar_width, color))
        bars.append('<text x="%d" y="%d" font-size="13" fill="#343b46">%s</text>' % (280 + bar_width, y + 17, formatted(item["mean"])))
    svg = (
        '<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">' % (width, height, width, height)
        + '<rect width="100%" height="100%" fill="#ffffff"/>'
        + '<text x="20" y="30" font-size="19" font-weight="700" fill="#343b46">Numeric column means</text>'
        + "".join(bars) + "</svg>"
    )
    with open(chart_path, "w", encoding="utf-8", newline="\n") as stream:
        stream.write(svg)
    return {"sheetName": sheet_name, "rowCount": len(rows), "columnCount": len(headers), "numericColumnCount": len(statistics_rows)}


if "SCIAIDE_INPUTS" in globals():
    if len(SCIAIDE_INPUTS) != 1 or len(SCIAIDE_OUTPUTS) != 4:
        raise ValueError("this Workflow requires one XLSX input and four declared outputs")
    if Path(SCIAIDE_INPUTS[0]).suffix.lower() != ".xlsx":
        raise ValueError("the declared input must be an .xlsx workbook")
    _SCIAIDE_XLSX_RESULT = analyze(SCIAIDE_INPUTS[0], SCIAIDE_OUTPUTS[0], SCIAIDE_OUTPUTS[1], SCIAIDE_OUTPUTS[2])
    with open(SCIAIDE_OUTPUTS[3], "w", encoding="utf-8", newline="\n") as stream:
        stream.write(globals()["script"])
elif __name__ == "__main__":
    if len(sys.argv) != 5:
        raise SystemExit("usage: xlsx_analysis.py INPUT.xlsx CLEANED.csv SUMMARY.csv CHART.svg")
    print(analyze(*sys.argv[1:]))
`

func xlsxAnalysisTemplate() Template {
	encodedScript, _ := json.Marshal(xlsxAnalysisScript)
	code := "script = " + string(encodedScript) + "\nexec(compile(script, '<sciaide-xlsx-analysis>', 'exec'))\n_SCIAIDE_XLSX_RESULT"
	return Template{
		ID:          "xlsx-descriptive-analysis",
		Name:        "XLSX 清洗与描述统计",
		Description: "读取 Workspace 中的一份 XLSX，生成清洗数据、描述统计、SVG 图和可重放 Python 脚本。",
		Definition: Definition{
			SchemaVersion: SchemaVersion,
			Name:          "XLSX 清洗与描述统计",
			Description:   "使用项目 Python Kernel 和标准库分析首张工作表；输入只读，环境、代码、输入及输出均进入复现审计。",
			Inputs: []Port{
				{Name: "input_paths", Type: TypeArray, Description: "选择一份 XLSX 工作簿", FileKind: "xlsx", MinItems: 1, MaxItems: 1, Required: true},
				{Name: "research_goal", Type: TypeString, Description: "这次分析希望解决的研究问题", Required: true, Default: raw(`"描述工作簿的数据质量与主要数值字段"`)},
			},
			Nodes: []Node{
				{ID: "environment", Name: "确保项目 Python 环境", Kind: NodeTool, ToolName: "builtin.research.workflow.python.ensure", Arguments: raw(`{}`)},
				{ID: "analysis", Name: "清洗、统计并绘图", Kind: NodePython, Arguments: rawObject(map[string]any{
					"code": code,
					"outputPaths": []string{
						"analysis-output/xlsx-{{runId}}-{{attempt}}-cleaned.csv",
						"analysis-output/xlsx-{{runId}}-{{attempt}}-summary.csv",
						"analysis-output/xlsx-{{runId}}-{{attempt}}-summary.svg",
						"analysis-output/xlsx-{{runId}}-{{attempt}}-analysis.py",
					},
					"timeoutSeconds": 120,
				})},
				{ID: "interpret", Name: "AI 解释描述统计", Kind: NodeAgentStage, Arguments: raw(`{}`), PromptVersion: "xlsx-interpret-v2", Prompt: "自主解释 Python 已生成的描述统计，指出缺失值、样本量和描述性结论的局限。按任务语义加载真正相关的科研 Skill，不得把相关性描述成因果关系，不得发明输入中没有的数字。输出通过结构化校验后由 Workflow 自动提交；仅在证据不足或存在高影响歧义时如实写入 limitations。", AllowedTools: []string{"builtin.workspace.read_text"}, SkillRouting: true, ReviewPolicy: AIReviewAuto, OutputSchema: raw(`{"type":"object","additionalProperties":false,"required":["summary","limitations"],"properties":{"summary":{"type":"string","minLength":1,"maxLength":10000},"limitations":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":2000}}}}`)},
				independentReviewNode("review", "独立审查分析结果", "xlsx-analysis-review-v1"),
				reviewGateNode("review_gate", "核验分析结果交付条件"),
			},
			Edges: []Edge{
				{FromNode: "$input", FromPort: "input_paths", ToNode: "analysis", ToPort: "inputPaths"},
				{FromNode: "$input", FromPort: "research_goal", ToNode: "analysis", ToPort: "inputData"},
				{FromNode: "environment", FromPort: "structured.environmentFingerprint", ToNode: "analysis", ToPort: "expectedEnvironmentFingerprint"},
				{FromNode: "analysis", FromPort: "structured", ToNode: "interpret", ToPort: "context"},
				{FromNode: "interpret", FromPort: "analysis", ToNode: "review", ToPort: "context"},
				{FromNode: "interpret", FromPort: "analysis", ToNode: "review_gate", ToPort: "subject"},
				{FromNode: "review", FromPort: "analysis", ToNode: "review_gate", ToPort: "review"},
			},
			Outputs: []Output{
				{Name: "analysis", Type: TypeObject, FromNode: "analysis", FromPort: "structured", Required: true, Description: "带环境、输入、代码、输出及复现哈希的分析结果"},
				{Name: "interpretation", Type: TypeObject, FromNode: "interpret", FromPort: "analysis", Required: true, Description: "经结构化校验并自动提交的 AI 结果解释"},
				{Name: "independent_review", Type: TypeObject, FromNode: "review", FromPort: "analysis", Required: true, Description: "独立二次审查快照"},
				{Name: "delivery_gate", Type: TypeObject, FromNode: "review_gate", FromPort: "structured", Required: true, Description: "宿主确定性交付门禁"},
				{Name: "artifacts", Type: TypeArtifacts, FromNode: "analysis", FromPort: "artifacts", Required: true, Description: "清洗 CSV、统计 CSV、SVG 图和可重放 Python 脚本"},
			},
		},
	}
}

func researchClosureTemplate() Template {
	const analysisCode = `import csv
import html
from collections import Counter

citations = SCIAIDE_DATA if isinstance(SCIAIDE_DATA, list) else []
csv_path, svg_path = SCIAIDE_OUTPUTS
rows = []
for item in citations:
    source = str(item.get("sourceName") or "Unknown source")
    quote = str(item.get("quote") or "")
    rows.append({
        "reference": str(item.get("reference") or ""),
        "source": source,
        "title": str(item.get("title") or ""),
        "locator": str(item.get("locator") or ""),
        "quote_characters": len(quote),
    })

with open(csv_path, "w", newline="", encoding="utf-8-sig") as stream:
    writer = csv.DictWriter(stream, fieldnames=["reference", "source", "title", "locator", "quote_characters"])
    writer.writeheader()
    writer.writerows(rows)

counts = Counter(row["source"] for row in rows)
items = sorted(counts.items(), key=lambda item: (-item[1], item[0]))
width, height = 760, max(180, 95 + 42 * len(items))
bars = []
maximum = max((count for _, count in items), default=1)
for index, (label, count) in enumerate(items):
    y = 58 + index * 42
    bar_width = int(430 * count / maximum)
    bars.append(f'<text x="20" y="{y + 17}" font-size="14" fill="#343b46">{html.escape(label[:42])}</text>')
    bars.append(f'<rect x="270" y="{y}" width="{bar_width}" height="22" fill="#58756a"/>')
    bars.append(f'<text x="{280 + bar_width}" y="{y + 17}" font-size="13" fill="#343b46">{count}</text>')
svg = (
    f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">'
    '<rect width="100%" height="100%" fill="#ffffff"/>'
    '<text x="20" y="30" font-size="19" font-weight="700" fill="#343b46">Selected evidence by source</text>'
    + ''.join(bars) + '</svg>'
)
with open(svg_path, "w", encoding="utf-8", newline="\n") as stream:
    stream.write(svg)

analysis = {
    "selectedCitationCount": len(rows),
    "sourceCounts": dict(sorted(counts.items())),
}
analysis`
	return Template{
		ID:          "trusted-research-closure",
		Name:        "可信科研闭环",
		Description: "公共数据库检索、人工筛选、本地证据、可复现 Python 分析和正式报告导出。",
		Definition: Definition{
			SchemaVersion: SchemaVersion,
			Name:          "可信科研闭环",
			Description:   "从公共数据库候选开始，经两次人工选择、本地知识检索和项目 Python 分析，生成带可信引用的不可变报告。",
			Inputs:        []Port{{Name: "query", Type: TypeString, Description: "研究问题或检索式", Required: true}},
			Nodes: []Node{
				{ID: "search", Name: "检索公共科研数据库", Kind: NodeTool, ToolName: "builtin.research.workflow.search", Arguments: raw(`{"limit":10}`)},
				{ID: "select_candidates", Name: "人工筛选文献候选", Kind: NodeCandidateSelection, Arguments: raw(`{}`), Prompt: "请选择要导入当前项目的候选文献。在线结果仅是发现数据，选中后才会进入本地材料链。"},
				{ID: "import", Name: "导入所选材料", Kind: NodeTool, ToolName: "builtin.research.workflow.import", Arguments: raw(`{"mode":"auto"}`)},
				{ID: "sync", Name: "同步本地知识索引", Kind: NodeTool, ToolName: "builtin.research.workflow.sync", Arguments: raw(`{}`)},
				{ID: "knowledge", Name: "检索本地可信证据", Kind: NodeTool, ToolName: "builtin.knowledge.search", Arguments: raw(`{"limit":20}`)},
				{ID: "select_citations", Name: "人工选择报告引用", Kind: NodeCitationSelection, Arguments: raw(`{}`), Prompt: "请选择报告可以使用的本地证据摘录。最终发布前会再次核验每个 Citation 快照。"},
				{ID: "environment", Name: "确保项目 Python 环境", Kind: NodeTool, ToolName: "builtin.research.workflow.python.ensure", Arguments: raw(`{}`)},
				{ID: "analysis", Name: "生成 CSV 与 SVG 分析产物", Kind: NodePython, Arguments: rawObject(map[string]any{
					"code": analysisCode, "outputPaths": []string{"analysis-output/research-{{runId}}-{{attempt}}.csv", "analysis-output/research-{{runId}}-{{attempt}}.svg"}, "timeoutSeconds": 120,
				})},
				{ID: "interpret", Name: "AI 审查证据并解释结果", Kind: NodeAgentStage, Arguments: raw(`{}`), PromptVersion: "research-interpret-v2", Prompt: "自主结合已经人工选择的可信引用和 Python 产生的结构化分析，解释结果、局限与可能的替代解释，并按任务语义加载真正相关的科研 Skill。引用只能来自阶段输入，不得新增不存在的数值或文献。先给出面向研究者的判断依据，再返回结构化审查结果；输出通过结构化校验后由 Workflow 自动提交。证据冲突或不足必须降低 confidence 并写入 limitations，不得静默补全。", AllowedTools: []string{"builtin.knowledge.search", "builtin.workspace.read_text"}, SkillRouting: true, ReviewPolicy: AIReviewAuto, OutputSchema: raw(`{"type":"object","additionalProperties":false,"required":["summary","limitations","confidence"],"properties":{"summary":{"type":"string","minLength":1,"maxLength":10000},"limitations":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":2000}},"confidence":{"type":"string","enum":["low","medium","high"]}}}`)},
				{ID: "draft_report", Name: "生成待审查研究报告", Kind: NodeAIAnalysis, Arguments: raw(`{}`), PromptVersion: "trusted-report-draft-v1", ReviewPolicy: AIReviewAuto, Prompt: "依据上一阶段冻结的结果解释和宿主提供的可信引用生成完整 Markdown 报告草稿。正文必须覆盖研究问题、方法、证据、可复现分析、结果、局限与结论。所有事实引用只能使用 trusted_workflow_citations 中的现有标记；所有数字必须可追溯到冻结的 Python 结果。不得把模型推断写成计算事实，不得隐藏证据冲突或数据缺口。", OutputSchema: raw(`{"type":"object","additionalProperties":false,"required":["markdown","claimSummary","limitations","confidence"],"properties":{"markdown":{"type":"string","minLength":1,"maxLength":180000},"claimSummary":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":2000}},"limitations":{"type":"array","maxItems":50,"items":{"type":"string","maxLength":2000}},"confidence":{"type":"string","enum":["low","medium","high"]}}}`)},
				independentReviewNode("review", "独立审查报告与结果", "trusted-report-review-v1"),
				reviewGateNode("review_gate", "核验报告交付条件"),
				{ID: "report", Name: "发布可信科研报告", Kind: NodeTool, ToolName: "builtin.research.workflow.report", Arguments: rawObject(map[string]any{"name": "可信科研证据报告"})},
			},
			Edges: []Edge{
				{FromNode: "$input", FromPort: "query", ToNode: "search", ToPort: "query"},
				{FromNode: "search", FromPort: "structured.queryText", ToNode: "select_candidates", ToPort: "query"},
				{FromNode: "search", FromPort: "structured.candidates", ToNode: "select_candidates", ToPort: "candidates"},
				{FromNode: "select_candidates", FromPort: "selectedCandidateIds", ToNode: "import", ToPort: "selectedCandidateIds"},
				{FromNode: "import", FromPort: "structured.attachmentIds", ToNode: "sync", ToPort: "attachmentIds"},
				{FromNode: "$input", FromPort: "query", ToNode: "knowledge", ToPort: "query"},
				{FromNode: "sync", FromPort: "structured.documentIds", ToNode: "knowledge", ToPort: "documentIds"},
				{FromNode: "$input", FromPort: "query", ToNode: "select_citations", ToPort: "query"},
				{FromNode: "knowledge", FromPort: "citations", ToNode: "select_citations", ToPort: "candidates"},
				{FromNode: "select_citations", FromPort: "citations", ToNode: "environment", ToPort: "context"},
				{FromNode: "select_citations", FromPort: "citations", ToNode: "analysis", ToPort: "inputData"},
				{FromNode: "environment", FromPort: "structured.environmentFingerprint", ToNode: "analysis", ToPort: "expectedEnvironmentFingerprint"},
				{FromNode: "analysis", FromPort: "structured", ToNode: "interpret", ToPort: "context"},
				{FromNode: "interpret", FromPort: "analysis", ToNode: "draft_report", ToPort: "context"},
				{FromNode: "draft_report", FromPort: "analysis", ToNode: "review", ToPort: "context"},
				{FromNode: "draft_report", FromPort: "analysis", ToNode: "review_gate", ToPort: "subject"},
				{FromNode: "review", FromPort: "analysis", ToNode: "review_gate", ToPort: "review"},
				{FromNode: "select_citations", FromPort: "citations", ToNode: "report", ToPort: "citations"},
				{FromNode: "draft_report", FromPort: "analysis", ToNode: "report", ToPort: "reportDraft"},
				{FromNode: "analysis", FromPort: "artifacts", ToNode: "report", ToPort: "sourceArtifacts"},
				{FromNode: "review_gate", FromPort: "structured", ToNode: "report", ToPort: "reviewGate"},
			},
			Outputs: []Output{
				{Name: "report", Type: TypeObject, FromNode: "report", FromPort: "structured", Required: true, Description: "不可变报告及 DOCX/PDF 导出快照"},
				{Name: "analysis_artifacts", Type: TypeArtifacts, FromNode: "analysis", FromPort: "artifacts", Required: true, Description: "CSV 与 SVG 分析产物"},
				{Name: "report_artifacts", Type: TypeArtifacts, FromNode: "report", FromPort: "artifacts", Required: true, Description: "报告、DOCX 与 PDF Artifact"},
				{Name: "independent_review", Type: TypeObject, FromNode: "review", FromPort: "analysis", Required: true, Description: "独立二次审查快照"},
				{Name: "delivery_gate", Type: TypeObject, FromNode: "review_gate", FromPort: "structured", Required: true, Description: "报告发布前宿主确定性门禁"},
			},
		},
	}
}

const delimitedAnalysisScript = `import csv
import html
import io
import json
import math
import statistics
import sys
from collections import Counter
from pathlib import Path

MAX_ROWS = 200000
MAX_COLUMNS = 256
MAX_CELLS = 2000000
MAX_CELL_CHARACTERS = 10000


def _read_text(path):
    contents = Path(path).read_bytes()
    for encoding in ("utf-8-sig", "utf-8", "gb18030"):
        try:
            return contents.decode(encoding), encoding
        except UnicodeDecodeError:
            pass
    raise ValueError("CSV/TSV must use UTF-8, UTF-8 BOM, or GB18030 text encoding")


def _delimiter(path, sample):
    suffix = Path(path).suffix.lower()
    fallback = "\t" if suffix == ".tsv" else ","
    try:
        return csv.Sniffer().sniff(sample[:65536], delimiters=",\t;").delimiter
    except csv.Error:
        return fallback


def _clean(value):
    result = " ".join(str(value).strip().split())
    if len(result) > MAX_CELL_CHARACTERS:
        raise ValueError("a data cell exceeds 10000 characters")
    return result


def _number(value):
    try:
        result = float(value)
    except (TypeError, ValueError):
        return None
    return result if math.isfinite(result) else None


def _csv_safe(value):
    text = str(value)
    if text.startswith(("=", "+", "@")) or (text.startswith("-") and _number(text) is None):
        return "'" + text
    return text


def _headers(values):
    result, used = [], {}
    for index, value in enumerate(values):
        base = _clean(value) or "column_%d" % (index + 1)
        used[base] = used.get(base, 0) + 1
        result.append(base if used[base] == 1 else "%s_%d" % (base, used[base]))
    return result


def _formatted(value):
    return "" if value is None else format(value, ".10g")


def _summaries(headers, rows):
    result = []
    for index, header in enumerate(headers):
        values = [row[index] for row in rows]
        present = [value for value in values if value != ""]
        numeric = [_number(value) for value in present]
        is_numeric = bool(present) and all(value is not None for value in numeric)
        numbers = [value for value in numeric if value is not None]
        counts = Counter(present)
        top = " | ".join("%s (%d)" % (value[:80], count) for value, count in sorted(counts.items(), key=lambda item: (-item[1], item[0]))[:5])
        result.append({
            "column": header,
            "type": "numeric" if is_numeric else "text",
            "count": len(present),
            "missing": len(values) - len(present),
            "unique": len(counts),
            "mean": sum(numbers) / len(numbers) if numbers else None,
            "median": statistics.median(numbers) if numbers else None,
            "minimum": min(numbers) if numbers else None,
            "maximum": max(numbers) if numbers else None,
            "pstdev": statistics.pstdev(numbers) if numbers else None,
            "top_values": top,
        })
    return result


def _write_svg(path, summaries, method):
    if method == "data_quality":
        title = "Missing values by column"
        values = [(item["column"], item["missing"]) for item in summaries]
        color = "#d65f59"
    else:
        numeric = [item for item in summaries if item["type"] == "numeric"]
        if numeric:
            title = "Numeric column means"
            values = [(item["column"], item["mean"] or 0) for item in numeric]
            color = "#3978d4"
        else:
            title = "Non-missing values by column"
            values = [(item["column"], item["count"]) for item in summaries]
            color = "#2e9c95"
    values = values[:40]
    maximum = max([abs(value) for _, value in values] or [1]) or 1
    width, height = 820, max(180, 88 + 36 * len(values))
    bars = []
    for index, (label, value) in enumerate(values):
        y = 52 + index * 36
        bar_width = int(400 * abs(value) / maximum)
        bars.append('<text x="20" y="%d" font-size="13" fill="#303744">%s</text>' % (y + 16, html.escape(label[:48])))
        bars.append('<rect x="310" y="%d" width="%d" height="21" rx="3" fill="%s"/>' % (y, bar_width, color))
        bars.append('<text x="%d" y="%d" font-size="12" fill="#303744">%s</text>' % (320 + bar_width, y + 15, html.escape(_formatted(value))))
    svg = '<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d"><rect width="100%%" height="100%%" fill="#fff"/><text x="20" y="28" font-size="18" font-weight="700" fill="#20242d">%s</text>%s</svg>' % (width, height, width, height, html.escape(title), "".join(bars))
    Path(path).write_text(svg, encoding="utf-8", newline="\n")


def analyze(input_path, cleaned_path, summary_path, chart_path, methods_path, request):
    goal = _clean((request or {}).get("goal") or "")
    method = str((request or {}).get("method") or "overview").strip()
    if not goal:
        raise ValueError("analysis goal is required")
    if method not in ("overview", "data_quality", "numeric_distribution"):
        raise ValueError("unsupported analysis method")
    source, encoding = _read_text(input_path)
    delimiter = _delimiter(input_path, source)
    source_rows = []
    cell_count = 0
    for row in csv.reader(io.StringIO(source, newline=""), delimiter=delimiter):
        cleaned = [_clean(value) for value in row]
        if any(cleaned):
            cell_count += len(cleaned)
            if cell_count > MAX_CELLS:
                raise ValueError("the data file exceeds 2000000 cells")
            source_rows.append(cleaned)
            if len(source_rows) > MAX_ROWS + 1:
                raise ValueError("the data file exceeds 200000 rows")
    if len(source_rows) < 2:
        raise ValueError("the data file must contain a header and at least one data row")
    width = max(len(row) for row in source_rows)
    if width == 0 or width > MAX_COLUMNS:
        raise ValueError("the data file must contain 1-256 columns")
    source_rows = [row + [""] * (width - len(row)) for row in source_rows]
    headers, rows = _headers(source_rows[0]), [row[:width] for row in source_rows[1:]]
    summaries = _summaries(headers, rows)
    for output in (cleaned_path, summary_path, chart_path, methods_path):
        Path(output).parent.mkdir(parents=True, exist_ok=True)
    with open(cleaned_path, "w", newline="", encoding="utf-8-sig") as stream:
        writer = csv.writer(stream, lineterminator="\n")
        writer.writerow(headers)
        writer.writerows([_csv_safe(value) for value in row] for row in rows)
    with open(summary_path, "w", newline="", encoding="utf-8-sig") as stream:
        fields = ["column", "type", "count", "missing", "unique", "mean", "median", "minimum", "maximum", "pstdev", "top_values"]
        writer = csv.DictWriter(stream, fieldnames=fields, lineterminator="\n")
        writer.writeheader()
        for item in summaries:
            writer.writerow({key: _csv_safe(_formatted(item[key]) if key in ("mean", "median", "minimum", "maximum", "pstdev") else item[key]) for key in fields})
    _write_svg(chart_path, summaries, method)
    method_names = {"overview": "数据概览", "data_quality": "数据质量检查", "numeric_distribution": "数值分布概览"}
    methods = "# 分析方法\n\n- 研究目标：%s\n- 分析方法：%s\n- 输入编码：%s\n- 分隔符：%s\n- 数据规模：%d 行 × %d 列\n- 数值字段：%d\n- 限制：当前结果为描述性探索，不代表因果推断或显著性检验。\n" % (goal, method_names[method], encoding, repr(delimiter), len(rows), len(headers), sum(1 for item in summaries if item["type"] == "numeric"))
    Path(methods_path).write_text(methods, encoding="utf-8", newline="\n")
    return {"goal": goal, "method": method, "encoding": encoding, "delimiter": delimiter, "rowCount": len(rows), "columnCount": len(headers), "numericColumnCount": sum(1 for item in summaries if item["type"] == "numeric"), "missingCellCount": sum(item["missing"] for item in summaries)}


if "SCIAIDE_INPUTS" in globals():
    if len(SCIAIDE_INPUTS) != 1 or len(SCIAIDE_OUTPUTS) != 5:
        raise ValueError("this Workflow requires one CSV/TSV input and five declared outputs")
    if Path(SCIAIDE_INPUTS[0]).suffix.lower() not in (".csv", ".tsv"):
        raise ValueError("the declared input must be a .csv or .tsv file")
    _SCIAIDE_DELIMITED_RESULT = analyze(SCIAIDE_INPUTS[0], SCIAIDE_OUTPUTS[0], SCIAIDE_OUTPUTS[1], SCIAIDE_OUTPUTS[2], SCIAIDE_OUTPUTS[3], SCIAIDE_DATA)
    Path(SCIAIDE_OUTPUTS[4]).write_text(globals()["script"], encoding="utf-8", newline="\n")
elif __name__ == "__main__":
    if len(sys.argv) != 7:
        raise SystemExit("usage: analysis.py INPUT CLEANED SUMMARY CHART METHODS REQUEST_JSON")
    print(analyze(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5], json.loads(sys.argv[6])))
`

func pythonAnalysisTemplate() Template {
	encodedScript, _ := json.Marshal(delimitedAnalysisScript)
	code := "script = " + string(encodedScript) + "\nexec(compile(script, '<sciaide-delimited-analysis>', 'exec'))\n_SCIAIDE_DELIMITED_RESULT"
	return Template{ID: "python-analysis", Name: "数据探索与可复现分析", Description: "选择 CSV/TSV 数据和分析目标，生成清洗数据、字段统计、SVG 图、方法说明及可重放脚本。", Definition: Definition{
		SchemaVersion: SchemaVersion, Name: "数据探索与可复现分析", Description: "使用固定且经过审计的分析程序处理项目数据；目标只作为结构化输入，不会拼接为 Python 代码。环境、代码、输入及输出均进入复现审计。",
		Inputs: []Port{
			{Name: "input_paths", Type: TypeArray, Description: "选择一份 CSV 或 TSV 数据文件", FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true},
			{Name: "analysis_request", Type: TypeObject, Description: "填写研究目标并选择分析方法", Control: "analysis_request", Required: true},
		},
		Nodes: []Node{
			{ID: "environment", Name: "准备项目 Python 环境", Kind: NodeTool, ToolName: "builtin.research.workflow.python.ensure", Arguments: raw(`{}`)},
			{ID: "analysis", Name: "清洗、统计并生成图表", Kind: NodePython, Arguments: rawObject(map[string]any{
				"code": code,
				"outputPaths": []string{
					"analysis-output/data-{{runId}}-{{attempt}}-cleaned.csv",
					"analysis-output/data-{{runId}}-{{attempt}}-summary.csv",
					"analysis-output/data-{{runId}}-{{attempt}}-overview.svg",
					"analysis-output/data-{{runId}}-{{attempt}}-methods.md",
					"analysis-output/data-{{runId}}-{{attempt}}-analysis.py",
				},
				"timeoutSeconds": 120,
			})},
			{ID: "interpret", Name: "AI 解释分析与局限", Kind: NodeAgentStage, Arguments: raw(`{}`), PromptVersion: "tabular-interpret-v2", Prompt: "自主依据 Python 阶段的结构化输出解释数据质量、分布和所选方法的局限，并按任务语义加载真正相关的科研 Skill。明确区分计算事实、推断和建议，不得发明数字。输出通过结构化校验后由 Workflow 自动提交；只有证据不足、方法前提不满足或存在高影响歧义时才将问题明确写入 limitations 和 nextSteps。", AllowedTools: []string{"builtin.workspace.read_text"}, SkillRouting: true, ReviewPolicy: AIReviewAuto, OutputSchema: raw(`{"type":"object","additionalProperties":false,"required":["findings","limitations","nextSteps"],"properties":{"findings":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"limitations":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":2000}},"nextSteps":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":2000}}}}`)},
			independentReviewNode("review", "独立审查分析结果", "tabular-analysis-review-v1"),
			reviewGateNode("review_gate", "核验分析结果交付条件"),
		},
		Edges: []Edge{
			{FromNode: "$input", FromPort: "input_paths", ToNode: "analysis", ToPort: "inputPaths"},
			{FromNode: "$input", FromPort: "analysis_request", ToNode: "analysis", ToPort: "inputData"},
			{FromNode: "environment", FromPort: "structured.environmentFingerprint", ToNode: "analysis", ToPort: "expectedEnvironmentFingerprint"},
			{FromNode: "analysis", FromPort: "structured", ToNode: "interpret", ToPort: "context"},
			{FromNode: "interpret", FromPort: "analysis", ToNode: "review", ToPort: "context"},
			{FromNode: "interpret", FromPort: "analysis", ToNode: "review_gate", ToPort: "subject"},
			{FromNode: "review", FromPort: "analysis", ToNode: "review_gate", ToPort: "review"},
		},
		Outputs: []Output{
			{Name: "analysis", Type: TypeObject, FromNode: "analysis", FromPort: "structured", Required: true, Description: "带环境、代码、输入、输出和复现哈希的分析结果"},
			{Name: "interpretation", Type: TypeObject, FromNode: "interpret", FromPort: "analysis", Required: true, Description: "经 Schema 校验并冻结的 AI 分析解释"},
			{Name: "independent_review", Type: TypeObject, FromNode: "review", FromPort: "analysis", Required: true, Description: "独立二次审查快照"},
			{Name: "delivery_gate", Type: TypeObject, FromNode: "review_gate", FromPort: "structured", Required: true, Description: "宿主确定性交付门禁"},
			{Name: "artifacts", Type: TypeArtifacts, FromNode: "analysis", FromPort: "artifacts", Required: true, Description: "清洗数据、字段统计、SVG 图、方法说明和可重放脚本"},
		},
	}}
}

func raw(value string) json.RawMessage { return json.RawMessage(value) }

func rawObject(value map[string]any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
