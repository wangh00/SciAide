"""Offline descriptive analysis of SciAide's bounded literature capture.

No network, model, private DB, fuzzy identity merge or scientific approval.
The experiment's frozen plan is verified before counterfactual subsets are used.
"""
from __future__ import annotations
import argparse
from collections import Counter, defaultdict
from datetime import datetime
import hashlib
import json
from pathlib import Path
import re
import tempfile


def identity(work: dict) -> str:
    """Conservative bibliographic grouping, NOT independent-study identity."""
    ids = work.get("identifiers") or {}
    for name in ("doi", "pmid", "pmcid", "arxiv"):
        value = str(ids.get(name) or "").strip().casefold()
        if value:
            return f"{name}:{value}"
    return f"source:{work['sourceId']}:{work['sourceRecordId']}"


def text_for_screen(work: dict) -> str:
    m = work.get("sourceMetadata") or {}
    fields = [work.get("title", ""), work.get("abstract", "")]
    fields.extend(m.get("subjects") or [])
    fields.extend(m.get("educationLevels") or [])
    return " ".join(fields).casefold().replace("–", "-").replace("—", "-")


def screen(case_id: str, work: dict) -> dict:
    """Transparent lexical proxy. Missing detail never becomes ineligibility."""
    text = text_for_screen(work)
    def has(pattern):
        return bool(re.search(pattern, text))
    if case_id == "education_writing":
        flags = {
            "ai": has(r"\b(ai|chatgpt|gpt|artificial intelligence|generative ai|automated writing|automated feedback|grammarly|awe)\b"),
            "writing": has(r"\b(writing|written|essay|essays)\b"),
            "feedback": has(r"\b(feedback|corrective|assessment|evaluation|revisions?)\b"),
            "population": has(r"\b(undergraduates?|universit\w*|college|tertiary|higher education|postgraduates?|graduate students)\b"),
        }
        topic = flags["ai"] and flags["writing"] and flags["feedback"]
    elif case_id == "biomedical_insomnia":
        flags = {
            "insomnia": has(r"\binsomnia\b"),
            "therapy": has(r"cognitive[ -]behavio[u]?r(?:al)?|\bcbt[ -]?i\b"),
            "chronic": has(r"\b(chronic|persistent)\b"),
            "population": has(r"\b(adults?|middle[ -]aged|older adults?)\b"),
        }
        topic = flags["insomnia"] and flags["therapy"] and flags["chronic"]
    else:
        raise ValueError("unknown case")
    label = ("scope_keyword_match" if flags["population"] else "topic_match_population_unverified") if topic else "not_demonstrated_by_keywords"
    return {"proxyLabel": label, "flags": flags, "hasAbstract": bool(str(work.get("abstract") or "").strip())}


def record_view(case_id: str, work: dict, years: dict) -> dict:
    year = work.get("year") or 0
    year_state = "in_window" if years["from"] <= year <= years["to"] else "outside_window" if year else "unknown"
    if work["sourceId"] == "arxiv":
        year_state = "submission_year_only"
    return {
        "key": identity(work), "sourceId": work["sourceId"], "sourceRecordId": work["sourceRecordId"],
        "title": work["title"], "year": year, "yearState": year_state,
        "doi": (work.get("identifiers") or {}).get("doi", ""),
        "landingUrl": work.get("landingUrl", ""), "pdfLead": bool(work.get("pdfUrl") or work.get("pdfUrls")),
        "screen": screen(case_id, work), "abstract": work.get("abstract", ""),
        "educationLevels": (work.get("sourceMetadata") or {}).get("educationLevels", []),
    }


