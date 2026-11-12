package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

type correctionPayloadFixture struct {
	base, patch, merged string
	full, repair        json.RawMessage
	diagnostic          error
}

func correctionPayloadTestFixture(t *testing.T, kind string) correctionPayloadFixture {
	t.Helper()
	var f correctionPayloadFixture
	var err error
	switch kind {
	case "issue_review":
		f.full, f.base = issueRepairFixture(t)
		f.diagnostic = &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.ReviewMissingFindingError{ID: "issue-1-2"}}
		r := newReviewRepair(f.base, f.full, f.diagnostic)
		f.patch, f.repair = issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"limitations": []string{"Scope retained"}}}), r.schema
		f.merged, err = r.merge(f.patch)
	case "legacy_review":
		f.base, f.full, f.diagnostic = reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic()
		r := newReviewRepair(f.base, f.full, f.diagnostic)
		f.patch, f.repair = reviewRepairValidPatch(r), r.schema
		f.merged, err = r.merge(f.patch)
	case "identity":
		f.base, f.full, f.diagnostic = identityRepairFixture()
		r := newLiteratureIdentityRepair(f.base, f.full, f.diagnostic)
		f.patch, f.repair = identityPatch(r), r.schema
		f.merged, err = r.merge(f.patch)
	case "citation":
		f.base, f.full = quoteRepairBase, quoteRepairSchema
		f.diagnostic = &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]"}}
		r := newCitationRepairForError(f.base, f.full, f.diagnostic)
		f.patch, f.repair = quotePatch(r, "Exact evidence."), r.schema
		f.merged, err = r.merge(f.patch)
	default:
		t.Fatal("unknown fixture", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func correctionTextEnvelope(envelope, text string) string {
	switch envelope {
	case "fenced":
		return "```json\n" + text + "\n```"
	case "crlf_fenced":
		return " \r\n``` JSON\r\n" + text + "\r\n```\t "
	default:
		return text
	}
}

func TestUnwrappedCorrectionMissingHashRetainsBoundBase(t *testing.T) {
	for _, kind := range []string{"issue_review", "legacy_review", "identity", "citation"} {
		f := correctionPayloadTestFixture(t, kind)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(f.patch), &fields)
		delete(fields, "baseSHA256")
		encoded, _ := json.Marshal(fields)
		for _, envelope := range []string{"bare", "fenced", "crlf_fenced"} {
			t.Run(kind+"/"+envelope, func(t *testing.T) {
				raw := correctionTextEnvelope(envelope, string(encoded))
				final := submissionToolScript(f.patch)
				final[0].Event.ToolCall.Name = stageRepairTool
				loop, state, provider := newLoopFixture(t, nil, submissionScript(f.base), submissionScript(raw), final)
				state.workflowAI = true
				host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: f.full}}, initial: f.diagnostic}
				loop.research = host
				if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 3 {
					t.Fatal("unwrapped invalid patch discarded bound repair", out, len(provider.Requests()), state.run.ErrorCode)
				}
				request := provider.Requests()[2]
				if len(request.Tools) != 2 || request.Tools[1].Name != stageRepairTool || string(request.Tools[1].InputSchema) != string(f.repair) {
					t.Fatal("missing hash changed or removed the current repair interface")
				}
				if len(host.validated) != 2 || host.validated[1] != f.merged || state.journalDrafts[2] != raw || state.journalDrafts[3] != f.patch || len(state.calls) != 0 {
					t.Fatal("unwrapped patch reached full validator or corrupted audit/evidence", len(host.validated))
				}
				if !strings.Contains(submissionRequestText(request), "baseSHA256") {
					t.Fatal("precise missing-field diagnostic absent from next correction")
				}
			})
		}
	}
}

