package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
)

// Scratch accounting upper bound for constructing a candidate, not permission
// to send a larger request. Native items and source dialogue are never trimmed.
func fullContextCharacterSpace(messages []conversation.Message, excludedID string, definitions []tool.Definition, calls []tool.Call, skills skill.RunContext, routing, system, dynamic string, checkpoint contextmemory.Checkpoint, turns []model.ProviderTurn) int {
	total := 2048 + len(fixedSystemRules) + len(workspaceToolRules) + len(routing) + len(system) + len(dynamic)
	if checkpoint.ID != "" {
		total += len(checkpointContextMessage(checkpoint).Content)
	}
	for _, message := range messages {
		if message.ID != excludedID {
			total += len(conversationText(message))
		}
	}
	for _, definition := range definitions {
		total += len(definition.QualifiedName) + len(definition.Description) + len(definition.InputSchema)
	}
	if fragments, err := skill.RenderContextMessages(skills); err == nil {
		for _, fragment := range fragments {
			total += len(fragment)
		}
	}
	for _, call := range calls {
		total += len(call.ToolName) + len(call.Arguments) + len(toolCallContext(call))
	}
	for _, turn := range turns {
		for _, item := range turn.Items {
			total += len(item.Payload)
		}
	}
	return total
}

type contextUsageComponent struct {
	key     string
	bytes   int
	content bool
}

func usageComponent(kind string, value any, contentBytes int) contextUsageComponent {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(append([]byte(kind+":"), encoded...))
	// Charge the complete serialized component, not just Content: Parts,
	// call IDs, schemas and changing envelope fields also occupy input space.
	return contextUsageComponent{key: hex.EncodeToString(hash[:]), bytes: max(contentBytes, len(encoded)), content: kind != "envelope"}
}

func requestUsageComponents(request model.ChatRequest) []contextUsageComponent {
	components := make([]contextUsageComponent, 0, len(request.Messages)+len(request.Tools))
	for _, message := range request.Messages {
		bytes := len(message.Content)
		for _, call := range message.ToolCalls {
			bytes += len(call.Name) + len(call.Arguments)
		}
		if message.Role == model.RoleTool {
			components = append(components, toolResultUsageComponents("message-result", message)...)
			continue
		}
		components = append(components, usageComponent("message", struct {
			Message              model.Message
			HostReferences, Tail bool
		}{message, message.HostToolReferences, message.ContextTail}, bytes))
	}
	for _, definition := range request.Tools {
		components = append(components, usageComponent("tool", definition, len(definition.Name)+len(definition.Description)+len(definition.InputSchema)))
	}
	for _, turn := range request.ProviderTurns {
		for _, item := range turn.Items {
			components = append(components, usageComponent("native", struct {
				Protocol string
				Item     model.ProviderItem
			}{string(turn.Protocol), item}, len(item.Payload)))
		}
		for _, result := range turn.ToolResults {
			components = append(components, toolResultUsageComponents("native-result", result)...)
		}
	}
	// Changes in submission/stream mode or effort invalidate this small
	// envelope component; its measured protocol overhead is not discounted.
	components = append(components, usageComponent("envelope", struct {
		ForcedTool, Requested, Resolved             string
		DisableTools, DisableStreaming, RequireTool bool
		ReferenceNames                              []string
	}{request.ForcedTool, string(request.RequestedReasoningLevel), string(request.ResolvedReasoningLevel), request.DisableTools, request.DisableStreaming, request.RequireTool, request.ToolReferenceNames}, 0))
	return components
}