def summarize(case_id: str, rows: list, spec: dict) -> tuple[dict, dict]:
    by_key = defaultdict(list)
    status = Counter()
    failures = Counter()
    abstracts = set()
    native_year = Counter()
    raw = 0
    source_records = set()
    for row in rows:
        if row.get("aborted"):
            status["aborted"] += 1
        for s in row["search"].get("sources", []):
            status[s["status"]] += 1
            if s["status"] == "failed":
                failures[s.get("errorCode", "unknown")] += 1
            native_year["applied" if s.get("yearFilterApplied") else "not_applied"] += 1
        for work in row["search"].get("works", []):
            if work["sourceId"] != row["slot"]["sourceId"]:
                raise ValueError("source escaped frozen slot")
            raw += 1
            source_records.add((work["sourceId"], work["sourceRecordId"]))
            v = record_view(case_id, work, spec["publicationYears"])
            by_key[v["key"]].append(v)
    labels = Counter()
    year_counts = Counter()
    pdf = set()
    for key, versions in by_key.items():
        proxy_labels = {v["screen"]["proxyLabel"] for v in versions}
        best = next((v for v in ("scope_keyword_match", "topic_match_population_unverified") if v in proxy_labels), "not_demonstrated_by_keywords")
        labels[best] += 1
        if any(v["screen"]["hasAbstract"] for v in versions):
            abstracts.add(key)
        if any(v["pdfLead"] for v in versions):
            pdf.add(key)
        states = {v["yearState"] for v in versions}
        state = next((v for v in ("in_window", "outside_window", "unknown", "submission_year_only") if v in states), "unknown")
        year_counts[state] += 1
    # A warning only; do not silently merge duplicate titles or infer trial IDs.
    title_keys = defaultdict(set)
    for key, versions in by_key.items():
        for v in versions:
            title = " ".join(re.findall(r"\w+", v["title"].casefold()))
            title_keys[(title, v["year"])].add(key)
    summary = {
        "reservedHttp": sum(r["slot"]["reservedHttp"] for r in rows),
        "observedHttpInFrozenPool": sum(r["actualHttp"] for r in rows),
        "sourceQueryCalls": len(rows), "sourceQueryStatus": dict(status), "failureCodes": dict(failures),
        "rawReturnedRecords": raw, "uniqueSourceRecords": len(source_records), "bibliographicGroups": len(by_key),
        "groupsWithAbstract": len(abstracts), "unverifiedPdfLeadGroups": len(pdf),
        "keywordProxyGroups": dict(labels), "yearStateGroups": dict(year_counts), "nativeYearFilterQueries": dict(native_year),
        "possibleExactTitleYearDuplicateClusters": sum(len(v) > 1 for v in title_keys.values()),
        "keys": sorted(by_key),
    }
    return summary, by_key


