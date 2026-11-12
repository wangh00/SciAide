package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestAIStageFormatClassificationPreservesHostAndBusinessErrors(t *testing.T) {
	schema := raw(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	for _, text := range []string{`{"summary":"ok"}"`, `{"summary":{}}`, `{"summary":"unfinished"`} {
		_, _, err := NormalizeAIStageSubmission(text, schema)
		if err == nil || !IsAIStageOutputFormatError(err) {
			t.Fatal("envelope/schema failure lost its category", err)
		}
		wrapped := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: err}
		if !IsAIStageOutputFormatError(wrapped) || wrapped.Cause.Error() != err.Error() {
			t.Fatal("host wrapper obscured original diagnostic", wrapped)
		}
	}
	_, _, err := NormalizeAIStageSubmission(`{}`, raw(`{"type":"object","required":["duplicate","duplicate"]}`))
	var host *apperr.Error
	if IsAIStageOutputFormatError(err) || !errors.As(err, &host) || host.Code != aiSchemaInvalidCode {
		t.Fatal("broken host contract was classified as model format", err)
	}
	s, repository, text := submissionValidationFixture(t)
	invalid := strings.Replace(text, `"nodeId":"method_selection"`, `"nodeId":"imaginary_stage"`, 1)
	_, err = s.ValidateResearchSubmission(context.Background(), "conversation", "run", repository.detail.Steps[4].ID, invalid)
	if err == nil || IsAIStageOutputFormatError(err) || !strings.Contains(err.Error(), "imaginary_stage") {
		t.Fatal("input-aware business rejection was classified as output format", err)
	}
}

func TestAIStageTrailingContentReportsOuterCandidateUTF8ByteOffset(t *testing.T) {
	schema := raw(`{"type":"object","additionalProperties":true}`)
	for _, body := range []string{
		`{"summary":"中文🙂","items":[{"detail":"保持证据"}]}`,
		"{\n  \"summary\": \"中文🙂\",\n  \"items\": [\n    {\"detail\": \"保持证据\"}\n  ]\n}",
	} {
		for _, fenced := range []bool{false, true} {
			for _, trailing := range []string{"\"", "”", "arbitrary prose"} {
				t.Run(fmt.Sprintf("pretty_%t/fenced_%t/tail_%s", strings.Contains(body, "\n"), fenced, trailing), func(t *testing.T) {
					prefix, suffix, fragmentLeading := "前置说明。\n", "", 0
					if fenced {
						prefix, suffix, fragmentLeading = prefix+"```json\n", "\n```", 1
					}
					text := prefix + body + " \t\r\n" + trailing + suffix
					_, _, err := NormalizeAIStageSubmission(text, schema)
					var tail *stageJSONTrailingContentError
					wantPosition := fragmentLeading + len(body) + len(" \t\r\n")
					if !errors.As(err, &tail) || tail.position != wantPosition || tail.character != []rune(trailing)[0] || !strings.Contains(err.Error(), "UTF-8") || tail.repaired {
						t.Fatalf("tail diagnostic did not refer to the outer JSON candidate, want byte=%d char=%q: %v", wantPosition, []rune(trailing)[0], err)
					}
				})
			}
		}
	}
}

func TestAIStageTrailingContentDoesNotPreferUnrelatedEarlierObject(t *testing.T) {
	text := `{"example":"` + strings.Repeat("long earlier example", 30) + `"}` + "\n" + `{"summary":"最终候选"}"`
	_, _, err := NormalizeAIStageSubmission(text, raw(`{"type":"object","additionalProperties":true}`))
	var tail *stageJSONTrailingContentError
	if !errors.As(err, &tail) || tail.character != '"' || tail.position != len(`{"summary":"最终候选"}`) {
		t.Fatal("unrelated longer example displaced final candidate diagnostic", err)
	}
}

func TestAIStageTrailingContentLabelsTemporaryRepairCoordinates(t *testing.T) {
	text := "```json\n" + `{"summary":"推荐与你"保留原文"的目标一致"}"` + "\n```"
	_, _, err := NormalizeAIStageSubmission(text, raw(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`))
	var tail *stageJSONTrailingContentError
	if !errors.As(err, &tail) || !tail.repaired || !strings.Contains(err.Error(), "临时语法修复副本（非原始回复）") {
		t.Fatal("post-repair offset was presented as original provider coordinates", err)
	}
}

func TestStageFenceOffsetsPreserveUnicodeBytes(t *testing.T) {
	schema := raw(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	for _, prefix := range []string{"K ", "Ⱥ ", "中文 KȺ\n"} {
		for _, fence := range []string{"```json", "```JSON", "``` JSON"} {
			text := prefix + fence + "\n" + `{"summary":"原始正文"}` + "\n```"
			value, _, err := NormalizeAIStageSubmission(text, schema)
			if err != nil || string(value) != `{"summary":"原始正文"}` {
				t.Errorf("Unicode prefix changed fence offsets: %q %v", prefix+fence, err)
			}
			if _, found := extractRawStageJSONObject(text, CompiledNode{OutputSchema: schema}); !found {
				t.Errorf("raw diagnostic parser lost Unicode-prefixed fence: %q", prefix+fence)
			}
		}
		metadata := `{"methodSummary":"中文 KȺ","dependencies":[],"analysisInput":{},"assumptions":[],"limitations":[]}`
		code := "source = SCIAIDE_INPUTS[0]\noutputs = SCIAIDE_OUTPUTS\nresult = {'ok': True}"
		text := prefix + "\n```JSON\n" + metadata + "\n```\n```PYTHON\n" + code + "\n```"
		value, _, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema(), "method_implementation")
		var decoded struct{ Code string }
		if err != nil || json.Unmarshal(value, &decoded) != nil || decoded.Code != code {
			t.Errorf("Unicode split-envelope offsets changed or rejected Python: %q %v", prefix, err)
		}
	}
}

func TestMalformedUnicodeStageFencesCannotPanic(t *testing.T) {
	for _, text := range []string{"Ⱥ```json", "K```JSON", "中文Ⱥ``` json"} {
		t.Run(text, func(t *testing.T) {
			defer func() {
				if failure := recover(); failure != nil {
					t.Errorf("malformed model text panicked instead of rejecting: %v", failure)
				}
			}()
			if _, _, err := NormalizeAIStageSubmission(text, raw(`{"type":"object"}`)); err == nil {
				t.Fatal("incomplete Unicode-prefixed fence was accepted")
			}
		})
	}
}
