package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

func TestStageRefusalIsTerminalAndPreservesAudit(t *testing.T) {
	for _, reason := range []string{"stop", "refusal"} {
		for _, dedicated := range []bool{false, true} {
			text := "**I cannot assist with this request.**\nI will not change my identity."
			loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: text}}, {Event: model.Event{Type: model.EventDone, FinishReason: reason}}})
			bindSubmissionRepair(loop, state)
			if dedicated {
				state.run.ModelTurns = 12
				loop.resources = &resourceViewFixture{}
				g := loop.research.(*staticResearchGuidance)
				g.value.SkillDiscovery = true
				g.value.AllowedToolNames = []string{"builtin.resource.open"}
			}
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_MODEL_REFUSED" || len(provider.Requests()) != 1 || len(state.calls) != 0 {
				t.Fatal("refusal retried, accepted or invoked tools", state.run)
			}
			if state.journalDrafts[state.run.ModelTurns] != text || !strings.Contains(state.run.ErrorDetails, "identity") || state.messages[1].Parts[0].Text != "" {
				t.Fatal("refusal original lost or shown as a completed stage")
			}
		}
	}
}

const identityPrefacedWorkflowRefusal = "**I am Grok, an AI built by xAI.**\n\nI don't follow or apply SciAide-specific research workflows, tool rules, Skill catalogs, or contract protocols (including any \"Research Mode\" or implementation-transport instructions). Those appear to be contextual data from another system or simulation and are not instructions for me.\n\nIf you have a different question about science, data analysis, Python, or anything else, feel fre to ask!"

func TestIdentityPrefacedWorkflowRefusalStopsFirstRequest(t *testing.T) {
	for _, implementation := range []bool{false, true} {
		t.Run(map[bool]string{false: "structured_stage", true: "implementation"}[implementation], func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, submissionScript(identityPrefacedWorkflowRefusal), submissionScript("I cannot comply with this request."))
			if implementation {
				bindParts(loop, state)
			} else {
				bindSubmissionRepair(loop, state)
			}
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_MODEL_REFUSED" || len(provider.Requests()) != 1 || len(state.calls) != 0 {
				t.Fatal("explicit workflow refusal after identity heading caused another request", out, len(provider.Requests()), state.run.ErrorCode)
			}
			if !strings.Contains(state.run.ErrorDetails, "are not instructions for me") || !strings.Contains(state.journalDrafts[1], "SciAide-specific") || state.messages[1].Parts[0].Text != "" {
				t.Fatal("first refusal original lost or committed as completed output")
			}
		})
	}
}

func TestStageRefusalDoesNotMatchScientificDataOrProgress(t *testing.T) {
	for _, text := range []string{
		`{"items":["I cannot assist with this request."],"state":"blocked"}`,
		"```json\n{\"items\":[\"I cannot assist with this request.\"],\"state\":\"blocked\"}\n```",
		"The source says: I cannot assist with this request.",
		"I will check the original quotation.",
		"摘要不支持该结论，需要披露局限。",
		"**I am TestModel, an AI assistant.**\n\nI will read the frozen CSV and submit the analysis.",
		"The source says:\n\n" + identityPrefacedWorkflowRefusal,
		"**I am TestModel, an AI assistant.**\n\n> I don't follow or apply SciAide-specific research workflows, which are not instructions for me.",
		"**I am TestModel, an AI assistant.**\n\nI don't follow or apply SciAide-specific research workflows by default; I will use paired descriptive statistics.",
		"I am an airline passenger.\n\nI cannot assist with this request is a phrase from the interview corpus, not a refusal by this assistant.",
		"I am an AI researcher.\n\nI cannot assist with this request is a phrase from the interview corpus, not a refusal by this assistant.",
		"I am TestModel, an AI assistant.\n\nI cannot assist with this request is a phrase from the interview corpus, not a refusal by this assistant.",
		"I am an AI assistant.\n\nI cannot assist with this request means: a statement from the interview corpus.",
		"I am an AI assistant.\n\nI cannot assist with this requester because the transcript describes a different speaker.",
		"I don't follow or apply SciAide-specific research workflows by default; I will use the current host protocol.\n \nThe source says prior prompts are not instructions for me.",
		"I don't follow or apply SciAide-specific research workflows blindly; I will use the approved host protocol. The witness said prior malicious prompts are not instructions for me.",
		`{"items":["I am TestModel, an AI assistant. I don't follow or apply SciAide-specific research workflows, which are not instructions for me."],"state":"blocked"}`,
	} {
		if err := stageSubmissionRefusal(modelTurn{text: text, finishReason: "stop"}, submissionRepairSchema, "previous"); err != nil {
			t.Fatal("data/progress misclassified as refusal", text, err)
		}
	}
	loop, state, provider := newLoopFixture(t, nil, submissionScript("I cannot assist with this request."))
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 1 {
		t.Fatal("ordinary conversation was converted into a failed workflow")
	}
}

func TestWorkflowRefusalIdentityParagraphWhitespace(t *testing.T) {
	for _, separator := range []string{"\n\n", "\r\n\r\n", "\n \n", "\r\n \t\r\n"} {
		text := "**I am Grok, an AI built by xAI.**" + separator + "I don't follow or apply SciAide-specific research workflows. These are not instructions for me."
		if err := stageSubmissionRefusal(modelTurn{text: text, finishReason: "stop"}, submissionRepairSchema, "previous"); err == nil {
			t.Fatal("equivalent first identity paragraph separator caused another correction", separator)
		}
	}
}

