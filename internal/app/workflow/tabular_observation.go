package workflow

// Observations only, not a statistical validator or a semantic identity guess.
// Keys are bounded independently of the existing full-file shape inspection.
const tabularObservationCode = `import hashlib

AUDIT_KEY_LIMIT = 200_000
AUDIT_RECORD_LIMIT = 200_000

class EqualityObservation:
    def __init__(self):
        self.counts = {}
        self.complete = True
        self.checked = 0
        self.limitation = ""

    def observe(self, values):
        if not self.complete:
            return
        if self.checked >= AUDIT_RECORD_LIMIT:
            self.complete = False
            self.counts.clear()
            self.limitation = "Optional equality audit reached its record budget; full-file shape checks still completed. Missing duplicate counts do not mean zero."
            return
        fingerprint = hashlib.sha256(json.dumps(values, ensure_ascii=False, separators=(",", ":")).encode("utf-8")).digest()
        if fingerprint not in self.counts and len(self.counts) >= AUDIT_KEY_LIMIT:
            self.complete = False
            self.counts.clear()
            self.limitation = "Optional equality audit reached its key budget; full-file shape checks still completed. Missing duplicate counts do not mean zero."
            return
        self.counts[fingerprint] = self.counts.get(fingerprint, 0) + 1
        self.checked += 1

    def result(self):
        result = {"complete": self.complete, "checkedRecordCount": self.checked, "comparison": "decoded cell strings in column order, SHA-256 fingerprints; no numeric coercion"}
        if self.complete:
            repeated = [count for count in self.counts.values() if count > 1]
            result.update({"distinctRecordCount": len(self.counts), "repeatedGroupCount": len(repeated), "recordsInRepeatedGroups": sum(repeated), "additionalRepeatedRecordCount": sum(count - 1 for count in repeated)})
        else:
            result["limitation"] = self.limitation
        return result

class ColumnUniquenessObservation:
    def __init__(self, width):
        self.width = min(width, 256)
        self.seen = [set() for _ in range(self.width)]
        self.repeated = [False] * self.width
        self.missing = [False] * self.width
        self.retained = 0
        self.checked = 0
        self.stopped = False

    def observe(self, row):
        if self.stopped:
            return
        if self.checked >= AUDIT_RECORD_LIMIT:
            self.stopped = True
            self.seen = [None] * self.width
            self.retained = 0
            return
        self.checked += 1
        for index in range(self.width):
            value = row[index]
            self.missing[index] = self.missing[index] or not value.strip()
            seen = self.seen[index]
            if seen is None:
                continue
            fingerprint = hashlib.sha256(value.encode("utf-8")).digest()
            if fingerprint in seen:
                self.repeated[index] = True
                self.retained -= len(seen)
                self.seen[index] = None
            elif self.retained >= AUDIT_KEY_LIMIT:
                self.retained -= len(seen)
                self.seen[index] = None
            else:
                seen.add(fingerprint)
                self.retained += 1

    def unique_indices(self, row_count):
        if self.stopped or row_count < 2:
            return []
        return [index for index, seen in enumerate(self.seen) if seen is not None and not self.missing[index]]

    def complete(self, width):
        return not self.stopped and self.width == width and all(seen is not None or self.repeated[index] for index, seen in enumerate(self.seen))

def tabular_equality_observation(text, delimiter, headers, row_count, full_rows, columns):
    excluded = columns.unique_indices(row_count)
    result = {"version": "tabular-equality-v1", "fullRows": full_rows.result(), "uniqueColumnScanComplete": columns.complete(len(headers)), "fullyUniqueColumnIndices": excluded, "identityVerified": False, "interpretation": "Structural equality is not sample identity. Fully unique columns may be row labels OR genuine measurements. The projection is a diagnostic candidate, never an adopted grouping, deletion or weighting rule."}
    if not excluded or len(excluded) == len(headers):
        result["withoutFullyUniqueColumns"] = {"complete": False, "reason": "No informative nonempty projection is available; do not infer no repeated measurements."}
        return result
    excluded_set = set(excluded)
    retained = [index for index in range(len(headers)) if index not in excluded_set]
    projection = EqualityObservation()
    reader = csv.reader(io.StringIO(text, newline=""), delimiter=delimiter, strict=True)
    next(reader)
    for row in reader:
        if row:
            projection.observe([row[index] for index in retained])
        if not projection.complete:
            break
    result["withoutFullyUniqueColumns"] = dict(projection.result(), excludedColumnIndices=excluded, retainedColumnCount=len(retained), retainedColumnIndices=retained[:256], retainedColumnIndicesComplete=len(retained) <= 256)
    return result
`

