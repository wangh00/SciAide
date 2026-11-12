package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

// The current host prompt may be a view of immutable stage data. Human text,
// source storage, native turns and evidence identities are never compressed.
// No observed token ratio is borrowed from a different run or model.
func budgetedStageMessages(messages []conversation.Message, run chat.Run, contract chat.WorkflowAIExecution, definitions []tool.Definition, calls []tool.Call, system, dynamic, routing string, effective, automatic int, initialGrowth ...int) []conversation.Message {
	if effective <= 0 {
		effective = defaultMaxContextTokens
	}
	if automatic <= 0 {
		automatic = effective
	}
	if !definitionVisible(definitions, workflow.ResearchTaskReadTool) || len(contract.Input) == 0 || fmt.Sprintf("%x", sha256.Sum256(contract.Input)) != contract.InputSHA256 {
		return messages
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(contract.PromptText))) != contract.PromptSHA256 {
		return messages
	}
	var allowed []string
	_ = json.Unmarshal(contract.AllowedTools, &allowed)
	found := false
	for _, name := range allowed {
		found = found || name == workflow.ResearchTaskReadTool
	}
	if !found {
		return messages
	}
	fixed := stageInputFixedRequest(definitions, system, dynamic, routing)
	// Reserve both exploration and the final submission shape, plus a useful
	// tool page/correction margin. This is not an enlarged model window.
	overhead := max(estimateRequestTokens(fixed), estimateRequestTokens(stageSubmissionRequest(fixed, workflow.ReviewSubmissionSchema(contract.OutputSchema))))
	reserve := min(24_000, max(8_000, effective/5))
	// Source-only implementation recovery can start with an already populated
	// control envelope. Account for that INITIAL shape, never re-page a view
	// already used by the current loop to make room for later corrections.
	if len(initialGrowth) > 0 {
		reserve += max(0, initialGrowth[0])
	}
	budget := min(effective, automatic) - overhead - reserve
	if budget < 4096 {
		budget = effective - overhead - reserve
	}
	if budget < 4096 {
		return messages
	}
	for i, m := range messages {
		if m.ID != run.UserMessageID || m.Role != conversation.RoleUser {
			continue
		}
		// Pages contain original workflow markers; the seed is the sole authority
		// for translating them to this execution. Never mint additional citations.
		replacements := []string{}
		mapping := map[string]string{}
		for _, call := range calls {
			if call.ToolName != citation.WorkflowSeedToolName || call.Result == nil || call.RunID != run.ID {
				continue
			}
			for _, ref := range call.Result.Citations {
				if ref.Reference != citation.KnowledgeReference(run.ID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256) {
					continue
				}
				old := citation.KnowledgeReference(contract.WorkflowRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
				if old != ref.Reference && strings.Contains(string(contract.Input), old) {
					replacements = append(replacements, old, ref.Reference)
					mapping[old] = ref.Reference
				}
			}
		}
		note := ""
		if len(mapping) > 0 {
			encoded, _ := json.Marshal(mapping)
			note = "\n<stage_page_citations>Original saved-page marker to CURRENT issued marker (same evidence, no new citation): " + string(encoded) + "</stage_page_citations>\n"
		}
		prompt, changed := workflow.ProjectResearchStagePrompt(contract.PromptText, contract.Input, budget-utf8.RuneCountInString(note), contract.PromptVersion)
		// The common projector owns composition and non-expansion for every
		// caller; do not infer its safety from marker text or a stray $read.
		// The automatic threshold is soft. A large immutable rule/schema
		// prefix can prevent ANY projection at that threshold even though the
		// effective window still has room. Retry projection locally against
		// that same hard window, before fixing this execution's material view.
		// Never reproject later corrections or enlarge the configured window.
		hardBudget := effective - overhead - reserve
		if utf8.RuneCountInString(prompt)+utf8.RuneCountInString(note) > budget && hardBudget > budget {
			if candidate, projected := workflow.ProjectResearchStagePrompt(contract.PromptText, contract.Input, hardBudget-utf8.RuneCountInString(note), contract.PromptVersion); projected && len(candidate) < len(prompt) && utf8.RuneCountInString(candidate) < utf8.RuneCountInString(prompt) {
				prompt, changed = candidate, true
			}
		}
		if !changed && (!strings.Contains(prompt, "$read") || note == "") && conversationText(m) == prompt {
			return messages
		}
		if len(replacements) > 0 {
			prompt = strings.NewReplacer(replacements...).Replace(prompt)
		}
		prompt += note
		result := append([]conversation.Message(nil), messages...)
		result[i].Parts = []conversation.MessagePart{{Type: "text", Text: prompt}}
		return result
	}
	return messages
}