func TestCorrectionDispatchPreservesTypedQuotationLengthDiagnostic(t *testing.T) {
	schema := json.RawMessage(strings.Replace(string(quoteRepairSchema), `"supportingQuote":{"type":"string"}`, `"supportingQuote":{"type":"string","maxLength":40}`, 1))
	diagnostic := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]"}}
	r := newCitationRepairForError(quoteRepairBase, schema, diagnostic)
	bad := quotePatch(r, strings.Repeat("证", 41))
	for _, envelope := range []string{"bare", "fenced", "native"} {
		t.Run(envelope, func(t *testing.T) {
			raw := correctionTextEnvelope(envelope, bad)
			firstBad, secondBad := submissionScript(raw), submissionScript(raw)
			if envelope == "native" {
				firstBad, secondBad = submissionToolScript(bad), submissionToolScript(bad)
				firstBad[0].Event.ToolCall.Name, secondBad[0].Event.ToolCall.Name = stageRepairTool, stageRepairTool
			}
			loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), firstBad, secondBad)
			state.workflowAI = true
			host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: schema}}, initial: diagnostic}
			loop.research = host
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || len(provider.Requests()) != 3 || len(host.validated) != 1 || len(state.calls) != 0 {
				t.Fatal("bad quote accepted, reached full validator, or renewed correction budget", out)
			}
			if state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || !strings.Contains(state.run.ErrorMessage, "超过长度限制") || !strings.Contains(state.run.ErrorMessage, "41") || !strings.Contains(state.run.ErrorMessage, "40") {
				t.Fatal("typed length failure replaced by generic patch format error", state.run.ErrorMessage)
			}
		})
	}
}

func TestUnwrappedMixedCorrectionMissingHashRetainsBoundBase(t *testing.T) {
	for _, kind := range []string{"issue_review", "legacy_review", "identity", "citation"} {
		f := correctionPayloadTestFixture(t, kind)
		var fields, base map[string]json.RawMessage
		_ = json.Unmarshal([]byte(f.patch), &fields)
		_ = json.Unmarshal([]byte(f.base), &base)
		delete(fields, "baseSHA256")
		fullOnly := map[string]string{"issue_review": "approved", "legacy_review": "approved", "identity": "summary", "citation": "count"}[kind]
		fields[fullOnly] = base[fullOnly]
		encoded, _ := json.Marshal(fields)
		for _, envelope := range []string{"bare", "fenced", "crlf_fenced"} {
			t.Run(kind+"/"+envelope, func(t *testing.T) {
				raw := correctionTextEnvelope(envelope, string(encoded))
				last := submissionToolScript(f.patch)
				last[0].Event.ToolCall.Name = stageRepairTool
				loop, state, provider := newLoopFixture(t, nil, submissionScript(f.base), submissionScript(raw), last)
				state.workflowAI = true
				host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: f.full}}, initial: f.diagnostic}
				loop.research = host
				if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 3 || len(host.validated) != 2 || host.validated[1] != f.merged {
					t.Fatal("mixed invalid patch discarded base or reached full validator", out, state.run.ErrorCode)
				}
				if state.journalDrafts[2] != raw || len(state.calls) != 0 || string(provider.Requests()[2].Tools[1].InputSchema) != string(f.repair) {
					t.Fatal("mixed patch lost original audit or bound repair")
				}
			})
		}
	}
}