def analyze(directory: Path) -> dict:
    plan_bytes = (directory / "plan.json").read_bytes()
    plan = json.loads(plan_bytes)
    capture = json.loads((directory / "capture.json").read_bytes())
    if plan["version"] not in ("literature-equal-http-v1", "literature-equal-http-v2") or capture["specSha256"] != hashlib.sha256(plan_bytes).hexdigest():
        raise ValueError("capture does not match the frozen experiment")
    if not capture["complete"] or capture["budgetDenied"] or capture["modelHttp"] != 0 or capture["actualHttp"] > plan["maxHttp"]:
        raise ValueError("incomplete or over-budget capture; do not report a completed comparison")
    started, ended = (datetime.fromisoformat(capture[k].replace("Z", "+00:00")) for k in ("startedAt", "endedAt"))
    if started.utcoffset() is None or ended.utcoffset() is None or not 0 <= (ended - started).total_seconds() <= plan["maxSeconds"]:
        raise ValueError("capture is outside its declared time window")
    expected = {}
    for question in plan["cases"]:
        for slot in plan["sharedPool"]:
            key = (question["id"], slot["sourceId"], slot["queryIndex"])
            if key in expected or not 0 <= slot["queryIndex"] < len(question["queries"]):
                raise ValueError("invalid frozen query pool")
            cost = 2 if slot["sourceId"] == "pubmed" else 1
            if slot["reservedHttp"] != cost:
                raise ValueError("frozen slot has an incorrect physical HTTP reservation")
            expected[key] = slot
    if sum(s["reservedHttp"] for s in expected.values()) > plan["maxHttp"]:
        raise ValueError("frozen pool exceeds its HTTP ceiling")
    row_map = {}
    for row in capture["rows"]:
        key = (row["caseId"], row["slot"]["sourceId"], row["slot"]["queryIndex"])
        if key not in expected or row["slot"] != expected[key]:
            raise ValueError("query slot differs from the frozen pool")
        if key in row_map or type(row["actualHttp"]) is not int or not 0 <= row["actualHttp"] <= row["slot"]["reservedHttp"]:
            raise ValueError("duplicate or over-budget query slot")
        row_map[key] = row
    if set(row_map) != set(expected):
        raise ValueError("capture does not contain the complete frozen query pool")
    if len(capture["http"]) != capture["actualHttp"] or sum(r["actualHttp"] for r in capture["rows"]) != capture["actualHttp"]:
        raise ValueError("HTTP accounting does not reconcile")
    trace_counts = Counter()
    for ordinal, trace in enumerate(capture["http"], 1):
        key = (trace["caseId"], trace["sourceId"], trace["queryIndex"])
        if trace["ordinal"] != ordinal or key not in row_map:
            raise ValueError("HTTP trace identity differs from the frozen pool")
        trace_counts[key] += 1
        if trace.get("captureWriteFailed"):
            raise ValueError("response capture missing")
        if not trace.get("bodyFile") and not trace.get("transportFailed"):
            raise ValueError("non-transport response has no preserved body")
        if trace.get("bodyFile"):
            name = trace["bodyFile"]
            if not isinstance(name, str) or not re.fullmatch(r"http-[0-9]{3,}\.raw", name) or name != f"http-{ordinal:03d}.raw":
                raise ValueError("unsafe response-capture path")
            target = (directory / name).resolve()
            if not target.is_relative_to(directory.resolve()):
                raise ValueError("response capture escaped its directory")
            data = target.read_bytes()
            if len(data) != trace["capturedBytes"] or hashlib.sha256(data).hexdigest() != trace["capturedSha256"]:
                raise ValueError("raw response hash mismatch")
    if any(trace_counts[key] != row["actualHttp"] for key, row in row_map.items()):
        raise ValueError("per-query HTTP accounting does not reconcile")
    cases = []
    for question in plan["cases"]:
        summaries, inventories = {}, {}
        for strategy in plan["strategies"]:
            keys = [(question["id"], s["sourceId"], s["queryIndex"]) for s in strategy["slots"]]
            if len(set(keys)) != len(keys) or any(expected.get(k) != s for k, s in zip(keys, strategy["slots"])):
                raise ValueError("strategy differs from the frozen query pool")
            rows = [row_map[(question["id"], s["sourceId"], s["queryIndex"])] for s in strategy["slots"]]
            summary, inventory = summarize(question["id"], rows, plan)
            if summary["reservedHttp"] != plan["strategyHttpCeiling"] or summary["observedHttpInFrozenPool"] > plan["strategyHttpCeiling"]:
                raise ValueError("strategies have unequal HTTP ceilings")
            summaries[strategy["id"]], inventories[strategy["id"]] = summary, inventory
        deltas = {}
        previous = "baseline_six"
        for current in ("plus_eric", "plus_eric_core"):
            before, after = inventories[previous], inventories[current]
            added, lost = set(after) - set(before), set(before) - set(after)
            deltas[f"{previous}_to_{current}"] = {
                "addedGroups": len(added), "lostGroups": len(lost), "retainedGroups": len(set(before) & set(after)),
                "added": {k: after[k] for k in sorted(added)}, "lost": {k: before[k] for k in sorted(lost)},
            }
            previous = current
        per_source = {}
        for source in sorted({s["sourceId"] for s in plan["sharedPool"]}):
            rows = [r for r in capture["rows"] if r["caseId"] == question["id"] and r["slot"]["sourceId"] == source]
            summary, _ = summarize(question["id"], rows, plan)
            per_source[source] = summary
        cases.append({"id": question["id"], "question": question["question"], "strategies": summaries, "deltas": deltas, "perSource": per_source})
    return {
        "version": "literature-descriptive-analysis-v2", "specSha256": capture["specSha256"],
        "httpAccounting": "non_replaying_http1" if plan["version"] == "literature-equal-http-v2" else "legacy_outer_roundtrip_only_unverified_wire_ceiling",
        "startedAt": capture["startedAt"], "endedAt": capture["endedAt"],
        "actualHttp": capture["actualHttp"] if plan["version"] == "literature-equal-http-v2" else None,
        "observedTransportAttempts": capture["actualHttp"], "modelHttp": 0, "cases": cases,
        "responseCaptureFlags": {flag: sum(bool(t.get(flag)) for t in capture["http"]) for flag in (
            "transportFailed", "capturedPrefixOnly", "bodyReadFailed", "bodyCloseFailed")},
        "limits": [
            "Two prespecified examples, first five results per query, single attempt, no enrichment, no PDF validation, no gold relevance set.",
            "Counterfactual subsets share captured responses. Observed HTTP is not standalone timing, monetary cost or quota-unit equivalence.",
            "Identifier-priority groups are conservative bibliographic groups, not independent studies; missing-ID/title duplicates remain unresolved.",
            "Keyword labels are lexical screening proxies, not scientific eligibility, precision, recall or effect judgments.",
            "arXiv year is separately marked as submission-only; failed searches are not empty successful results.",
            "Legacy v1 captures count outer RoundTrip calls; transport-internal replays were not instrumented, so do not claim a verified wire-level ceiling or exact HTTP total from v1.",
        ],
    }