const computationAuditVersion = "data-audit-v2"

// Apply only to computational work. The model owns the scientific judgment;
// the host never fabricates findings, alters old reviews or auto-approves.
const computationAuditInstruction = `
Computational observation and inference audit (within the agreed research scope):
Apply these distinctions to the duplicate, leakage, uncertainty and inference checks relevant to this task. Do not add unrelated deliverables or tests to a simple calculation.
- Distinguish a row identifier, an observed equality group and a verified scientific sample/participant/batch identity. Uniqueness or an ID-like name does NOT prove that the field identifies independent observations. When duplicate checks are relevant, report both whole-record duplicates and repeated measurement combinations excluding justified annotation/identifier/partition columns; including a unique row label in every equality key can hide repetitions. Explicitly state the compared columns, counts and uncertain sample identity. Partition and outcome must not enter a predictor-only leakage key. Check repeated IDs separately where relevant.
- tabularEqualityAudit, when present in preflight, is a bounded structural observation only. fullRows compares every column. withoutFullyUniqueColumns is a DIAGNOSTIC projection excluding structurally unique columns, which may include real measurements; it neither selects scientific variables nor proves independent samples. Read fullyUniqueColumnIndices against the actual headers, inspect the original bound rows through exposed tools for a concrete disagreement, and choose the justified measurement/identity views. Incomplete/missing counts are unknown, not zero. An old preflight without this observation does not prove no duplicates; inspect the same declared input rather than requiring a new upload.
- Preserve original records. Equal measured values do not by themselves prove the same subject or a recording error. Do not automatically delete, merge, reweight or change train/test. Record the basis of any adopted group key and assess repeat weights, cross-partition overlap and uncertainty. Distinguish record-level errors from equal-group errors and observational groups from independent samples. A bootstrap grouped by unique row numbers is still record-level resampling, not a correction for unverified dependence. Keep changes to grouping, estimands, resampling, exclusions or multiplicity in the existing researchChanges/confirmation flow.
- Trace each reported statistical value across executed code, JSON/CSV outputs and the methods/report. Distinguish raw from adjusted p-values, primary from supplemental analyses and exploratory descriptions from inferential conclusions. State the correction family/method for the p-values used inferentially; label unadjusted supplemental values as such and do not use them as adjusted significance evidence. Pointwise intervals and exploratory regression coefficients need accurate interpretation, not an automatic universal multiplicity gate. Do not claim that ALL p-values were adjusted when only one family was.
- Review the complete computational contract in this pass, not just the first reporting issue or a successful Kernel status. Zero duplicates/group counts and claims of independent resampling require a justified comparison/group definition. Compare questionable upstream definitions against available rows rather than trusting the report or code to self-certify them. Find all currently supported actionable defects; do not invent missing facts or force extra analyses beyond the agreed maturity.
- Choose the latest offered repair stage that can fully correct the defect and its reusable outputs. A labeling/provenance defect with correct values can be resolved by explicitly defining their use and raw/adjusted status when the agreed output permits that; the mere presence of an unused unadjusted supplemental value does not demand a full computational replay. A wrong grouping, uncertainty calculation, value or reusable output does require the responsible upstream implementation. Only genuinely inaccessible material or an unresolved scientific choice requires user input. This guidance is not a new output schema, submission veto, automatic verdict or execution authorization.
`

func computationalStageAudit(detail RunDetail, step Step, node CompiledNode) string {
	if node.Kind != NodeAgentStage && node.Kind != NodeAIAnalysis {
		return ""
	}
	switch node.ID {
	case "method_selection", "method_implementation", "result_interpretation", "report_drafting", "draft_report":
	default:
		if !isIndependentReviewSchema(node.OutputSchema) {
			return ""
		}
	}
	if node.ID == "method_implementation" || node.ID == "result_interpretation" {
		return computationAuditInstruction
	}
	input := decodeObject(step.Input)
	for _, key := range []string{"computedResults", "dataPreflight", "dataContext"} {
		if _, ok := input[key].(map[string]any); ok {
			return computationAuditInstruction
		}
	}
	for _, compiled := range detail.Run.Compilation.Nodes {
		if compiled.ID == "python_analysis" && compiled.Kind == NodePython {
			return computationAuditInstruction
		}
	}
	return ""
}
