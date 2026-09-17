package workflow

// The preview is bounded, but CSV/TSV shape and counts cover the entire file.
// Preserve quoted newlines: splitting text into lines corrupts valid cells.
const dynamicDataPreflightCode = `import csv
import io
import json
import zipfile
from pathlib import Path

def inspect_file(path):
    suffix = path.suffix.lower()
    summary = {"preflightVersion": 2, "fileName": path.name, "fileSize": path.stat().st_size, "format": suffix.lstrip("."), "columns": [], "preview": [], "limitations": []}
    if suffix in (".csv", ".tsv"):
        raw = path.read_bytes()
        encoding = "utf-8-sig"
        try:
            text = raw.decode(encoding)
        except UnicodeDecodeError:
            encoding = "gb18030"
            text = raw.decode(encoding)
        reader = csv.reader(io.StringIO(text, newline=""), delimiter="\t" if suffix == ".tsv" else ",", strict=True)
        headers = next(reader, None)
        if not headers or any(not value.strip() for value in headers):
            raise ValueError("数据预检失败：表格为空或存在空字段名，请确认表头后重新选择数据。")
        normalized = [value.strip() for value in headers]
        if len(set(normalized)) != len(normalized):
            raise ValueError("数据预检失败：存在重复字段名，请明确各列含义并重命名，避免分析错列。")
        row_count = blank_records = missing_cells = populated_cells = 0
        for row in reader:
            if not row:
                blank_records += 1
                continue
            if len(row) != len(headers):
                raise ValueError("数据预检失败：第 %d 行的列数与表头不一致，不能静默丢弃或截断记录。" % reader.line_num)
            row_count += 1
            missing = sum(not value.strip() for value in row)
            missing_cells += missing
            populated_cells += len(row) - missing
            if len(summary["preview"]) < 10:
                summary["preview"].append([value[:500] for value in row[:256]])
        if row_count == 0 or populated_cells == 0:
            raise ValueError("数据预检失败：表格没有可分析的数据记录，不能生成实证结果。")
        summary.update({"encoding": encoding, "columns": [value[:200] for value in headers[:256]], "columnCount": len(headers), "rowCount": row_count, "blankRecordCount": blank_records, "missingCellCount": missing_cells, "shapeCheckedAllRows": True})
        summary["limitations"].append("记录数不等于独立样本量；预检不判定变量语义、单位、重复测量关系或统计方法适用性。")
        if len(headers) > 256:
            summary["limitations"].append("字段预览只展示前 256 列；结构检查覆盖全部列。")
    elif suffix == ".xlsx":
        with zipfile.ZipFile(path) as archive:
            summary["archiveEntries"] = len(archive.namelist())
            summary["hasWorkbook"] = "xl/workbook.xml" in archive.namelist()
            if not summary["hasWorkbook"]:
                raise ValueError("数据预检失败：XLSX 缺少工作簿结构。")
        summary["shapeCheckedAllRows"] = False
        summary["limitations"].append("XLSX 预检仅验证文件结构；工作表选择、完整行列检查和有效记录数必须由方法实现明确报告，尚未通过数据质量验收。")
    else:
        raise ValueError("dynamic data analysis requires CSV, TSV, or XLSX")
    return summary

files = []
for index, source in enumerate(SCIAIDE_INPUTS):
    try:
        item = inspect_file(Path(source))
    except Exception as error:
        raise ValueError('Input %d (%s): %s' % (index, Path(source).name, error)) from error
    item['inputIndex'] = index
    files.append(item)
summary = dict(files[0]) if len(files) == 1 else {'preflightVersion': 3, 'limitations': ['File roles, join keys and unmatched/duplicate records must be assessed before combining data.']}
summary.update({'files': files, 'fileCount': len(files)})
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
summary
`
