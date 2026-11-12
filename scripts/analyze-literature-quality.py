"""Bounded source-record quality pilot. Labels are authored, never inferred by regex."""
import argparse
from collections import Counter
import datetime as dt
import hashlib
import json
from pathlib import Path
import tempfile
from urllib.parse import parse_qs, urlencode


def sha(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def load(path):
    return json.loads(path.read_text(encoding="utf-8-sig"), parse_constant=lambda _: (_ for _ in ()).throw(ValueError("non-finite JSON")))


def save_new(path, value):
    with path.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def has_dispatch_evidence(spec):
    return spec["version"] in ("literature-quality-pilot-v2", "literature-quality-pilot-v3")


def read_capture(directory):
    directory = directory.resolve()
    spec_bytes = (directory / "plan.json").read_bytes()
    spec, capture = load(directory / "plan.json"), load(directory / "capture.json")
    require(spec["version"] in ("literature-quality-pilot-v1", "literature-quality-pilot-v2", "literature-quality-pilot-v3"), "unsupported pilot version")
    expected_cases, expected_http = (4, 24) if spec["version"] == "literature-quality-pilot-v3" else (2, 12)
    require(spec["maxHttp"] == expected_http and spec["maxSeconds"] == 480 and spec["limitPerSourceQuery"] == 5, "pilot bounds changed")
    require(spec["sources"] == ["eric", "openalex", "crossref"] and spec["arms"] == ["baseline", "population_focus"], "pilot sources/arms changed")
    require(capture["specSha256"] == sha(spec_bytes), "frozen plan hash mismatch")
    require(capture["modelHttp"] == 0 and capture["complete"] is True and capture["budgetDenied"] == 0, "incomplete/denied pilot; retain capture, no quality success claim")
    start, end = [dt.datetime.fromisoformat(capture[k].replace("Z", "+00:00")) for k in ("startedAt", "endedAt")]
    require(start.tzinfo is not None and end.tzinfo is not None and 0 <= (end-start).total_seconds() <= spec["maxSeconds"] + 1, "invalid capture time window")
    require(len(capture["http"]) == capture["actualHttp"] <= spec["maxHttp"], "HTTP ceiling/count mismatch")
    cases = {c["id"]: c for c in spec["cases"]}
    expected = {(c, a, s) for c in cases for a in spec["arms"] for s in spec["sources"]}
    require(len(cases) == expected_cases and len(expected) == expected_http, "pilot case count changed")
    seen, by_slot, traces_by_slot = set(), Counter(), {}
    for ordinal, trace in enumerate(capture["http"], 1):
        require(trace["ordinal"] == ordinal, "HTTP ordinals not contiguous")
        index = trace["queryIndex"]
        require(type(index) is int and index in (0, 1), "invalid trace arm index")
        slot = (trace["caseId"], spec["arms"][index], trace["sourceId"])
        require(slot in expected, "unexpected HTTP slot")
        by_slot[slot] += 1
        traces_by_slot[slot] = trace
        host = {"eric": "https://api.ies.ed.gov/eric/", "openalex": "https://api.openalex.org/works", "crossref": "https://api.crossref.org/works"}[trace["sourceId"]]
        require(trace["endpoint"] == host, "HTTP endpoint escaped fixed source")
        require(not trace.get("captureWriteFailed"), "HTTP capture write failed")
        name = trace.get("bodyFile")
        if name:
            require(name == f"http-{ordinal:03}.raw", "raw body is not uniquely bound to HTTP ordinal")
            path = (directory / name).resolve()
            require(path.parent == directory, "raw body escaped capture directory")
            with path.open("rb") as stream:
                body = stream.read((8 << 20) + 1)
            require(len(body) == trace["capturedBytes"] <= 8 << 20 and sha(body) == trace["capturedSha256"], "raw body hash/size mismatch")
        else:
            require(trace.get("transportFailed") is True and trace.get("capturedBytes", 0) == 0, "missing HTTP body provenance")
    for row in capture["rows"]:
        slot = (row["caseId"], row["arm"], row["sourceId"])
        require(slot in expected and slot not in seen, "duplicate/unexpected result slot")
        seen.add(slot)
        require(row["actualHttp"] == by_slot[slot] and row["actualHttp"] <= 1, "row HTTP count mismatch")
        if row.get("aborted"):
            require(not row["search"].get("works") and not row["search"].get("sources"), "aborted result retained successful source/records")
            continue
        result = row["search"]
        require(result["query"] == cases[row["caseId"]]["logicalQuery"], "logical query mismatch")
        require(len(result["sources"]) == 1 and result["sources"][0]["sourceId"] == row["sourceId"], "source audit mismatch")
        status = result["sources"][0]
        inp = cases[row["caseId"]]["focusedProviderQueries"][row["sourceId"]] if row["arm"] == "population_focus" else result["query"]
        require(status.get("queryProjectionVersion") == "source-query-v1" and status.get("logicalQuery") == result["query"] and status.get("inputQuery") == inp, "query provenance mismatch")
        require(status.get("publicationYears") == spec["publicationYears"], "requested publication years changed")
        require(status["status"] in ("ok", "empty", "failed"), "unknown source status")
        if status["status"] in ("ok", "empty"):
            trace = traces_by_slot.get(slot, {})
            require(row["actualHttp"] == 1 and trace.get("status") == 200 and trace.get("bodyFile") and not any(trace.get(k) for k in ("transportFailed", "capturedPrefixOnly", "bodyReadFailed", "bodyCloseFailed")), "successful fresh slot lacks complete HTTP/raw evidence")
        works = result["works"]
        require(len(works) <= 5 and status["count"] == len(works), "source record count mismatch")
        require((status["status"] == "ok") == bool(works), "failed/empty source fabricated records")
        ids = [w["sourceRecordId"] for w in works]
        require(len(ids) == len(set(ids)) and all(w["sourceId"] == row["sourceId"] for w in works), "duplicate/cross-source record")
    require(seen == expected and sum(r["actualHttp"] for r in capture["rows"]) == capture["actualHttp"], "missing slot/HTTP attribution")
    if has_dispatch_evidence(spec):
        validate_dispatch_v2(spec, capture, directory)
    return spec, capture


QUERY_AUDIT_FIELDS = ("sourceId", "publicationYears", "yearFilterApplied", "offset", "effectiveQuery", "providerQuery", "queryMode", "inputQuery", "logicalQuery", "queryOrigin", "queryProjectionVersion", "queryLimitations")


def query_audit(source):
    return {key: source[key] for key in QUERY_AUDIT_FIELDS if key in source}


def validate_request(request, source, status, spec):
    keys = {"eric": {"search", "format", "start", "rows", "fields"}, "openalex": {"search", "per-page", "sort", "filter"}, "crossref": {"query.bibliographic", "rows", "sort", "filter"}}[source]
    endpoint = {"eric": "https://api.ies.ed.gov/eric/", "openalex": "https://api.openalex.org/works", "crossref": "https://api.crossref.org/works"}[source]
    require(set(request) == {"method", "endpoint", "encodedQuery", "parameters"} and request["method"] == "GET" and request["endpoint"] == endpoint, "invalid dispatch request envelope")
    params, encoded = request["parameters"], request["encodedQuery"]
    require(isinstance(encoded, str) and len(encoded) <= 32 << 10 and isinstance(params, dict) and set(params) == keys and all(isinstance(values, list) and len(values) == 1 and isinstance(values[0], str) and values[0] for values in params.values()), "dispatch query contains missing/unknown/duplicated parameter")
    require(parse_qs(encoded, keep_blank_values=True, strict_parsing=True, errors="strict") == params, "encoded query and recorded parameters disagree")
    years, limit = spec["publicationYears"], str(spec["limitPerSourceQuery"])
    require(status.get("publicationYears") == years and status.get("yearFilterApplied") is True, "dispatch year audit changed")
    effective = status["effectiveQuery"]
    provider = f'({effective}) AND publicationdateyear:[{years["from"]} TO {years["to"]}]' if source == "eric" else effective
    require(status["providerQuery"] == provider, "compiled provider/year query changed")
    if source == "eric":
        want = {"search": [provider], "rows": [limit], "start": ["0"], "format": ["json"], "fields": params["fields"]}
    elif source == "openalex":
        want = {"search": [provider], "per-page": [limit], "sort": ["relevance_score:desc"], "filter": [f'from_publication_date:{years["from"]}-01-01,to_publication_date:{years["to"]}-12-31']}
    else:
        want = {"query.bibliographic": [provider], "rows": [limit], "sort": ["relevance"], "filter": [f'from-pub-date:{years["from"]}-01-01,until-pub-date:{years["to"]}-12-31']}
    require(params == want, "dispatch parameters differ from source query, years or first-page contract")


def validate_dispatch_v2(spec, capture, directory):
    require(spec.get("requestEvidenceVersion") == capture.get("captureVersion") == "quality-dispatch-v2", "v2 request evidence missing; never upgrade v1 traces")
    require(capture.get("evidenceMode") in ("public_api", "controlled_tls_fixture") and capture.get("guardRejected") == 0, "unknown capture mode or request guard rejection")
    preflight_bytes = (directory/"preflight.json").read_bytes()
    require(capture.get("preflightSha256") == sha(preflight_bytes), "preflight hash mismatch")
    preflight = load(directory/"preflight.json")
    planned = [(case["id"], spec["arms"][(oi+ci+si)%2], source) for ci, case in enumerate(spec["cases"]) for si, source in enumerate(spec["sources"]) for oi in range(2)]
    require([(r["caseId"], r["arm"], r["sourceId"]) for r in capture["rows"]] == planned, "v2 result slots differ from frozen dispatch order")
    dispatched = [(r["caseId"], r["arm"], r["sourceId"]) for r in capture["rows"] if r["actualHttp"] == 1]
    require([(t["caseId"], spec["arms"][t["queryIndex"]], t["sourceId"]) for t in capture["http"]] == dispatched, "v2 actual dispatch order differs from attempted result slots")
    rows = {f'{r["caseId"]}:{r["arm"]}:{r["sourceId"]}': r for r in capture["rows"]}
    require(set(preflight) == set(rows), "preflight slots missing/duplicated")
    for case in spec["cases"]:
        for source in spec["sources"]:
            entries = [preflight[f'{case["id"]}:{arm}:{source}'] for arm in spec["arms"]]
            for arm, entry in zip(spec["arms"], entries):
                validate_request(entry["request"], source, entry["source"], spec)
                row = rows[f'{case["id"]}:{arm}:{source}']
                if not row.get("aborted"):
                    require(query_audit(entry["source"]) == query_audit(row["search"]["sources"][0]), "captured source audit differs from preflight")
            a, b = entries
            suffix = spec.get("populationSuffixes", {}).get(source)
            require(isinstance(suffix, str) and suffix and b["source"]["effectiveQuery"] == a["source"]["effectiveQuery"] + suffix, "post-projection pair is not population-suffix-only")
            query_key = "query.bibliographic" if source == "crossref" else "search"
            require({k: v for k, v in a["request"]["parameters"].items() if k != query_key} == {k: v for k, v in b["request"]["parameters"].items() if k != query_key}, "non-query parameter differs between arms")
    for trace in capture["http"]:
        key = f'{trace["caseId"]}:{spec["arms"][trace["queryIndex"]]}:{trace["sourceId"]}'
        require(trace.get("request") == preflight[key]["request"], "actual dispatch differs from preflight request")
        validate_request(trace["request"], trace["sourceId"], preflight[key]["source"], spec)


VIEW_FIELDS = ("sourceId", "sourceRecordId", "title", "abstract", "authors", "year", "publicationYears", "workType", "sourceMetadata", "identifiers", "venue")


def record_view(work):
    return {k: work[k] for k in VIEW_FIELDS if k in work}


def validate_replay(replay, spec, capture, directory, diagnostic_partial=False):
    """Current production-code replay, never invented historical wire evidence."""
    kind = "current_service_partial_diagnostic_no_network" if diagnostic_partial else "current_service_raw_replay_no_network"
    require(replay.get("version") == "literature-quality-replay-v1" and replay.get("kind") == kind, "unsupported local replay or partial diagnostic supplied to strict mode")
    if diagnostic_partial:
        require(replay.get("diagnosticPartial") is True and has_dispatch_evidence(spec), "partial diagnostic requires explicit dispatch-evidence capture")
    require(replay.get("protocolSha256") == capture["specSha256"] and replay.get("captureSha256") == sha((directory/"capture.json").read_bytes()), "replay bound to another capture/protocol")
    require(replay.get("networkHttp") == replay.get("modelHttp") == 0, "replay is not offline")
    rows = {(r["caseId"], r["arm"], r["sourceId"]): r for r in capture["rows"]}
    traces = {(t["caseId"], spec["arms"][t["queryIndex"]], t["sourceId"]): t for t in capture["http"]}
    prepared = None
    if diagnostic_partial:
        require(sha((directory/"preflight.json").read_bytes()) == capture.get("preflightSha256"), "diagnostic preflight hash mismatch")
        prepared = load(directory/"preflight.json")
    seen, records, failed_slots = set(), 0, 0
    for entry in replay["entries"]:
        slot = (entry["caseId"], entry["arm"], entry["sourceId"])
        require(slot in rows and slot not in seen, "duplicate/unexpected replay slot")
        seen.add(slot)
        row, trace = rows[slot], traces.get(slot, {})
        require(not row.get("aborted"), "aborted source is not a diagnostic replay candidate")
        status = row["search"]["sources"][0]
        if diagnostic_partial and status["status"] == "failed":
            require(not row["search"].get("works") and entry.get("disposition") == "failed_not_replayed" and "search" not in entry and "request" not in entry, "failed source fabricated successful replay or dispatch")
            require(query_audit(entry["projectionAudit"]) == query_audit(status), "failed-slot current projection differs from captured audit")
            require(entry["projectionRequest"] == prepared[":".join(slot)]["request"], "failed-slot current request projection differs from frozen preflight")
            require(entry.get("bodyFile", "") == trace.get("bodyFile", "") and entry.get("bodySha256", "") == trace.get("capturedSha256", ""), "failed diagnostic raw association mismatch")
            if trace:
                require(entry["projectionRequest"] == trace["request"], "failed attempt's projection differs from actual dispatch")
            else:
                validate_request(entry["projectionRequest"], row["sourceId"], entry["projectionAudit"], spec)
            failed_slots += 1
            continue
        require(status["status"] in ("ok", "empty"), "failed source cannot be rewritten as a successful raw replay")
        require(entry.get("bodyFile") == trace.get("bodyFile") and entry.get("bodySha256") == trace.get("capturedSha256"), "replay raw evidence association mismatch")
        result, projected = row["search"], entry["search"]
        require(result["query"] == projected["query"] and result.get("partial", False) == projected.get("partial", False), "local Service result mismatch")
        require(result["sources"] == projected["sources"], "source query/audit differs from current production replay")
        if has_dispatch_evidence(spec):
            require(entry.get("request") == trace.get("request"), "current adapter request differs from captured dispatch evidence")
        actual, expected = [[record_view(w) for w in r["works"]] for r in (result, projected)]
        require(actual == expected, "source records differ from same-slot raw production replay")
        records += len(actual)
    require(seen == set(rows), "missing replay slot")
    meaning = "Current Service/adapter reconstruction matches frozen query audits and annotated metadata from each slot's hashed raw body. Original trace has endpoint only: original wire query/year parameters were NOT independently captured or verified."
    if has_dispatch_evidence(spec):
        meaning = "Current Service/adapter reconstruction matches frozen audits, same-slot hashed raw metadata and separately recorded client dispatch parameters. Does not prove provider receipt/filter semantics, nor turn a controlled TLS fixture into a public API experiment."
    result = {"kind": replay["kind"], "slots": len(seen), "sourceRecords": records, "boundMetadataFields": list(VIEW_FIELDS), "networkHttp": 0, "modelHttp": 0, "meaning": meaning}
    if diagnostic_partial:
        require(failed_slots == replay.get("failedSlotsNotReplayed"), "failed diagnostic count changed")
        result.update(diagnosticPartial=True, failedSlotsNotReplayed=failed_slots, successfulSlotsReplayed=len(seen)-failed_slots, meaning="Partial failure diagnostic only. Successful raw metadata replayed; failure statuses retained, not replayed as empty results. Failed slots only receive current-code projection checks; no full quality acceptance.")
    return result


def mark_partial_diagnostic(result, capture):
    result.update(diagnosticPartial=True, status="partial_failure_diagnostic_not_quality_acceptance", strictQualityReportEligible=False)
    failed = [r for r in capture["rows"] if r.get("aborted") or not r["search"].get("sources") or r["search"]["sources"][0]["status"] == "failed"]
    result["failedSourceSlots"] = len(failed)
    result["failedWithoutHttp"] = sum(r["actualHttp"] == 0 for r in failed)
    for slot in result["perSourceSlots"]:
        if slot["status"] in ("failed", "aborted"):
            for key in ("sourceRecords", "labels", "relevantEvidenceTypes", "relevantWithoutAbstract", "missingAbstracts", "metadataFractionLower", "metadataFractionUpper"):
                slot[key] = None
    for anchor in result["predeclaredAnchorProbes"]:
        anchor["absenceMeaning"] = "Not observed in successful captured first pages only; failed/unattempted source slots remain unknown, not database absence or recall. New topics may have no prespecified anchors."
    result["limitations"].insert(0, "PARTIAL FAILURE DIAGNOSTIC: strict quality report not eligible; successful-source observations do not compensate for failed sources. Failed-slot counts/labels and failed-pair gains/losses are unknown, never zero literature.")
    return result


def annotation_id(case_id, work):
    return sha(canonical({"caseId": case_id, "metadata": record_view(work)}))


def screening_pack(spec, capture, directory):
    records = {}
    for row in capture["rows"]:
        for work in row["search"].get("works") or []:
            key = annotation_id(row["caseId"], work)
            records[key] = {"id": key, "caseId": row["caseId"], "metadata": record_view(work)}
    pack = {"version": "literature-quality-screening-v1", "protocolSha256": capture["specSha256"], "captureSha256": sha((directory / "capture.json").read_bytes()), "publicationYears": spec["publicationYears"], "rules": spec["screening"], "questions": [{"id": c["id"], "question": c["question"]} for c in spec["cases"]], "records": [records[k] for k in sorted(records)]}
    if has_dispatch_evidence(spec):
        pack["evidenceMode"] = capture["evidenceMode"]
    return pack


def validate_labels(labels, pack):
    require(labels.get("version") == "literature-quality-labels-v1" and labels.get("reviewer") == "agent_metadata_review", "label provenance missing")
    require(labels.get("protocolSha256") == pack["protocolSha256"] and labels.get("captureSha256") == pack["captureSha256"] and labels.get("screeningSha256") == sha(canonical(pack)), "labels bound to another protocol/capture/pack")
    records = {r["id"]: r for r in pack["records"]}
    by_id = {}
    for label in labels["labels"]:
        key = label["id"]
        require(key in records and key not in by_id, "unknown/duplicate annotation ID")
        require(label["label"] in ("metadata_relevant", "out_of_scope", "uncertain"), "invalid relevance label")
        require(label["population"] in ("yes", "no", "unclear") and label["intervention"] in ("yes", "no", "unclear"), "missing population/intervention judgment")
        require(label["yearScope"] in ("yes", "no", "unclear"), "missing publication-year judgment")
        metadata = records[key]["metadata"]
        known_years = {y for y in [metadata.get("year"), *metadata.get("publicationYears", [])] if type(y) is int and y > 0}
        if "publicationYears" in pack and label["yearScope"] != "unclear":
            bounds = pack["publicationYears"]
            inside = any(bounds["from"] <= y <= bounds["to"] for y in known_years)
            require(bool(known_years) and (label["yearScope"] == "yes") == inside, "year judgment contradicts recorded publication years")
        require(label["evidenceType"] in ("writing_performance", "feedback_process", "self_efficacy_perception", "secondary_review", "none", "unclear"), "invalid evidence type")
        require(isinstance(label.get("rationale"), str) and len(label["rationale"].strip()) >= 12, "annotation lacks rationale")
        require(isinstance(label.get("unknowns"), list) and isinstance(label.get("evidenceFields"), list) and label["evidenceFields"], "annotation lacks field/unknown disclosure")
        for field in label["evidenceFields"]:
            value = records[key]["metadata"]
            for part in field.split("."):
                require(isinstance(value, dict) and part in value, "annotation cites missing metadata field")
                value = value[part]
            require(bool(value), "annotation cites an empty metadata field")
        if label["label"] == "metadata_relevant":
            require(label["population"] == label["intervention"] == label["yearScope"] == "yes" and label["evidenceType"] not in ("none", "unclear"), "relevance contradicts explicit annotation dimensions")
        if label["label"] == "out_of_scope":
            require(label["population"] == "no" or label["intervention"] == "no" or label["yearScope"] == "no" or label["evidenceType"] == "none", "exclusion is based only on unknown information")
        by_id[key] = label
    require(set(by_id) == set(records), "unlabelled records remain")
    return by_id


def doi(value):
    return str(value or "").lower().removeprefix("https://doi.org/").strip()


def report(spec, capture, pack, labels):
    annotations = validate_labels(labels, pack)
    summaries, slots, observations = [], {}, {}
    for row in capture["rows"]:
        key = (row["caseId"], row["sourceId"], row["arm"])
        works = row["search"].get("works") or []
        slots[key] = row
        observations[key] = {w["sourceRecordId"]: w for w in works}
        counts = Counter(annotations[annotation_id(row["caseId"], w)]["label"] for w in works)
        evidence = Counter(annotations[annotation_id(row["caseId"], w)]["evidenceType"] for w in works if annotations[annotation_id(row["caseId"], w)]["label"] == "metadata_relevant")
        n = len(works)
        source_status = row["search"].get("sources", [])
        title_only = sum(annotations[annotation_id(row["caseId"], w)]["label"] == "metadata_relevant" and not w.get("abstract") for w in works)
        summaries.append({"caseId": row["caseId"], "sourceId": row["sourceId"], "arm": row["arm"], "status": "aborted" if row.get("aborted") else source_status[0]["status"] if source_status else "aborted", "errorCode": source_status[0].get("errorCode") if source_status else None, "sourceRecords": n, "labels": dict(counts), "relevantEvidenceTypes": dict(evidence), "relevantWithoutAbstract": title_only, "metadataFractionLower": counts["metadata_relevant"]/n if n else None, "metadataFractionUpper": (counts["metadata_relevant"]+counts["uncertain"])/n if n else None, "missingAbstracts": sum(not w.get("abstract") for w in works), "http": row["actualHttp"]})
    changes = []
    for case in spec["cases"]:
        cid = case["id"]
        for source in spec["sources"]:
            a, b = [observations[(cid, source, arm)] for arm in spec["arms"]]
            statuses = [slots[(cid, source, arm)]["search"].get("sources", []) for arm in spec["arms"]]
            comparable = all(not slots[(cid, source, arm)].get("aborted") and s and s[0]["status"] in ("ok", "empty") for arm, s in zip(spec["arms"], statuses))
            def item(work):
                tag = annotations[annotation_id(cid, work)]
                return {"sourceRecordId": work["sourceRecordId"], "title": work["title"], "label": tag["label"], "evidenceType": tag["evidenceType"]}
            changes.append({"caseId": cid, "sourceId": source, "bothSourceSlotsSucceeded": comparable, "added": [item(b[k]) for k in sorted(b.keys()-a.keys())] if comparable else None, "lost": [item(a[k]) for k in sorted(a.keys()-b.keys())] if comparable else None, "retainedSourceRecords": len(a.keys() & b.keys()) if comparable else None, "metadataChangedWithinRetained": [k for k in sorted(a.keys() & b.keys()) if record_view(a[k]) != record_view(b[k])] if comparable else None})
    anchor_probes = []
    for anchor in spec["anchors"]:
        hits = []
        for row in capture["rows"]:
            if row["caseId"] != anchor["caseId"]:
                continue
            for work in row["search"].get("works") or []:
                matched = (anchor.get("doi") and doi(work.get("identifiers", {}).get("doi")) == doi(anchor["doi"])) or (anchor.get("eric") and work["sourceId"] == "eric" and work["sourceRecordId"] == anchor["eric"])
                if matched:
                    hits.append({"arm": row["arm"], "sourceId": row["sourceId"], "sourceRecordId": work["sourceRecordId"]})
        anchor_probes.append({"anchorId": anchor["id"], "expectedMetadataLabel": anchor["expected"], "hits": hits, "absenceMeaning": "not observed in captured first pages; not database absence or population recall"})
    diagnostics = {name: sum(bool(t.get(name)) for t in capture["http"]) for name in ("transportFailed", "capturedPrefixOnly", "bodyReadFailed", "bodyCloseFailed")}
    compiled_controls = []
    for case in spec["cases"]:
        baseline, focused = [slots[(case["id"], "crossref", arm)]["search"].get("sources", []) for arm in spec["arms"]]
        if baseline and focused:
            before, after = baseline[0].get("providerQuery", ""), focused[0].get("providerQuery", "")
            compiled_controls.append({"caseId": case["id"], "sourceId": "crossref", "baselineProviderQuery": before, "focusedProviderQuery": after, "isExactPopulationSuffixAddition": after == before + " university undergraduate college"})
    deviations = [{"caseId": p["caseId"], "sourceId": p["sourceId"], "reason": "Ranked projection changed more than the intended population suffix; this pair is NOT an isolated population-only comparison. Preserve frozen protocol and capture; do not attribute observed changes solely to population scope."} for p in compiled_controls if not p["isExactPopulationSuffixAddition"]]
    result = {"version": "literature-quality-report-v1", "protocolSha256": capture["specSha256"], "captureSha256": pack["captureSha256"], "screeningSha256": sha(canonical(pack)), "labelsSha256": sha(canonical(labels)), "modelHttp": capture["modelHttp"], "sourceHttp": capture["actualHttp"], "captureDiagnostics": diagnostics, "perSourceSlots": summaries, "withinSourceChanges": changes, "rankedCompiledControls": compiled_controls, "protocolDeviations": deviations, "predeclaredAnchorProbes": anchor_probes, "limitations": ["Frozen protocol constraints describe intentions; protocolDeviations override any unfulfilled population-only comparison claim."] + spec["constraints"] + ["Metadata labels authored by one agent, not independent expert/fulltext gold. Arm membership omitted from annotation pack, not rigorous double-blinding.", "Fractions concern returned source records, not independent studies, causal effects, population precision/recall or model scientific acceptance.", "writing_performance includes revision accuracy and title-described writing outcomes; it does not verify independent writing ability, measured test results, design validity or improvement. relevantWithoutAbstract discloses title/other-metadata-only judgments.", "Source failures are not empty sets: failed pairs do not receive fabricated gain/loss comparisons.", "Abstract/PDF availability is not fulltext acquisition. No fulltexts or effect conclusions were validated."]}
    return apply_capture_mode(result, spec, capture)


def apply_capture_mode(result, spec, capture):
    if not has_dispatch_evidence(spec):
        return result  # v1 report/pack identities stay unchanged
    mode = capture["evidenceMode"]
    require(mode in ("public_api", "controlled_tls_fixture"), "unknown evidence mode")
    public = mode == "public_api"
    result.update(sourceHttp=capture["actualHttp"] if public else 0, dispatchHttp=capture["actualHttp"], evidenceMode=mode, publicApiCaptureDeclared=public)
    result["requestEvidence"] = {"version": capture["captureVersion"], "mode": mode, "guardRejected": capture["guardRejected"], "preflightSha256": capture["preflightSha256"], "recordedAt": "client single-attempt dispatch boundary", "publicSourceHttp": result["sourceHttp"], "controlledFixtureHttp": 0 if public else capture["actualHttp"], "limitation": "Captured request bytes/parameters at the client boundary, not proof of provider receipt or semantic equivalence. Controlled fixtures are not live source-quality evidence."}
    result["requestEvidence"]["modeProvenance"] = "producer_declared_not_independently_authenticated"
    result["limitations"].insert(0, "Evidence mode is declared by the producing entry point, not independently authenticated network provenance. Hashes bind artifacts, not their trustworthiness; manually rewriting capture and derived hashes cannot be detected as an authentic producer run.")
    if not public:
        result["limitations"].insert(0, "CONTROLLED TLS FIXTURE ONLY: not a live retrieval-quality experiment; sourceHttp is 0, dispatchHttp counts local fixture requests. Do not interpret fixture labels, empty sets or anchor misses as real source quality.")
    return result


def self_test():
    work = {"sourceId": "eric", "sourceRecordId": "EJ1234567", "title": "Controlled university writing feedback", "abstract": "Undergraduate participants received feedback on writing.", "year": 2024}
    key = annotation_id("fixture", work)
    pack = {"protocolSha256": "p", "captureSha256": "c", "publicationYears": {"from": 2022, "to": 2026}, "records": [{"id": key, "caseId": "fixture", "metadata": record_view(work)}]}
    labels = {"version": "literature-quality-labels-v1", "reviewer": "agent_metadata_review", "protocolSha256": "p", "captureSha256": "c", "screeningSha256": sha(canonical(pack)), "labels": [{"id": key, "label": "uncertain", "population": "yes", "intervention": "unclear", "yearScope": "yes", "evidenceType": "unclear", "rationale": "The fixture abstract does not specify the feedback provider.", "evidenceFields": ["abstract"], "unknowns": ["provider"]}]}
    validate_labels(labels, pack)
    for mutate in (lambda x: x.update(screeningSha256="wrong"), lambda x: x["labels"].append(x["labels"][0]), lambda x: x["labels"][0].update(label="out_of_scope"), lambda x: x["labels"][0].update(label="metadata_relevant"), lambda x: x["labels"][0].update(evidenceFields=["authors.institution"]), lambda x: x["labels"][0].update(yearScope="no")):
        copy = json.loads(json.dumps(labels)); mutate(copy)
        try:
            validate_labels(copy, pack)
        except ValueError:
            pass
        else:
            raise AssertionError("invalid labels accepted")
    require(annotation_id("other-case", work) != key, "case identity lost")
    for years, judgment, valid in (([2021], "no", True), ([2021], "yes", False), ([2022], "yes", True), ([2026], "yes", True), ([2027], "yes", False), ([2021, 2024], "yes", True), ([], "yes", False), ([], "unclear", True)):
        year_pack = json.loads(json.dumps(pack))
        year_pack["records"][0]["metadata"].pop("year")
        year_pack["records"][0]["metadata"]["publicationYears"] = years
        year_labels = json.loads(json.dumps(labels))
        year_labels["screeningSha256"] = sha(canonical(year_pack))
        year_labels["labels"][0]["yearScope"] = judgment
        try:
            validate_labels(year_labels, year_pack)
        except ValueError:
            require(not valid, "valid boundary publication year rejected")
        else:
            require(valid, "contradictory/unknown publication year accepted")
    with tempfile.TemporaryDirectory(prefix="literature-quality-test-") as temp:
        directory = Path(temp).resolve()
        path = directory/"once.json"
        save_new(path, {"fixture": True})
        try:
            save_new(path, {})
        except FileExistsError:
            pass
        else:
            raise AssertionError("existing output overwritten")
        # Full local capture fixture: missing/tampered raw evidence, counters,
        # query association and output semantics, without contacting any source.
        spec = load(Path(__file__).resolve().parent.parent/"contracts/literature-quality-pilot-v1.json")
        encoded = canonical(spec)
        (directory/"plan.json").write_bytes(encoded)
        fixture = {"specSha256": sha(encoded), "modelHttp": 0, "complete": True, "budgetDenied": 0, "actualHttp": 12, "startedAt": "2026-11-04T00:00:00Z", "endedAt": "2026-11-04T00:00:01Z", "rows": [], "http": []}
        for case in spec["cases"]:
            for ai, arm in enumerate(spec["arms"]):
                for source in spec["sources"]:
                    number = len(fixture["http"])+1
                    name = f"http-{number:03}.raw"
                    body = canonical({"eric": {"response": {"numFound": 0, "docs": []}}, "openalex": {"meta": {"count": 0}, "results": []}, "crossref": {"status": "ok", "message": {"items": []}}}[source])
                    (directory/name).write_bytes(body)
                    endpoint = {"eric": "https://api.ies.ed.gov/eric/", "openalex": "https://api.openalex.org/works", "crossref": "https://api.crossref.org/works"}[source]
                    fixture["http"].append({"ordinal": number, "queryIndex": ai, "caseId": case["id"], "sourceId": source, "endpoint": endpoint, "status": 200, "bodyFile": name, "capturedBytes": len(body), "capturedSha256": sha(body)})
                    inp = case["focusedProviderQueries"][source] if ai else case["logicalQuery"]
                    fixture["rows"].append({"caseId": case["id"], "arm": arm, "sourceId": source, "actualHttp": 1, "aborted": False, "search": {"query": case["logicalQuery"], "works": [], "sources": [{"sourceId": source, "status": "empty", "count": 0, "publicationYears": spec["publicationYears"], "queryProjectionVersion": "source-query-v1", "logicalQuery": case["logicalQuery"], "inputQuery": inp, "providerQuery": "controlled-provider-query", "effectiveQuery": "controlled-effective-query"}]}})
        def write_fixture(value):
            (directory/"capture.json").write_bytes(canonical(value))
        write_fixture(fixture)
        read_capture(directory)
        raw_path = directory/fixture["http"][0]["bodyFile"]
        original_body = raw_path.read_bytes()
        raw_path.write_bytes(b"x" * ((8 << 20) + 1))
        try:
            read_capture(directory)
        except ValueError:
            pass
        else:
            raise AssertionError("oversized raw evidence accepted")
        finally:
            raw_path.write_bytes(original_body)
        def drop_last_request(value):
            value["http"].pop()
            value["actualHttp"] -= 1
            value["rows"][-1]["actualHttp"] = 0
        for mutate in (
            lambda x: x.update(specSha256="bad"),
            lambda x: x.update(actualHttp=13),
            lambda x: x["rows"].pop(),
            lambda x: x["rows"].append(x["rows"][0]),
            lambda x: x["rows"][0].update(actualHttp=0),
            lambda x: x["rows"][0]["search"]["sources"][0].update(inputQuery="another query"),
            lambda x: x["rows"][0]["search"]["sources"][0].update(publicationYears={"from": 2021, "to": 2026}),
            lambda x: x["rows"][0].update(aborted=True),
            lambda x: x["http"][0].update(capturedPrefixOnly=True),
            lambda x: x["http"][0].update(bodyReadFailed=True),
            lambda x: x["http"][0].update(bodyCloseFailed=True),
            lambda x: x["http"][0].update(status=500),
            lambda x: x["http"][1].update(bodyFile=x["http"][0]["bodyFile"], capturedSha256=x["http"][0]["capturedSha256"]),
            lambda x: x["http"][0].update(bodyFile="../escaped.raw"),
            lambda x: x["http"][0].update(capturedSha256="bad"),
            lambda x: x.update(complete=False),
            lambda x: x.update(budgetDenied=1),
            drop_last_request,
        ):
            copy = json.loads(json.dumps(fixture)); mutate(copy); write_fixture(copy)
            try:
                read_capture(directory)
            except ValueError:
                pass
            else:
                raise AssertionError("invalid capture accepted")
        # Bind all annotation fields and source audits to a separately generated
        # production replay. Mutating ID/title/abstract/year/DOI/metadata or query
        # audit must fail, even when capture counters/raw hashes still match.
        fixture["rows"][0]["search"]["works"] = [work]
        fixture["rows"][0]["search"]["sources"][0].update(status="ok", count=1)
        body = canonical({"response": {"numFound": 1, "docs": [{"id": work["sourceRecordId"], "title": work["title"], "description": work["abstract"], "publicationdateyear": work["year"]}]}})
        (directory/fixture["http"][0]["bodyFile"]).write_bytes(body)
        fixture["http"][0].update(capturedBytes=len(body), capturedSha256=sha(body))
        replay = {"version": "literature-quality-replay-v1", "kind": "current_service_raw_replay_no_network", "protocolSha256": fixture["specSha256"], "networkHttp": 0, "modelHttp": 0, "entries": []}
        for row, trace in zip(fixture["rows"], fixture["http"]):
            replay["entries"].append({**{k: row[k] for k in ("caseId", "arm", "sourceId")}, "bodyFile": trace["bodyFile"], "bodySha256": trace["capturedSha256"], "search": json.loads(json.dumps(row["search"]))})
        write_fixture(fixture)
        replay["captureSha256"] = sha((directory/"capture.json").read_bytes())
        validate_replay(replay, spec, fixture, directory)
        for mutate in (
            lambda x: x["rows"][0]["search"]["sources"][0].update(providerQuery="tampered"),
            lambda x: x["rows"][0]["search"]["sources"][0].update(effectiveQuery="tampered"),
            lambda x: x["rows"][0]["search"]["sources"][0].update(yearFilterApplied=False),
            lambda x: x["rows"][0]["search"]["works"][0].update(sourceRecordId="EJ9999999"),
            lambda x: x["rows"][0]["search"]["works"][0].update(title="Another title"),
            lambda x: x["rows"][0]["search"]["works"][0].update(abstract="Another abstract"),
            lambda x: x["rows"][0]["search"]["works"][0].update(year=2021),
            lambda x: x["rows"][0]["search"]["works"][0].update(identifiers={"doi": "10.1/wrong"}),
            lambda x: x["rows"][0]["search"]["works"][0].update(sourceMetadata={"educationLevels": ["Secondary Education"]}),
        ):
            copy = json.loads(json.dumps(fixture)); mutate(copy); write_fixture(copy)
            bound = json.loads(json.dumps(replay))
            bound["captureSha256"] = sha((directory/"capture.json").read_bytes())
            try:
                validate_replay(bound, spec, copy, directory)
            except ValueError:
                pass
            else:
                raise AssertionError("same-slot raw/query replay mismatch accepted")
        # A failed source is a valid observation, but not an empty-set success.
        fixture["rows"][0]["search"]["works"] = []
        fixture["rows"][0]["search"]["sources"][0].update(status="failed", count=0, errorCode="invalid_data")
        write_fixture(fixture)
        read_capture(directory)
        empty_pack = screening_pack(spec, fixture, directory)
        empty_labels = {"version": "literature-quality-labels-v1", "reviewer": "agent_metadata_review", "protocolSha256": fixture["specSha256"], "captureSha256": empty_pack["captureSha256"], "screeningSha256": sha(canonical(empty_pack)), "labels": []}
        result = report(spec, fixture, empty_pack, empty_labels)
        require(result["withinSourceChanges"][0]["added"] is None and result["withinSourceChanges"][0]["lost"] is None, "failed source became an empty set")
        require(all(s["metadataFractionLower"] is None for s in result["perSourceSlots"]), "zero records became 100 percent relevance")
        fixture["rows"][0].update(aborted=True)
        fixture["rows"][0]["search"]["sources"] = []
        write_fixture(fixture)
        read_capture(directory)
        result = report(spec, fixture, empty_pack, empty_labels)
        require(result["withinSourceChanges"][0]["added"] is None, "aborted source became a successful comparison")
    print("Quality analyzer self-test passed: labels/year boundaries, capture/raw ordinal integrity, fresh HTTP, complete-body scope, query/metadata replay binding, aborted/failure semantics and overwrite guards; no network/model.")


def self_test_dispatch(directory):
    """Mutate private copies of an existing controlled fixture, never its originals."""
    spec, capture = read_capture(directory)
    require(has_dispatch_evidence(spec) and capture["evidenceMode"] == "controlled_tls_fixture", "dispatch self-test requires a controlled v2/v3 fixture")
    preflight = load(directory/"preflight.json")
    pack = screening_pack(spec, capture, directory)
    require(not pack["records"], "dispatch self-test expects empty controlled fixture records, not scientific labels")
    labels = {"version": "literature-quality-labels-v1", "reviewer": "agent_metadata_review", "protocolSha256": pack["protocolSha256"], "captureSha256": pack["captureSha256"], "screeningSha256": sha(canonical(pack)), "labels": []}
    fixture_report = report(spec, capture, pack, labels)
    require(fixture_report["sourceHttp"] == fixture_report["requestEvidence"]["publicSourceHttp"] == 0 and fixture_report["dispatchHttp"] == fixture_report["requestEvidence"]["controlledFixtureHttp"] == spec["maxHttp"] and fixture_report["publicApiCaptureDeclared"] is False, "local fixture requests became public-source quality evidence")
    def change_pair_beyond_suffix(c, p):
        row = next(r for r in c["rows"] if r["caseId"] == "ai_feedback_writing" and r["arm"] == "population_focus" and r["sourceId"] == "crossref")
        entry = p['ai_feedback_writing:population_focus:crossref']
        source = entry["source"]
        source["effectiveQuery"] += " EXTRA"
        source["providerQuery"] = source["effectiveQuery"]
        entry["request"]["parameters"]["query.bibliographic"] = [source["providerQuery"]]
        entry["request"]["encodedQuery"] = urlencode(sorted(entry["request"]["parameters"].items()), doseq=True)
        row["search"]["sources"][0].update(query_audit(source))
        trace = next(t for t in c["http"] if t["caseId"] == row["caseId"] and t["sourceId"] == "crossref" and t["queryIndex"] == 1)
        trace["request"] = json.loads(json.dumps(entry["request"]))
    def reorder_traces(c, p):
        c["http"][0], c["http"][1] = c["http"][1], c["http"][0]
        for i, trace in enumerate(c["http"][:2], 1):
            trace.update(ordinal=i, bodyFile=f"http-{i:03}.raw")
    with tempfile.TemporaryDirectory(prefix="literature-dispatch-test-") as temp:
        target = Path(temp)
        for name in ["plan.json", *[t["bodyFile"] for t in capture["http"]]]:
            (target/name).write_bytes((directory/name).read_bytes())
        for mutate in (
            lambda c, p: c.update(captureVersion="quality-dispatch-v1"),
            lambda c, p: c.pop("evidenceMode"),
            lambda c, p: c.update(guardRejected=1),
            lambda c, p: c["rows"].reverse(),
            reorder_traces,
            lambda c, p: c["http"][0].pop("request"),
            lambda c, p: c["http"][0]["request"].update(method="POST"),
            lambda c, p: c["http"][0]["request"]["parameters"].update(rows=["100"]),
            lambda c, p: p.pop(next(iter(p))),
            lambda c, p: p[next(iter(p))]["request"].update(encodedQuery="invalid=query"),
            change_pair_beyond_suffix,
        ):
            clone, prepared = json.loads(json.dumps(capture)), json.loads(json.dumps(preflight))
            mutate(clone, prepared)
            (target/"preflight.json").write_bytes(canonical(prepared))
            clone["preflightSha256"] = sha((target/"preflight.json").read_bytes())
            (target/"capture.json").write_bytes(canonical(clone))
            try:
                read_capture(target)
            except ValueError:
                pass
            else:
                raise AssertionError("invalid dispatch/preflight evidence accepted")
        # All planned rows remain in order even when a source fails before an
        # HTTP attempt; trace order is the actualHttp==1 subsequence, not 12
        # fabricated attempts. Such a pair still receives no gain/loss result.
        failed = json.loads(json.dumps(capture))
        row = failed["rows"][-1]
        failed["http"].pop()
        failed["actualHttp"] -= 1
        row["actualHttp"] = 0
        row["search"]["sources"][0].update(status="failed", count=0, errorCode="unavailable")
        row["search"]["partial"] = True
        (target/"preflight.json").write_bytes(canonical(preflight))
        failed["preflightSha256"] = sha((target/"preflight.json").read_bytes())
        (target/"capture.json").write_bytes(canonical(failed))
        read_capture(target)
        failed_pack = screening_pack(spec, failed, target)
        failed_labels = {**labels, "captureSha256": failed_pack["captureSha256"], "screeningSha256": sha(canonical(failed_pack))}
        failed_report = report(spec, failed, failed_pack, failed_labels)
        pair = next(p for p in failed_report["withinSourceChanges"] if p["caseId"] == row["caseId"] and p["sourceId"] == row["sourceId"])
        require(pair["added"] is pair["lost"] is None and failed_report["sourceHttp"] == 0 and failed_report["dispatchHttp"] == spec["maxHttp"]-1, "pre-dispatch failure became a fabricated request or empty success")
        replay = {"version": "literature-quality-replay-v1", "kind": "current_service_partial_diagnostic_no_network", "diagnosticPartial": True, "failedSlotsNotReplayed": 1, "protocolSha256": failed["specSha256"], "captureSha256": sha((target/"capture.json").read_bytes()), "networkHttp": 0, "modelHttp": 0, "entries": []}
        traces = {(t["caseId"], spec["arms"][t["queryIndex"]], t["sourceId"]): t for t in failed["http"]}
        for source_row in failed["rows"]:
            slot = (source_row["caseId"], source_row["arm"], source_row["sourceId"])
            trace = traces.get(slot, {})
            entry = {**{k: source_row[k] for k in ("caseId", "arm", "sourceId")}, "bodyFile": trace.get("bodyFile", ""), "bodySha256": trace.get("capturedSha256", "")}
            if source_row["search"]["sources"][0]["status"] == "failed":
                prepared = preflight[":".join(slot)]
                entry.update(disposition="failed_not_replayed", projectionAudit=prepared["source"], projectionRequest=prepared["request"])
            else:
                entry.update(search=source_row["search"], request=trace["request"])
            replay["entries"].append(entry)
        consistency = validate_replay(replay, spec, failed, target, True)
        require(consistency["failedSlotsNotReplayed"] == 1, "failure was replayed as success")
        try:
            validate_replay(replay, spec, failed, target)
        except ValueError:
            pass
        else:
            raise AssertionError("strict mode accepted partial diagnostic")
        marked = mark_partial_diagnostic(failed_report, failed)
        failed_slot = next(s for s in marked["perSourceSlots"] if s["status"] == "failed")
        require(failed_slot["sourceRecords"] is failed_slot["labels"] is None and marked["strictQualityReportEligible"] is False, "partial diagnostic fabricated known counts or quality eligibility")
        for mutate in (lambda x: x.update(failedSlotsNotReplayed=0), lambda x: x["entries"][-1].update(search={}), lambda x: x["entries"][-1].update(request={}), lambda x: x["entries"][-1]["projectionRequest"].update(encodedQuery="&".join(reversed(x["entries"][-1]["projectionRequest"]["encodedQuery"].split("&"))))):
            invalid = json.loads(json.dumps(replay)); mutate(invalid)
            try:
                validate_replay(invalid, spec, failed, target, True)
            except ValueError:
                pass
            else:
                raise AssertionError("failed diagnostic promoted to successful replay")
    print("Dispatch self-test passed: mode/version, guard rejection, absent/altered request evidence, preflight shape and post-projection confound rejected; source originals unchanged, no network/model.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--capture-dir", type=Path)
    parser.add_argument("--prepare", type=Path)
    parser.add_argument("--labels", type=Path)
    parser.add_argument("--query-audit", type=Path, help="offline production raw/query replay export (not wire evidence)")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--diagnostic-partial", action="store_true", help="explicit failure diagnostic only; strict default unchanged")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        if args.capture_dir:
            self_test_dispatch(args.capture_dir)
        return
    if not args.capture_dir or not args.query_audit:
        parser.error("--capture-dir and --query-audit are required")
    if args.prepare:
        if args.labels or args.out:
            parser.error("--prepare cannot be mixed with --labels/--out")
    elif not args.labels or not args.out:
        parser.error("use either --prepare OR both --labels/--out")
    spec, capture = read_capture(args.capture_dir)
    consistency = validate_replay(load(args.query_audit), spec, capture, args.capture_dir, args.diagnostic_partial)
    pack = screening_pack(spec, capture, args.capture_dir)
    if args.diagnostic_partial:
        pack.update(analysisScope="successful_slots_from_partial_failure_capture", partialReplayFileSha256=sha(args.query_audit.read_bytes()))
    if args.prepare:
        save_new(args.prepare, pack)
        print(f"Prepared {len(pack['records'])} metadata observations with arm membership omitted.")
    else:
        result = report(spec, capture, pack, load(args.labels))
        if args.diagnostic_partial:
            mark_partial_diagnostic(result, capture)
        result["offlineConsistency"] = consistency
        result["offlineReplayFileSha256"] = sha(args.query_audit.read_bytes())
        save_new(args.out, result)
        print("Wrote PARTIAL FAILURE DIAGNOSTIC, not strict quality acceptance." if args.diagnostic_partial else "Wrote controlled fixture report (0 public-source HTTP), NOT live retrieval quality." if capture.get("evidenceMode") == "controlled_tls_fixture" else "Wrote metadata-level quality report; no recall/scientific acceptance claim.")


if __name__ == "__main__":
    main()
