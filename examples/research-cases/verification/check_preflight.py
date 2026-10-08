"""Replay the currently checked-in/working-tree CSV preflight, without the app DB."""
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[1]
go_path = REPO / "internal/app/workflow/research_preflight.go"
source = go_path.read_text(encoding="utf-8")
match = re.search(r"const dynamicDataPreflightCode = `([\s\S]*?)`", source)
if not match:
    raise RuntimeError("Current CSV preflight constant not found")
out = REPO / ".tmp/research-cases-preparation"
out.mkdir(parents=True, exist_ok=True)
result = []
for label, relative, expected in [
    ("initial", "02-data/materials/initial/wine-red-initial-1000.csv", 1000),
    ("full", "02-data/materials/followup/wine-red-full-1599.csv", 1599),
]:
    destination = out / f"preflight-{label}.json"
    scope = {"SCIAIDE_INPUTS": [str(ROOT / relative)], "SCIAIDE_OUTPUTS": [str(destination)]}
    exec(compile(match[1], str(go_path) + ":dynamicDataPreflightCode", "exec"), scope)
    summary = json.loads(destination.read_text(encoding="utf-8"))
    assert summary["rowCount"] == expected and summary["columnCount"] == 14
    assert summary["missingCellCount"] == 0 and summary["shapeCheckedAllRows"]
    result.append({"input": relative, "rows": summary["rowCount"], "columns": summary["columnCount"],
                   "missing_cells": summary["missingCellCount"], "shape_checked_all_rows": True})
print(json.dumps(result, ensure_ascii=False, indent=2))