// Result previews are fitted again when calls grow. Hashing an entire preview
// makes every old result look new when its middle is shortened or expanded.
// Edge-aligned chunks reuse only exactly measured text for the SAME call;
// changed/expanded text still pays its full serialized byte cost. No hidden
// state is decoded, and no text is retained in the observation metadata.
func toolResultUsageComponents(kind string, message model.Message) []contextUsageComponent {
	text := []rune(message.Content)
	message.Content = ""
	envelope := struct {
		Message    model.Message
		Host, Tail bool
	}{message, message.HostToolReferences, message.ContextTail}
	identity := usageComponent(kind+"-identity", envelope, 0)
	identity.content = false
	parts := []contextUsageComponent{identity}
	const chunkSize = 512
	middle := len(text) / 2
	add := func(edge string, offset, from, to int) {
		parts = append(parts, usageComponent(kind+"-chunk", struct {
			Identity, Edge string
			Offset         int
			Text           string
		}{identity.key, edge, offset, string(text[from:to])}, 0))
	}
	for from := 0; from < middle; from += chunkSize {
		add("start", from, from, min(middle, from+chunkSize))
	}
	for end := len(text); end > middle; end -= chunkSize {
		add("end", len(text)-end, max(middle, end-chunkSize), end)
	}
	return parts
}

func observeContextUsage(run chat.Run, request model.ChatRequest, usage model.Usage, reported bool) chat.ContextUsageObservation {
	if !reported || usage.InputTokens <= 0 || requestHasImages(request) || !requestHasValidUTF8(request) {
		return chat.ContextUsageObservation{}
	}
	value := chat.ContextUsageObservation{Version: 1, RunID: run.ID, TurnIndex: run.ModelTurns, ModelProfileID: run.ModelProfileID, ModelID: run.ModelID, APIProtocol: run.APIProtocol,
		InputTokens: usage.InputTokens, CharacterEstimate: estimateRequestTokens(request), Components: map[string]int{}}
	for _, component := range requestUsageComponents(request) {
		value.Components[component.key]++
	}
	if !value.Matches(run) {
		return chat.ContextUsageObservation{}
	}
	return value
}

func requestHasValidUTF8(request model.ChatRequest) bool {
	encoded, err := json.Marshal(request)
	if err != nil || !utf8.Valid(encoded) {
		return false
	}
	// encoding/json replaces malformed strings, so checking its output alone
	// cannot establish that fingerprints represent the original request.
	return validContextUTF8(reflect.ValueOf(request))
}

func validContextUTF8(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.String:
		return utf8.ValidString(value.String())
	case reflect.Interface, reflect.Pointer:
		return value.IsNil() || validContextUTF8(value.Elem())
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if !validContextUTF8(value.Field(i)) {
				return false
			}
		}
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return utf8.Valid(value.Bytes())
		}
		fallthrough
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !validContextUTF8(value.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if !validContextUTF8(iter.Key()) || !validContextUTF8(iter.Value()) {
				return false
			}
		}
	}
	return true
}

func observedContextReserve(value chat.ContextUsageObservation) int {
	// Input includes cached tokens (all adapters normalize this). Reserve 20%
	// or 1024 tokens, whichever is larger. Never grant more than a fourfold
	// character-accounting credit even for unusually small gateway usage.
	return max(value.InputTokens+max(1024, value.InputTokens/5), (value.CharacterEstimate+3)/4)
}

func observedRequestTokens(request model.ChatRequest, value chat.ContextUsageObservation) int {
	if requestHasImages(request) || !requestHasValidUTF8(request) {
		return estimateRequestTokens(request)
	}
	remaining := make(map[string]int, len(value.Components))
	for key, count := range value.Components {
		remaining[key] = count
	}
	used, matched := observedContextReserve(value), false
	for _, component := range requestUsageComponents(request) {
		if remaining[component.key] > 0 {
			remaining[component.key]--
			matched = matched || component.content
			continue
		}
		// No blanket ratio: changed/new content is charged by UTF-8 bytes plus
		// an envelope allowance. The full measured pool remains reserved even
		// when some old components disappear, so partial overlap cannot give
		// new scientific content the old English/JSON/ciphertext discount.
		used += component.bytes + 32
	}
	if !matched {
		return estimateRequestTokens(request)
	}
	return used
}

func currentRequestBudgetTokens(request model.ChatRequest, observation *chat.ContextUsageObservation) int {
	if observation == nil {
		return estimateRequestTokens(request)
	}
	return observedRequestTokens(request, *observation)
}