def self_test() -> None:
    sample = {"sourceId": "eric", "sourceRecordId": "EJ1", "title": "AI feedback for university writing", "year": 2025, "abstract": "", "identifiers": {"doi": "10.1/EXACT"}}
    assert identity(sample) == "doi:10.1/exact"
    assert screen("education_writing", sample)["proxyLabel"] == "scope_keyword_match"
    sample["title"] = "AI feedback for writing"
    assert screen("education_writing", sample)["proxyLabel"] == "topic_match_population_unverified"
    sample["sourceId"] = "arxiv"
    assert record_view("education_writing", sample, {"from": 2022, "to": 2026})["yearState"] == "submission_year_only"
    rows = [{"actualHttp": 0, "slot": {"sourceId": "eric", "reservedHttp": 1}, "search": {"works": [], "sources": [{"status": "failed", "errorCode": "rate_limited"}]}}]
    summary, inventory = summarize("education_writing", rows, {"publicationYears": {"from": 2022, "to": 2026}})
    assert summary["sourceQueryStatus"] == {"failed": 1} and summary["failureCodes"] == {"rate_limited": 1} and not inventory
    # Small, entirely local capture: exercise evidence guards as well as labels.
    sample["sourceId"] = "eric"
    slot = {"sourceId": "eric", "queryIndex": 0, "reservedHttp": 1}
    plan = {
        "version": "literature-equal-http-v1", "maxHttp": 1, "maxSeconds": 60, "strategyHttpCeiling": 1,
        "publicationYears": {"from": 2022, "to": 2026}, "sharedPool": [slot],
        "cases": [{"id": "education_writing", "question": "fixture", "queries": ["fixture"]}],
        "strategies": [{"id": name, "slots": [slot]} for name in ("baseline_six", "plus_eric", "plus_eric_core")],
    }
    body = b'{"public":"fixture"}'
    capture = {
        "specSha256": hashlib.sha256(json.dumps(plan).encode()).hexdigest(), "complete": True, "budgetDenied": 0,
        "modelHttp": 0, "actualHttp": 1, "startedAt": "2026-11-04T00:00:00Z", "endedAt": "2026-11-04T00:00:01Z",
        "rows": [{"caseId": "education_writing", "slot": slot, "actualHttp": 1,
                  "search": {"works": [sample], "sources": [{"status": "ok", "count": 1}]}}],
        "http": [{"ordinal": 1, "caseId": "education_writing", "sourceId": "eric", "queryIndex": 0,
                  "bodyFile": "http-001.raw", "capturedBytes": len(body), "capturedSha256": hashlib.sha256(body).hexdigest()}],
    }
    with tempfile.TemporaryDirectory(prefix="sciaide-literature-analysis-") as temp:
        directory = Path(temp)
        (directory / "plan.json").write_text(json.dumps(plan), encoding="utf-8")
        (directory / "http-001.raw").write_bytes(body)
        def check(mutator=None, expected_error=None):
            candidate = json.loads(json.dumps(capture))
            if mutator:
                mutator(candidate)
            (directory / "capture.json").write_text(json.dumps(candidate), encoding="utf-8")
            try:
                result = analyze(directory)
            except ValueError as error:
                if expected_error is None or expected_error not in str(error):
                    raise
            else:
                assert expected_error is None, expected_error
                assert result["cases"][0]["strategies"]["baseline_six"]["bibliographicGroups"] == 1
                assert result["actualHttp"] == (None if plan["version"] == "literature-equal-http-v1" else 1)
                assert result["observedTransportAttempts"] == 1
        check()
        check(lambda c: c.update(specSha256="bad"), "frozen experiment")
        check(lambda c: c.update(modelHttp=1), "incomplete or over-budget")
        check(lambda c: c.update(endedAt="2026-11-04T00:02:00Z"), "time window")
        check(lambda c: c["rows"].append(c["rows"][0]), "duplicate or over-budget")
        check(lambda c: c.update(rows=[]), "complete frozen query pool")
        check(lambda c: c["rows"][0].update(actualHttp=-1), "duplicate or over-budget")
        check(lambda c: c["rows"][0]["slot"].update(reservedHttp=2), "differs from the frozen pool")
        check(lambda c: c["http"][0].update(queryIndex=1), "trace identity")
        check(lambda c: c["http"][0].update(ordinal=2), "trace identity")
        check(lambda c: c["http"][0].update(bodyFile="../http-001.raw"), "unsafe response-capture path")
        check(lambda c: c["http"][0].update(capturedSha256="bad"), "raw response hash mismatch")
        (directory / "http-001.raw").write_bytes(body + b"tampered")
        check(expected_error="raw response hash mismatch")
        (directory / "http-001.raw").write_bytes(body)
        plan["version"] = "literature-equal-http-v2"
        plan_bytes = json.dumps(plan).encode()
        (directory / "plan.json").write_bytes(plan_bytes)
        capture["specSha256"] = hashlib.sha256(plan_bytes).hexdigest()
        check()
    print("offline literature-comparison self-test passed")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--capture-dir", type=Path)
    parser.add_argument("--out", type=Path)
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return
    if not args.capture_dir or not args.out:
        parser.error("--capture-dir and --out are required; output must not exist")
    result = analyze(args.capture_dir)
    with args.out.open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(result, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    for case in result["cases"]:
        for strategy, data in case["strategies"].items():
            print(case["id"], strategy, "http", data["observedHttpInFrozenPool"], "/", data["reservedHttp"], "groups", data["bibliographicGroups"], "abstracts", data["groupsWithAbstract"], "pdf-leads", data["unverifiedPdfLeadGroups"], "status", data["sourceQueryStatus"], "proxy", data["keywordProxyGroups"])
    print("Captured transport attempts:", result["observedTransportAttempts"], "; model HTTP: 0; accounting:", result["httpAccounting"])


if __name__ == "__main__":
    main()