func TestTextCorrectionControlUsesCurrentSchemasAndWholeRootOnly(t *testing.T) {
	f := correctionPayloadTestFixture(t, "issue_review")
	var missing map[string]json.RawMessage
	_ = json.Unmarshal([]byte(f.patch), &missing)
	delete(missing, "baseSHA256")
	withoutHash, _ := json.Marshal(missing)
	fullWithMetadata := json.RawMessage(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","items":{"type":"string"}},"baseSHA256":{"type":"string"}}}`)
	shared := json.RawMessage(`{"type":"object","required":["baseSHA256"],"additionalProperties":false,"properties":{"baseSHA256":{"type":"string"}}}`)
	for _, tt := range []struct {
		name, text, control string
		full, repair        json.RawMessage
		invalid             bool
	}{
		{name: "current_patch", text: f.patch, control: stageRepairTool, full: f.full, repair: f.repair},
		{name: "missing_hash", text: string(withoutHash), control: stageRepairTool, full: f.full, repair: f.repair, invalid: true},
		{name: "full_coercion_with_repair_named_metadata", text: `{"items":"one, two","baseSHA256":"audit-not-patch"}`, control: stageSubmissionTool, full: fullWithMetadata, repair: f.repair},
		{name: "both_valid", text: `{"baseSHA256":"audit-or-patch"}`, full: shared, repair: shared, invalid: true},
		{name: "only_shared_invalid_retains_original_owner", text: `{"baseSHA256":42}`, full: shared, repair: shared},
		{name: "ordinary_full", text: f.base, full: f.full, repair: f.repair},
		{name: "nested_patch_is_not_root", text: `{"outer":` + f.patch + `}`, full: f.full, repair: f.repair},
		{name: "patch_field_in_string", text: `{"note":"reviewUpdates and baseSHA256 are ordinary prose"}`, full: f.full, repair: f.repair},
		{name: "malformed_not_reconstructed", text: strings.TrimSuffix(f.patch, "}"), full: f.full, repair: f.repair},
		{name: "prose_not_scanned", text: "Explanation: " + f.patch, full: f.full, repair: f.repair},
		{name: "pseudo_tool_not_scanned", text: "<tool_call>" + f.patch + "</tool_call>", full: f.full, repair: f.repair},
		{name: "multiple_objects_not_selected", text: f.patch + "\n" + f.patch, full: f.full, repair: f.repair},
	} {
		for _, envelope := range []string{"bare", "fenced", "crlf_fenced"} {
			t.Run(tt.name+"/"+envelope, func(t *testing.T) {
				raw := correctionTextEnvelope(envelope, tt.text)
				control, body, err := textCorrectionControl(raw, tt.full, tt.repair)
				if control != tt.control || (err != nil) != tt.invalid {
					t.Fatal("incorrect or guessed control routing", control, err)
				}
				if control != "" && string(body) != tt.text {
					t.Fatal("routing changed payload bytes or selected a nested value", string(body))
				}
				if control == "" && body != nil {
					t.Fatal("ambiguous/unknown text yielded an executable candidate")
				}
			})
		}
	}
}

func TestTextCorrectionInvalidFieldsStayBoundAndStrict(t *testing.T) {
	for _, kind := range []string{"issue_review", "legacy_review", "identity", "citation"} {
		f := correctionPayloadTestFixture(t, kind)
		for _, mode := range []string{"null_hash", "wrong_hash", "unknown_field"} {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal([]byte(f.patch), &fields)
			switch mode {
			case "null_hash":
				fields["baseSHA256"] = json.RawMessage(`null`)
			case "wrong_hash":
				fields["baseSHA256"] = json.RawMessage(`"stale-hash"`)
			case "unknown_field":
				fields["foreignPatch"] = json.RawMessage(`true`)
			}
			encoded, _ := json.Marshal(fields)
			for _, envelope := range []string{"bare", "fenced", "crlf_fenced"} {
				t.Run(kind+"/"+mode+"/"+envelope, func(t *testing.T) {
					raw := correctionTextEnvelope(envelope, string(encoded))
					last := submissionToolScript(f.patch)
					last[0].Event.ToolCall.Name = stageRepairTool
					loop, state, provider := newLoopFixture(t, nil, submissionScript(f.base), submissionScript(raw), last)
					state.workflowAI = true
					host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: f.full}}, initial: f.diagnostic}
					loop.research = host
					if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 3 || len(host.validated) != 2 || host.validated[1] != f.merged {
						t.Fatal("invalid field bypassed patch validation or discarded base", out, state.run.ErrorCode)
					}
					if state.journalDrafts[2] != raw || len(state.calls) != 0 || string(provider.Requests()[2].Tools[1].InputSchema) != string(f.repair) {
						t.Fatal("patch validation changed journal, base, or completed evidence")
					}
				})
			}
		}
	}
}

func TestUnwrappedBadCorrectionDoesNotRenewBudget(t *testing.T) {
	for _, kind := range []string{"issue_review", "legacy_review", "identity", "citation"} {
		f := correctionPayloadTestFixture(t, kind)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(f.patch), &fields)
		delete(fields, "baseSHA256")
		encoded, _ := json.Marshal(fields)
		t.Run(kind, func(t *testing.T) {
			raw := correctionTextEnvelope("fenced", string(encoded))
			loop, state, provider := newLoopFixture(t, nil, submissionScript(f.base), submissionScript(raw), submissionScript(raw))
			state.workflowAI = true
			host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: f.full}}, initial: f.diagnostic}
			loop.research = host
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || len(host.validated) != 1 {
				t.Fatal("invalid unwrapped patch accepted, renewed budget, or reached full validator", out)
			}
			if len(state.calls) != 0 || state.messages[1].Parts[0].Text != "" || state.journalDrafts[2] != raw || state.journalDrafts[3] != raw {
				t.Fatal("rejected correction overwrote accepted output or lost original audit")
			}
		})
	}
}