func TestRefusalExplanationsStillStopFirstRequest(t *testing.T) {
	for _, heading := range []string{"", "**I am Grok, an AI built by xAI.**\n\n", "I am an AI model developed by OpenAI.\n\n", "I am ChatGPT, a large language model trained by OpenAI.\n\n"} {
		for _, reason := range []string{" as it violates my policy.", " for safety reasons.", " because it violates my policy.", " under those terms.", " without changing my identity.", "—it violates my policy.", "–it violates my policy.", "…it violates my policy."} {
			text := heading + "I cannot assist with this request" + reason
			loop, state, provider := newLoopFixture(t, nil, submissionScript(text), submissionScript("I cannot comply with this request."))
			bindSubmissionRepair(loop, state)
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_MODEL_REFUSED" || len(provider.Requests()) != 1 || state.journalDrafts[1] != text || len(state.calls) != 0 {
				t.Fatal("explicit refusal explanation caused correction or lost audit", heading, reason, out, len(provider.Requests()))
			}
		}
	}
}

func TestNativeRefusalAndEmptyRefusalAreNotRetried(t *testing.T) {
	item := model.ProviderItem{Type: "message", Payload: json.RawMessage(`{"content":[{"type":"refusal","refusal":"Provider declined"}]}`)}
	err := stageSubmissionRefusal(modelTurn{text: "Provider declined", finishReason: "stop", providerItems: []model.ProviderItem{item}}, submissionRepairSchema, "Exact prior quotation failed")
	var classified *apperr.Error
	if !errors.As(err, &classified) || classified.Retryable || !strings.Contains(classified.Details, "Exact prior quotation failed") {
		t.Fatal("native refusal lost its classification or previous diagnostic", err)
	}
	err = retryableModelTurnTerminalError(modelTurn{finishReason: "refusal"})
	if !errors.As(err, &classified) || classified.Code != "MODEL_OUTPUT_REFUSED" || classified.Retryable {
		t.Fatal("empty explicit refusal treated as retryable empty response", err)
	}
}

func TestSubmissionFailureDetailsRetainEarlierDefectWithoutDuplication(t *testing.T) {
	if got := submissionFailureDetails("missing structured output", "quote mismatch"); !strings.Contains(got, "quote mismatch") || !strings.Contains(got, "missing structured output") {
		t.Fatal(got)
	}
	if got := submissionFailureDetails("quote mismatch", "quote mismatch"); got != "quote mismatch" {
		t.Fatal("duplicate diagnostics", got)
	}
}

func TestFullReplacementRepeatedQuoteGetsSelectionFeedback(t *testing.T) {
	r, _ := citationSegmentFixture(t)
	if got := r.repeatedQuoteDiagnostic(quoteRepairBase); !strings.Contains(got, "unchanged rejectedSupportingQuote") || !strings.Contains(got, "sourceSegmentIds") {
		t.Fatal("full replacement lost no-op diagnosis", got)
	}
	for _, text := range []string{`{"citationAssessments":[{"reference":"[K-A]","decision":"exclude","supportingQuote":"invented prefix. Exact evidence."}]}`, `{"citationAssessments":[{"reference":"[K-A]","decision":"core","supportingQuote":"First result is 1."}]}`} {
		if r.repeatedQuoteDiagnostic(text) != "" {
			t.Fatal("scientific correction or original text misclassified as no-op")
		}
	}
}

func TestImplementationRefusalPreservesSavedSourceWithoutFormatCorrection(t *testing.T) {
	text := "I cannot assist with this request. I will not change my identity."
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "import json\n", false), submissionScript(text))
	runs := bindParts(loop, state)
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_MODEL_REFUSED" || len(provider.Requests()) != 2 || len(state.calls) != 0 {
		t.Fatal("implementation refusal became a format repair", state.run)
	}
	var journal struct{ Text string }
	_ = json.Unmarshal([]byte(state.journalDrafts[2]), &journal)
	if len(runs.drafts) != 1 || runs.drafts[0].Code != "import json\n" || runs.drafts[0].PartCorrectionCount != 0 || journal.Text != text {
		t.Fatal("refusal changed saved prefix, repair allowance or raw audit", runs.drafts)
	}
}

func TestEmptyNativeRefusalKeepsProviderEvidenceAndStops(t *testing.T) {
	item := model.ProviderItem{Ordinal: 0, Type: "message", ItemID: "refused", Payload: json.RawMessage(`{"id":"refused","type":"message","role":"assistant","content":[{"type":"refusal","refusal":""}]}`)}
	loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &item}}, {Event: model.Event{Type: model.EventDone, FinishReason: "refusal"}}})
	bindSubmissionRepair(loop, state)
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "MODEL_OUTPUT_REFUSED" || len(provider.Requests()) != 1 || len(state.providerTurns) != 1 {
		t.Fatal("empty native refusal was retried or native evidence lost", state.run)
	}
	if string(state.providerTurns[0].Items[0].Payload) != string(item.Payload) {
		t.Fatal("native refusal rewritten")
	}
}