func stageInputFixedRequest(definitions []tool.Definition, system, dynamic, routing string) model.ChatRequest {
	fixed := model.ChatRequest{Messages: []model.Message{{Role: model.RoleSystem, Content: fixedSystemRules + workspaceToolRules + system + dynamic + routing}}}
	for _, d := range definitions {
		fixed.Tools = append(fixed.Tools, model.ToolDefinition{Name: d.QualifiedName, Description: d.Description, InputSchema: d.InputSchema})
	}
	return fixed
}

// Reading a frozen input page is transport, not a fresh research exploration.
// Only successful, same-snapshot, forward coverage earns allowance, bounded
// independently from research turns. Duplicates/failures do not reset limits.
func stageInputTransportTurns(contract chat.WorkflowAIExecution, calls []tool.Call, ledgers ...*stageMaterialLedger) int {
	materialTurns := 0
	if len(ledgers) > 0 {
		materialTurns = ledgers[0].transportTurns(calls)
	} else {
		materialTurns = stageMaterialTransportTurns(contract, calls)
	}
	return min(256, rawStageInputTransportTurns(contract, calls)+materialTurns)
}

func rawStageInputTransportTurns(contract chat.WorkflowAIExecution, calls []tool.Call) int {
	if len(contract.Input) == 0 || fmt.Sprintf("%x", sha256.Sum256(contract.Input)) != contract.InputSHA256 {
		return 0
	}
	type span struct{ start, end int }
	spans := []span{}
	credits := 0
	totalCharacters := utf8.RuneCount(contract.Input)
	for _, call := range calls {
		if call.ToolName != workflow.ResearchTaskReadTool || call.Result == nil || call.Result.Status != tool.ResultSuccess {
			continue
		}
		var args struct {
			Section, NodeID string
			Offset          int
			Pointer         *string
		}
		if json.Unmarshal(call.Arguments, &args) != nil || args.Pointer != nil || args.Section != "stage_input" || args.NodeID != "" && args.NodeID != contract.NodeID && args.NodeID != contract.WorkflowStepID {
			continue
		}
		var page struct {
			Section, SHA256, Content            string
			Offset, NextOffset, TotalCharacters int
		}
		if json.Unmarshal(call.Result.Structured, &page) != nil || page.Section != "stage_input" || page.SHA256 != contract.InputSHA256 || page.Offset != args.Offset || page.NextOffset <= page.Offset || page.NextOffset > totalCharacters || page.NextOffset-page.Offset != utf8.RuneCountInString(page.Content) {
			continue
		}
		// A preview may expose only a prefix, so the next legal read can
		// overlap the saved page while still reaching previously unread bytes.
		// Count substantial new coverage, not any overlap and not 1-byte drift.
		novel := page.NextOffset - page.Offset
		for _, s := range spans {
			novel -= max(0, min(page.NextOffset, s.end)-max(page.Offset, s.start))
		}
		if novel < min(512, page.NextOffset-page.Offset) {
			continue
		}
		merged := span{page.Offset, page.NextOffset}
		kept := spans[:0]
		for _, s := range spans {
			if s.end < merged.start || s.start > merged.end {
				kept = append(kept, s)
			} else {
				merged.start = min(merged.start, s.start)
				merged.end = max(merged.end, s.end)
			}
		}
		spans = append(kept, merged)
		credits++
		if credits >= 256 {
			break
		}
	}
	return credits
}