func TestUnwrappedFullResultWithRepairNamedMetadataUsesHost(t *testing.T) {
	f := correctionPayloadTestFixture(t, "citation")
	var schema, properties map[string]json.RawMessage
	_ = json.Unmarshal(f.full, &schema)
	_ = json.Unmarshal(schema["properties"], &properties)
	properties["baseSHA256"] = json.RawMessage(`{"type":"string"}`)
	schema["properties"], _ = json.Marshal(properties)
	fullSchema, _ := json.Marshal(schema)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(f.base), &fields)
	fields["baseSHA256"] = json.RawMessage(`"full-result-metadata-not-patch-base"`)
	complete, _ := json.Marshal(fields)
	for _, envelope := range []string{"bare", "fenced", "crlf_fenced"} {
		t.Run(envelope, func(t *testing.T) {
			raw := correctionTextEnvelope(envelope, string(complete))
			loop, state, provider := newLoopFixture(t, nil, submissionScript(f.base), submissionScript(raw))
			state.workflowAI = true
			host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: fullSchema}}, initial: f.diagnostic}
			loop.research = host
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 2 || host.validated[1] != raw || len(state.calls) != 0 {
				t.Fatal("legitimate full result misread as local patch", out, state.run.ErrorCode)
			}
			if state.journalDrafts[2] != raw || !strings.Contains(host.validated[1], "9007199254740993") {
				t.Fatal("full output normalization replaced original text or numeric bytes")
			}
		})
	}
}

func TestStageSubmissionObjectPreservesExistingEnvelopeBoundary(t *testing.T) {
	body := "{\"note\":\"literal backticks ```json and ``` are data\",\"n\":9007199254740993}"
	for _, tt := range []struct {
		name, text     string
		object, fenced bool
	}{
		{name: "bare", text: " \n" + body + "\t ", object: true},
		{name: "fence", text: correctionTextEnvelope("fenced", body), object: true, fenced: true},
		{name: "crlf", text: correctionTextEnvelope("crlf_fenced", body), object: true, fenced: true},
		{name: "prose", text: "Explanation: " + correctionTextEnvelope("fenced", body)},
		{name: "trailing_prose", text: correctionTextEnvelope("fenced", body) + " done"},
		{name: "two_blocks", text: correctionTextEnvelope("fenced", body) + "\n" + correctionTextEnvelope("fenced", body)},
		{name: "array", text: "[" + body + "]"},
		{name: "fenced_array", text: correctionTextEnvelope("fenced", "["+body+"]")},
		{name: "scalar", text: `"baseSHA256"`},
		{name: "broken", text: strings.TrimSuffix(body, "}")},
		{name: "pseudo_tool", text: "<tool_call>" + body + "</tool_call>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decoded, ok := stageSubmissionObject(tt.text)
			candidate, err := stageSubmissionCandidate(nil, tt.text)
			if ok != tt.object || (err == nil) != tt.object || stageSubmissionEnvelope(tt.text) != tt.fenced {
				t.Fatal("submission admission and envelope decoding differ", ok, err)
			}
			if ok && (string(decoded) != body || candidate != tt.text) {
				t.Fatal("decoder altered payload or provider text")
			}
		})
	}
}

func TestAuditableOversizeCorrectionCannotFallThroughToFullResult(t *testing.T) {
	for _, kind := range []string{"issue_review", "legacy_review", "identity", "citation"} {
		f := correctionPayloadTestFixture(t, kind)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(f.patch), &fields)
		fields["junk"], _ = json.Marshal(strings.Repeat("证", 90000))
		encoded, _ := json.Marshal(fields)
		for _, envelope := range []string{"bare", "fenced", "native"} {
			t.Run(kind+"/"+envelope, func(t *testing.T) {
				raw := correctionTextEnvelope(envelope, string(encoded))
				if len([]rune(raw)) >= 200000 || len(raw) <= 256*1024 {
					t.Fatal("fixture must fit raw journal rune limit but exceed JSON byte limit")
				}
				bad := submissionScript(raw)
				if envelope == "native" {
					bad = submissionToolScript(raw)
					bad[0].Event.ToolCall.Name = stageRepairTool
				}
				last := submissionToolScript(f.patch)
				last[0].Event.ToolCall.Name = stageRepairTool
				loop, state, provider := newLoopFixture(t, nil, submissionScript(f.base), bad, last)
				state.workflowAI = true
				host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: f.full}}, initial: f.diagnostic}
				loop.research = host
				if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 3 || len(host.validated) != 2 || host.validated[1] != f.merged {
					t.Fatal("oversize repair fell through as full result and discarded base", out, state.run.ErrorCode, len(host.validated))
				}
				if state.journalDrafts[2] != raw || len(state.calls) != 0 || string(provider.Requests()[2].Tools[1].InputSchema) != string(f.repair) {
					t.Fatal("oversize rejection lost raw audit or bound repair")
				}
			})
		}
	}
}
