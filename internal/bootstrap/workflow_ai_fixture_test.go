package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

const workflowAITestModelID = "workflow-ai-fixture"

type workflowAITestPublisher struct{}

func (workflowAITestPublisher) Publish(context.Context, events.Envelope) {}

func newWorkflowAITestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		prompt := workflowAITestPrompt(body)
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Providers are free to format otherwise identical JSON differently.
		// Keep the fixture indented so Workflow output verification exercises
		// semantic JSON equality instead of byte-for-byte whitespace equality.
		fixture := workflowAISchemaFixture(schema)
		if object, ok := fixture.(map[string]any); ok {
			if _, review := object["reviewedInputSha256"]; review {
				object["reviewedInputSha256"] = workflowAITestStageInputSHA256(prompt)
				object["approved"] = true
				applyResearchAcceptanceFixture(object, prompt)
			}
		}
		reference := workflowAIReissuedCitation(prompt)
		if object, ok := fixture.(map[string]any); ok && reference != "" {
			if summary, exists := object["summary"].(string); exists {
				object["summary"] = summary + " " + reference
			}
		}
		output, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		answer := "本地测试模型已依据冻结输入生成结构化科研阶段结果。"
		if reference != "" {
			answer += " " + reference
		}
		answer += "\n```json\n" + string(output) + "\n```"
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": nil}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		finished, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 32, "completion_tokens": 16},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finished)
	}))
	t.Cleanup(server.Close)
	return server
}

func workflowAIReissuedCitation(prompt string) string {
	start := strings.LastIndex(prompt, "<trusted_workflow_citations>")
	if start < 0 {
		return ""
	}
	value := prompt[start:]
	if end := strings.Index(value, "</trusted_workflow_citations>"); end >= 0 {
		value = value[:end]
	}
	for marker := 0; marker+16 <= len(value); marker++ {
		if value[marker:marker+3] != "[K-" || value[marker+15] != ']' {
			continue
		}
		valid := true
		for _, character := range value[marker+3 : marker+15] {
			if !((character >= '0' && character <= '9') || (character >= 'A' && character <= 'F')) {
				valid = false
				break
			}
		}
		if valid {
			return value[marker : marker+16]
		}
	}
	return ""
}

func applyResearchAcceptanceFixture(object map[string]any, prompt string) {
	if _, enabled := object["acceptanceChecks"]; !enabled {
		return
	}
	start := strings.LastIndex(prompt, "<stage_input>")
	if start < 0 {
		return
	}
	input := prompt[start+len("<stage_input>"):]
	end := strings.Index(input, "</stage_input>")
	if end < 0 {
		return
	}
	var stage struct {
		Criteria []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"acceptanceCriteria"`
	}
	if json.Unmarshal([]byte(input[:end]), &stage) != nil {
		return
	}
	checks := make([]any, 0, len(stage.Criteria))
	for _, criterion := range stage.Criteria {
		checks = append(checks, map[string]any{"criterionId": criterion.ID, "status": "met", "basis": "测试桩依据冻结输入逐项核对：" + criterion.Text})
	}
	object["acceptanceChecks"] = checks
}

func workflowAITestStageInputSHA256(prompt string) string {
	const marker = "The trusted SHA-256 of the exact stage_input JSON is "
	start := strings.LastIndex(prompt, marker)
	if start < 0 {
		return strings.Repeat("0", 64)
	}
	value := prompt[start+len(marker):]
	if len(value) < 64 {
		return strings.Repeat("0", 64)
	}
	return value[:64]
}

// Dynamic research reviews carry a durable finding ledger. The fixture mirrors
// that contract: a later clean review closes prior findings explicitly instead
// of returning an empty array and losing the revision audit.
func applyTrackedReviewFixture(output map[string]any, prompt string) {
	if _, enabled := output["reviewFindings"]; !enabled {
		return
	}
	input := workflowAITestStageInput(prompt)
	history, _ := input["reviewHistory"].(map[string]any)
	prefix, _ := history["issuePrefix"].(string)
	if prefix == "" {
		prefix = "issue-1-"
	}
	previous, _ := history["findings"].([]any)
	issues := workflowAITestReviewIssues(output)
	// Older bootstrap callers expressed a rejection only through
	// requiredCorrections. Keep those fixtures meaningful under the tracked
	// contract by projecting each such correction into a method issue.
	if len(issues) == 0 && output["approved"] == false {
		for index, correction := range workflowAITestStrings(output["requiredCorrections"]) {
			issues["methodIssues\x00"+correction] = index
		}
		if len(issues) > 0 {
			methodIssues := workflowAITestStrings(output["methodIssues"])
			for _, correction := range workflowAITestStrings(output["requiredCorrections"]) {
				methodIssues = append(methodIssues, correction)
			}
			output["methodIssues"] = methodIssues
		}
	}
	basis := workflowAITestReviewBasis(input)
	findings := make([]any, 0, len(previous)+len(issues))
	seen := map[string]bool{}
	for _, rawPrior := range previous {
		prior, ok := rawPrior.(map[string]any)
		if !ok {
			continue
		}
		id, _ := prior["id"].(string)
		category, _ := prior["category"].(string)
		summary, _ := prior["summary"].(string)
		key := category + "\x00" + summary
		if correction, open := issues[key]; open {
			findings = append(findings, workflowAITestTrackedFinding(id, "open", category, summary, basis, "existing", "当前输入仍显示该问题", correction))
			seen[key] = true
			continue
		}
		findings = append(findings, workflowAITestTrackedFinding(id, "resolved", category, summary, basis, "existing", "当前交付已修正该问题", -1))
	}
	keys := make([]string, 0, len(issues))
	for key := range issues {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for index, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		findings = append(findings, workflowAITestTrackedFinding(fmt.Sprintf("%s%d", prefix, index+1), "open", parts[0], parts[1], basis, "new", "测试桩在本轮首次识别该问题", issues[key]))
	}
	output["reviewFindings"] = findings
	output["suggestions"] = []any{}
	if len(issues) == 0 {
		output["approved"] = true
		output["requiredCorrections"] = []any{}
		output["revisionPlan"] = []any{}
		return
	}
	output["approved"] = false
	corrections := workflowAITestStrings(output["requiredCorrections"])
	if len(corrections) != len(issues) {
		corrections = make([]string, 0, len(issues))
		for _, key := range keys {
			corrections = append(corrections, "修正："+strings.SplitN(key, "\x00", 2)[1])
		}
		output["requiredCorrections"] = corrections
	}
	target := workflowAITestRevisionTarget(input)
	plan := make([]any, 0, len(corrections))
	for index := range corrections {
		plan = append(plan, map[string]any{"correctionIndex": index, "nodeId": target, "reason": "测试桩依据当前审查问题安排返修", "whyNotLater": "需要在上游阶段修正后再生成交付", "needsUserInput": false, "requiredInput": ""})
	}
	output["revisionPlan"] = plan
}

func workflowAITestStageInput(prompt string) map[string]any {
	start := strings.LastIndex(prompt, "<stage_input>")
	if start < 0 {
		return map[string]any{}
	}
	value := prompt[start+len("<stage_input>"):]
	end := strings.Index(value, "</stage_input>")
	if end < 0 {
		return map[string]any{}
	}
	result := map[string]any{}
	_ = json.Unmarshal([]byte(value[:end]), &result)
	return result
}

func workflowAITestReviewIssues(output map[string]any) map[string]int {
	result := map[string]int{}
	correction := 0
	for _, category := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
		for _, summary := range workflowAITestStrings(output[category]) {
			result[category+"\x00"+summary] = correction
			correction++
		}
	}
	return result
}

func workflowAITestStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && text != "" {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

func workflowAITestTrackedFinding(id, status, category, summary string, basis map[string]any, origin, changeReason string, correctionIndex int) map[string]any {
	return map[string]any{"id": id, "status": status, "category": category, "summary": summary, "location": "测试交付稿", "basis": []any{basis}, "reason": "测试桩使用当前冻结输入定位审查问题", "origin": origin, "changeReason": changeReason, "correctionIndex": correctionIndex}
}

func workflowAITestReviewBasis(input map[string]any) map[string]any {
	for _, root := range []string{"context", "researchContract", "methodContext", "designContext", "acceptanceCriteria"} {
		if value, exists := input[root]; exists {
			if pointer, quote, ok := workflowAITestFirstString(value, "/"+root); ok {
				return map[string]any{"pointer": pointer, "quote": quote}
			}
		}
	}
	return map[string]any{"pointer": "/researchContract/query", "quote": "测试"}
}

func workflowAITestFirstString(value any, pointer string) (string, string, bool) {
	switch value := value.(type) {
	case string:
		if value != "" {
			return pointer, value, true
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			if path, quote, ok := workflowAITestFirstString(value[key], pointer+"/"+escaped); ok {
				return path, quote, true
			}
		}
	case []any:
		for index, item := range value {
			if path, quote, ok := workflowAITestFirstString(item, fmt.Sprintf("%s/%d", pointer, index)); ok {
				return path, quote, true
			}
		}
	}
	return "", "", false
}

func workflowAITestRevisionTarget(input map[string]any) string {
	targets, _ := input["revisionTargets"].([]any)
	for _, value := range targets {
		if target, ok := value.(map[string]any); ok {
			if id, ok := target["nodeId"].(string); ok && id != "" {
				return id
			}
		}
	}
	return "method_selection"
}

func newWorkflowAISkillTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"function"`
			} `json:"tools"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil || json.Unmarshal(body, &request) != nil {
			t.Logf("research fixture rejected request: bytes=%d readErr=%v", len(body), err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests++
		requestNumber := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if requestNumber == 1 {
			providerName := ""
			for _, candidate := range request.Tools {
				if strings.Contains(candidate.Function.Description, "Load specialized research instructions") {
					providerName = candidate.Function.Name
					break
				}
			}
			if providerName == "" {
				http.Error(w, "Workflow AI did not receive builtin.skill.load", http.StatusBadRequest)
				return
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"delta":         map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "workflow-skill-call", "function": map[string]any{"name": providerName, "arguments": `{"name":"scientific-writing"}`}}}},
				"finish_reason": "tool_calls",
			}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		hasToolResult := false
		for _, message := range request.Messages {
			hasToolResult = hasToolResult || message.Role == "tool"
		}
		if !hasToolResult {
			http.Error(w, "second request did not include the Skill result", http.StatusBadRequest)
			return
		}
		schema, err := workflowAITestSchema(workflowAITestPrompt(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		output, _ := json.Marshal(workflowAISchemaFixture(schema))
		answer := "Skill-assisted analysis completed.\n```json\n" + string(output) + "\n```"
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": nil}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		finished, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 48, "completion_tokens": 20}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finished)
	}))
	t.Cleanup(server.Close)
	return server
}

func newResearchStarterSkillTestServer(t *testing.T, mutations ...func(map[string]any)) *httptest.Server {
	return newResearchStarterOutputTestServer(t, nil, mutations...)
}

func newResearchStarterOutputTestServer(t *testing.T, mutateOutput func(map[string]any, string), mutations ...func(map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"function"`
			} `json:"tools"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil || json.Unmarshal(body, &request) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		prompt := workflowAITestPrompt(body)
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			t.Logf("research fixture rejected prompt: bytes=%d error=%v", len(body), err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		properties, _ := schema["properties"].(map[string]any)
		_, starterStage := properties["normalizedQuestion"]
		_, methodStage := properties["selectedSkills"]
		_, designStage := properties["status"]
		_, reviewStage := properties["reviewedInputSha256"]
		requiresSkill := starterStage || methodStage || designStage || reviewStage
		hasToolResult := false
		for _, message := range request.Messages {
			hasToolResult = hasToolResult || message.Role == "tool"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(prompt, "<resource_actions>") {
			hasToolResult = resourceFixtureSkillLoaded(body, "scientific-writing")
		}
		if requiresSkill && !hasToolResult {
			providerName := ""
			for _, candidate := range request.Tools {
				if strings.Contains(candidate.Function.Description, "Load specialized research instructions") {
					providerName = candidate.Function.Name
					break
				}
			}
			arguments := `{"name":"scientific-writing"}`
			if name, args, enabled, selectErr := resourceFixtureSelection(body, "scientific-writing", false); enabled {
				if selectErr != nil {
					t.Error(selectErr)
					http.Error(w, selectErr.Error(), 400)
					return
				}
				providerName, arguments = name, args
			}
			if providerName == "" {
				t.Logf("research fixture missing Skill tool: request bytes=%d", len(body))
				http.Error(w, "research stage did not receive builtin.skill.load", http.StatusBadRequest)
				return
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": fmt.Sprintf("research-skill-call-%d", len(request.Messages)), "function": map[string]any{"name": providerName, "arguments": arguments},
				}}},
				"finish_reason": "tool_calls",
			}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		fixture := workflowAISchemaFixture(schema)
		if object, ok := fixture.(map[string]any); ok {
			if starterStage {
				applyScientificWritingRouteFixture(object)
				for _, mutate := range mutations {
					if mutate != nil {
						mutate(object)
					}
				}
			}
			if _, review := object["reviewedInputSha256"]; review {
				object["reviewedInputSha256"] = workflowAITestStageInputSHA256(prompt)
				object["approved"] = true
				applyResearchAcceptanceFixture(object, prompt)
			}
		}
		if object, ok := fixture.(map[string]any); ok && mutateOutput != nil {
			mutateOutput(object, prompt)
		}
		if object, ok := fixture.(map[string]any); ok {
			applyTrackedReviewFixture(object, prompt)
		}
		implementationCode := workflowAITestImplementationCode()
		if object, ok := fixture.(map[string]any); ok {
			if override, exists := object["_fixturePythonCode"].(string); exists {
				implementationCode = override
				delete(object, "_fixturePythonCode")
			}
		}
		output, err := json.Marshal(fixture)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		answer := "Skill-assisted research stage completed.\n```json\n" + string(output) + "\n```"
		if strings.Contains(prompt, "split implementation envelope") {
			answer += "\n```python\n" + implementationCode + "\n```"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": nil}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		finished, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 48, "completion_tokens": 20}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finished)
	}))
	t.Cleanup(server.Close)
	return server
}

func applyScientificWritingRouteFixture(plan map[string]any) {
	plan["selectedSkills"] = []any{map[string]any{
		"name": "scientific-writing", "role": "组织研究方法、设计和独立复核",
		"limitations": []any{"写作与研究组织方法不能替代数据或实证证据"},
	}}
	routes, _ := plan["routes"].([]any)
	for _, rawRoute := range routes {
		route, _ := rawRoute.(map[string]any)
		if stagePlans, ok := route["stagePlans"].([]any); ok {
			for _, rawStage := range stagePlans {
				stage, _ := rawStage.(map[string]any)
				stageID, _ := stage["stageId"].(string)
				if stageID == "method_selection" || stageID == "research_design" || stageID == "independent_review" {
					stage["skillNames"] = []any{"scientific-writing"}
				}
			}
			continue
		}
		layers, _ := route["layers"].([]any)
		for _, rawLayer := range layers {
			layer, _ := rawLayer.(map[string]any)
			stages, _ := layer["stages"].([]any)
			for _, rawStage := range stages {
				stage, _ := rawStage.(map[string]any)
				stageID, _ := stage["stageId"].(string)
				if stageID == "method_selection" || stageID == "research_design" || stageID == "independent_review" {
					stage["skillNames"] = []any{"scientific-writing"}
				}
			}
		}
	}
}

func saveWorkflowAITestProfile(t *testing.T, application *Application, baseURL string) modelprofile.Profile {
	t.Helper()
	profile, err := application.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{
		Name: "Workflow AI fixture", BaseURL: baseURL + "/v1", APIProtocol: modelprofile.ProtocolOpenAIChat,
		ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}},
		TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func workflowAITestPrompt(body []byte) string {
	var request struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil {
		return ""
	}
	var result strings.Builder
	for _, message := range request.Messages {
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			result.WriteString(text)
			result.WriteByte('\n')
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &parts) == nil {
			for _, part := range parts {
				result.WriteString(part.Text)
				result.WriteByte('\n')
			}
		}
	}
	return result.String()
}

func workflowAITestSchema(prompt string) (map[string]any, error) {
	openTag, closeTag := "<output_schema>", "</output_schema>"
	start := strings.LastIndex(prompt, openTag)
	metadataTag := "<metadata_output_schema>"
	if metadataStart := strings.LastIndex(prompt, metadataTag); metadataStart > start {
		openTag, closeTag, start = metadataTag, "</metadata_output_schema>", metadataStart
	}
	if start < 0 {
		return nil, fmt.Errorf("Workflow AI fixture did not receive output schema")
	}
	value := prompt[start+len(openTag):]
	end := strings.Index(value, closeTag)
	if end < 0 {
		return nil, fmt.Errorf("Workflow AI fixture received an unterminated output schema")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(value[:end])), &schema); err != nil {
		return nil, fmt.Errorf("decode Workflow AI fixture schema: %w", err)
	}
	return schema, nil
}

func workflowAITestImplementationCode() string {
	return `import csv
import json
from pathlib import Path

with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
result = {"rowCount": len(rows), "columns": list(rows[0].keys()) if rows else []}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\n\nRead the frozen CSV and counted its rows.", encoding="utf-8")
result`
}

func workflowAISchemaFixture(schema map[string]any) any {
	if value, exists := schema["const"]; exists {
		return value
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	switch schema["type"] {
	case "object":
		if properties, ok := schema["properties"].(map[string]any); ok {
			if _, implementation := properties["analysisInput"]; implementation {
				result := map[string]any{"methodSummary": "Read the frozen CSV with the Python standard library.", "dependencies": []any{}, "analysisInput": map[string]any{}, "researchChanges": []any{}, "expectedOutputs": []any{"JSON result", "Markdown methods"}, "assumptions": []any{}, "limitations": []any{}}
				if _, hasCode := properties["code"]; hasCode {
					result["code"] = workflowAITestImplementationCode()
				}
				return result
			}
			if _, implementation := properties["code"]; implementation {
				return map[string]any{
					"methodSummary":   "Read the frozen CSV with the Python standard library.",
					"researchChanges": []any{},
					"dependencies":    []any{}, "analysisInput": map[string]any{}, "code": workflowAITestImplementationCode(),
					"expectedOutputs": []any{"JSON result", "Markdown methods"}, "assumptions": []any{}, "limitations": []any{},
				}
			}
			if _, report := properties["markdown"]; report {
				result := map[string]any{
					"markdown":     "# Frozen empirical report\n\nThe audited Python analysis completed and produced a frozen row count.\n\n## Limitations\n\nNo external literature evidence was established in this fixture.",
					"claimSummary": []any{"The frozen analysis completed."},
					"limitations":  []any{"No external literature evidence was established."}, "confidence": "medium",
				}
				if _, required := properties["methodSummary"]; required {
					result["methodSummary"] = "Standard-library CSV analysis executed by the SciAide Python Kernel."
				}
				return result
			}
			if _, starter := properties["normalizedQuestion"]; starter {
				if _, dynamic := properties["selectedSkills"]; !dynamic {
					return nil
				}
				semanticStage := func(stageID, objective string) map[string]any {
					return map[string]any{"stageId": stageID, "objective": objective, "methods": []any{}, "skillNames": []any{}}
				}
				semanticRoute := func(id, title, reason string, available bool, resources, deliverables, blockers []any, stages ...map[string]any) map[string]any {
					plans := make([]any, 0, len(stages))
					for _, stage := range stages {
						plans = append(plans, stage)
					}
					return map[string]any{"routeId": id, "title": title, "reason": reason, "availableNow": available, "requiredResources": resources, "deliverables": deliverables, "blockers": blockers, "stagePlans": plans}
				}
				return map[string]any{
					"normalizedQuestion": "测试研究问题", "researchType": "mixed", "selectedSkills": []any{},
					"availableResources": []any{}, "missingInformation": []any{"尚未提供表格数据"},
					"routes": []any{
						semanticRoute("design_first", "动态研究设计", "当前没有数据，先形成可执行且可审查的研究设计", true, []any{}, []any{"课题专属研究设计"}, []any{}, semanticStage("question_refinement", "冻结问题和交付边界"), semanticStage("method_selection", "综合适用科研方法"), semanticStage("research_design", "形成可执行设计"), semanticStage("independent_review", "独立审查设计"), semanticStage("delivery_gate", "确定性验证审查")),
						semanticRoute("tabular_analysis_when_ready", "动态数据分析", "获得课题相关表格后按方法蓝图实现和执行分析", false, []any{"与课题相关的 CSV、TSV 或 XLSX"}, []any{"可复现分析结果"}, []any{"尚未提供课题相关表格数据"}, semanticStage("question_refinement", "冻结问题和交付边界"), semanticStage("method_selection", "综合适用科研方法"), semanticStage("data_preflight", "冻结并预检数据"), semanticStage("method_implementation", "实现课题专属方法"), semanticStage("dependency_preparation", "准备项目依赖"), semanticStage("python_analysis", "执行冻结代码"), semanticStage("result_interpretation", "解释结果和限制"), semanticStage("independent_review", "独立审查结果"), semanticStage("delivery_gate", "确定性验证审查")),
					},
					"recommendedRouteId": "design_first", "recommendationReason": "当前无数据，先动态形成方法蓝图和研究设计", "confidence": "medium", "limitations": []any{"本次没有适用的已加载 Skill；尚未完成实证检验"},
				}
			}
		}
		result := map[string]any{}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, rawName := range required {
			name, _ := rawName.(string)
			property, _ := properties[name].(map[string]any)
			result[name] = workflowAISchemaFixture(property)
		}
		return result
	case "array":
		count := 0
		if minimum, ok := schema["minItems"].(float64); ok {
			count = int(minimum)
		}
		result := make([]any, 0, count)
		item, _ := schema["items"].(map[string]any)
		for range count {
			result = append(result, workflowAISchemaFixture(item))
		}
		return result
	case "integer":
		return 1
	case "number":
		return 1.0
	case "boolean":
		return false
	default:
		return "测试模型基于冻结输入生成的结果"
	}
}

func TestAutonomousWorkflowAgentStageRoutesAndSnapshotsSkill(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAISkillTestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Autonomous Skill stage"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: projectValue.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Autonomous Skill analysis",
		Nodes: []workflow.Node{{
			ID: "review", Name: "Autonomous review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`),
			PromptVersion: "autonomous-skill-v1", Prompt: "Use a relevant scientific writing Skill to review the frozen evidence.",
			SkillRouting: true, ReviewPolicy: workflow.AIReviewAuto,
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`),
		}},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "review", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{
		ProjectID: projectValue.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`),
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 10*time.Second)
	if len(detail.Steps) != 1 || detail.Steps[0].Status != workflow.StepCompleted || len(detail.AIExecutions) != 1 {
		t.Fatalf("autonomous Workflow result = %#v", detail)
	}
	chatRunID := detail.AIExecutions[0].ChatRunID
	snapshot, err := application.ChatFacade.GetRunSnapshot(chatRunID)
	if err != nil || !snapshot.WorkflowAI {
		t.Fatalf("Workflow AI chat snapshot = %#v, %v", snapshot, err)
	}
	var routingCount, auditCount, loadedCount, completedToolCalls int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM run_skill_routing WHERE run_id=?`, chatRunID).Scan(&routingCount); err != nil {
		t.Fatal(err)
	}
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM run_skill_routing_audits WHERE run_id=?`, chatRunID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM run_dynamic_skills WHERE run_id=? AND skill_name='scientific-writing' AND length(content_hash)=64 AND length(package_hash)=64 AND length(instruction_snapshot)>0`, chatRunID).Scan(&loadedCount); err != nil {
		t.Fatal(err)
	}
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND tool_name='builtin.skill.load' AND status='completed'`, chatRunID).Scan(&completedToolCalls); err != nil {
		t.Fatal(err)
	}
	if routingCount != 1 || auditCount != 1 || loadedCount != 1 || completedToolCalls != 1 {
		t.Fatalf("Skill audit counts = routing:%d audit:%d loaded:%d tool:%d", routingCount, auditCount, loadedCount, completedToolCalls)
	}
}

func TestLegacyWorkflowAgentStageStillRequiresHumanReview(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, _, detail := startWorkflowAIRecoveryFixture(t, application, profile)
	if detail.Run.Status != workflow.RunWaitingHumanConfirmation || detail.Steps[0].Status != workflow.StepWaitingHumanConfirmation {
		t.Fatalf("legacy Agent Stage no longer preserves its human checkpoint: %#v", detail)
	}
	chatGate := sqlite.NewRunRepository(application.store.DB())
	if blocked, err := chatGate.BlocksOrdinaryChatForWorkflowConversation(context.Background(), detail.Run.ConversationID); err != nil || blocked {
		t.Fatalf("legacy Agent Stage review did not open its revision conversation: blocked=%v err=%v", blocked, err)
	}
	if _, err := application.WorkflowFacade.Cancel(projectValue.ID, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	waitForWorkflowState(t, application, projectValue.ID, detail.Run.ID, workflow.RunCancelled, 10*time.Second)
	if blocked, err := chatGate.BlocksOrdinaryChatForWorkflowConversation(context.Background(), detail.Run.ConversationID); err != nil || blocked {
		t.Fatalf("terminal research conversation remained blocked: blocked=%v err=%v", blocked, err)
	}
}

func TestWorkflowDeletionRemovesAIConversationGraph(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	project, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "AI deletion lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{
		SchemaVersion: 1, Name: "AI lifecycle",
		Nodes: []workflow.Node{{
			ID: "review", Name: "AI review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`),
			PromptVersion: "delete-fixture-v1", Prompt: "Review the frozen stage input.",
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`),
		}},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "review", FromPort: "analysis", Required: true}},
	}
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{
		ProjectID: project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`),
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitForWorkflowState(t, application, project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	if len(detail.AIExecutions) != 1 || detail.AIExecutions[0].ChatRunID == "" {
		t.Fatalf("Workflow AI graph = %#v", detail.AIExecutions)
	}
	conversationID, chatRunID, executionID := detail.Run.ConversationID, detail.AIExecutions[0].ChatRunID, detail.AIExecutions[0].ID
	if err := application.WorkflowFacade.Delete(project.ID, saved.Workflow.ID); err == nil {
		t.Fatal("active Agent Stage Workflow was deleted without cancellation")
	}
	if _, err := application.WorkflowFacade.Cancel(project.ID, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	waitForWorkflowState(t, application, project.ID, detail.Run.ID, workflow.RunCancelled, 10*time.Second)
	if err := application.WorkflowFacade.Delete(project.ID, saved.Workflow.ID); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{
		"workflows": saved.Workflow.ID, "workflow_runs": detail.Run.ID, "conversations": conversationID,
		"runs": chatRunID, "workflow_ai_executions": executionID,
	} {
		var count int
		if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("deleted AI lifecycle retained %s/%s: count=%d err=%v", table, id, count, err)
		}
	}
	var messages, bindings int
	_ = application.store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=?`, conversationID).Scan(&messages)
	_ = application.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_ai_chat_runs WHERE chat_run_id=?`, chatRunID).Scan(&bindings)
	if messages != 0 || bindings != 0 {
		t.Fatalf("deleted AI lifecycle retained messages=%d bindings=%d", messages, bindings)
	}
}

func TestWorkflowRunDeletionRemovesOnlySelectedAIConversationGraph(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	project, saved, selected := startWorkflowAIRecoveryFixture(t, application, profile)
	if err := application.WorkflowFacade.DeleteRun(project.ID, selected.Run.ID); err == nil {
		t.Fatal("active Agent Stage task was deleted without cancellation")
	}
	if _, err := application.WorkflowFacade.Cancel(project.ID, selected.Run.ID); err != nil {
		t.Fatal(err)
	}
	selected = waitForWorkflowState(t, application, project.ID, selected.Run.ID, workflow.RunCancelled, 10*time.Second)
	second, err := application.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	secondDetail := waitForWorkflowState(t, application, project.ID, second.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	conversationID, chatRunID, executionID := selected.Run.ConversationID, selected.AIExecutions[0].ChatRunID, selected.AIExecutions[0].ID
	if err := application.WorkflowFacade.DeleteRun(project.ID, selected.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.WorkflowFacade.Get(project.ID, saved.Workflow.ID); err != nil {
		t.Fatalf("single task deletion removed its Workflow: %v", err)
	}
	if _, err := application.WorkflowFacade.GetRun(project.ID, secondDetail.Run.ID); err != nil {
		t.Fatalf("single task deletion removed another task: %v", err)
	}
	for table, id := range map[string]string{
		"workflow_runs": selected.Run.ID, "conversations": conversationID, "runs": chatRunID, "workflow_ai_executions": executionID,
	} {
		var count int
		if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("deleted Workflow task retained %s/%s: count=%d err=%v", table, id, count, err)
		}
	}
	var messages, bindings int
	_ = application.store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=?`, conversationID).Scan(&messages)
	_ = application.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_ai_chat_runs WHERE chat_run_id=?`, chatRunID).Scan(&bindings)
	if messages != 0 || bindings != 0 {
		t.Fatalf("deleted Workflow task retained messages=%d bindings=%d", messages, bindings)
	}
}

func TestProjectArchiveRestoresCompletedWorkflowAIExecutionGraph(t *testing.T) {
	root := t.TempDir()
	application, _, profile := newWorkflowAIRecoveryApplication(t, root)
	defer application.Close()
	projectValue, saved, source := startWorkflowAIRecoveryFixture(t, application, profile)
	completed, err := application.WorkflowFacade.Decide(workflow.HumanDecisionCommand{
		ProjectID: projectValue.ID, RunID: source.Run.ID, StepID: source.Steps[0].ID, Approved: true, Note: "核验并采用 AI 阶段结果",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed = waitForWorkflowState(t, application, projectValue.ID, completed.Run.ID, workflow.RunCompleted, 10*time.Second)
	if len(completed.AIExecutions) != 1 || completed.AIExecutions[0].Status != "completed" {
		t.Fatalf("source Workflow AI execution = %#v", completed.AIExecutions)
	}
	sourceExecution := completed.AIExecutions[0]
	projects := project.NewService(sqlite.NewProjectRepository(application.store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	archives, err := projectarchive.NewService(sqlite.NewProjectArchiveRepository(application.store.DB()), projects, filepath.Join(root, "data", "workspaces"), filepath.Join(root, "cache", "project-archives"), filepath.Join(root, "backups", "trash"), "workflow-ai-test")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "workflow-ai.sciaide-project")
	if _, err := archives.Export(context.Background(), projectValue.ID, archivePath); err != nil {
		t.Fatal(err)
	}
	restored, err := archives.Restore(context.Background(), projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	restoredWorkflows, err := application.WorkflowFacade.List(restored.Project.ID)
	if err != nil || len(restoredWorkflows) != 1 || restoredWorkflows[0].ID == saved.Workflow.ID {
		t.Fatalf("restored Workflow list = %#v, %v", restoredWorkflows, err)
	}
	restoredRuns, err := application.WorkflowFacade.ListRuns(restored.Project.ID, restoredWorkflows[0].ID, 10)
	if err != nil || len(restoredRuns) != 1 || restoredRuns[0].ID == completed.Run.ID {
		t.Fatalf("restored Workflow Runs = %#v, %v", restoredRuns, err)
	}
	restoredDetail, err := application.WorkflowFacade.GetRun(restored.Project.ID, restoredRuns[0].ID)
	if err != nil || len(restoredDetail.AIExecutions) != 1 {
		t.Fatalf("restored Workflow AI detail = %#v, %v", restoredDetail, err)
	}
	restoredExecution := restoredDetail.AIExecutions[0]
	if restoredDetail.Run.Status != workflow.RunCompleted || restoredDetail.Run.ConversationID == completed.Run.ConversationID || restoredExecution.ID == sourceExecution.ID || restoredExecution.ChatRunID == sourceExecution.ChatRunID || restoredExecution.WorkflowStepID == sourceExecution.WorkflowStepID {
		t.Fatalf("restored Workflow AI identities = run:%#v execution:%#v", restoredDetail.Run, restoredExecution)
	}
	if restoredExecution.Status != "completed" || restoredExecution.OutputSHA256 != sourceExecution.OutputSHA256 || restoredExecution.PromptSHA256 != sourceExecution.PromptSHA256 || restoredExecution.InputSHA256 != sourceExecution.InputSHA256 {
		t.Fatalf("restored Workflow AI reproducibility snapshot = %#v", restoredExecution)
	}
	var chatRunConversation string
	if err := application.store.DB().QueryRow(`SELECT conversation_id FROM runs WHERE id=?`, restoredExecution.ChatRunID).Scan(&chatRunConversation); err != nil || chatRunConversation != restoredDetail.Run.ConversationID {
		t.Fatalf("restored AI Chat Run conversation = %q, %v", chatRunConversation, err)
	}
	var foreignKeys int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("Workflow AI archive foreign keys = %d, %v", foreignKeys, err)
	}
}

func TestProjectDeletionRemovesTerminalWorkflowAIConversationGraph(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, _, detail := startWorkflowAIRecoveryFixture(t, application, profile)
	if _, err := application.ProjectFacade.RemoveProject(projectValue.ID); err == nil {
		t.Fatal("project with active Agent Stage was deleted")
	}
	if _, err := application.WorkflowFacade.Cancel(projectValue.ID, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	detail = waitForWorkflowState(t, application, projectValue.ID, detail.Run.ID, workflow.RunCancelled, 10*time.Second)
	conversationID, chatRunID, executionID := detail.Run.ConversationID, detail.AIExecutions[0].ChatRunID, detail.AIExecutions[0].ID
	if _, err := application.ProjectFacade.RemoveProject(projectValue.ID); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{
		"projects": projectValue.ID, "workflow_runs": detail.Run.ID, "conversations": conversationID,
		"runs": chatRunID, "workflow_ai_executions": executionID,
	} {
		var count int
		if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("deleted AI project retained %s/%s: count=%d err=%v", table, id, count, err)
		}
	}
	var foreignKeys int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("AI project deletion foreign keys = %d, %v", foreignKeys, err)
	}
}

func TestWorkflowAIRecoveryCommitsCompletedChatCheckpoint(t *testing.T) {
	root := t.TempDir()
	application, server, profile := newWorkflowAIRecoveryApplication(t, root)
	project, saved, detail := startWorkflowAIRecoveryFixture(t, application, profile)

	if _, err := application.store.DB().Exec(`UPDATE workflow_runs SET status='running',completed_at=NULL,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`UPDATE workflow_steps SET status='running',output_json='{}',completed_at=NULL,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), detail.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`UPDATE workflow_ai_executions SET status='running',output_text='',output_json='{}',output_sha256='',completed_at=NULL,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), detail.AIExecutions[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.Startup(context.Background())
	recovered := waitForWorkflowState(t, restarted, project.ID, detail.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	if len(recovered.AIExecutions) != 1 || recovered.AIExecutions[0].Status != "completed" || recovered.AIExecutions[0].OutputSHA256 == "" {
		t.Fatalf("recovered completed AI checkpoint = %#v", recovered.AIExecutions)
	}
	if recovered.Steps[0].Attempt != 1 || recovered.Steps[0].Status != workflow.StepWaitingHumanConfirmation {
		t.Fatalf("recovered Agent Stage = %#v", recovered.Steps[0])
	}
	if err := restarted.WorkflowFacade.Delete(project.ID, saved.Workflow.ID); err == nil {
		t.Fatal("active recovered Workflow was deleted")
	}
	server.Close()
}

func TestWorkflowAIFailurePersistsEmptyStructuredOutputAsObject(t *testing.T) {
	application, _, profile := newWorkflowAIRecoveryApplication(t, t.TempDir())
	defer application.Close()
	_, _, detail := startWorkflowAIRecoveryFixture(t, application, profile)
	if len(detail.AIExecutions) != 1 {
		t.Fatalf("Workflow AI executions = %#v", detail.AIExecutions)
	}
	execution := detail.AIExecutions[0]
	now := time.Now().UTC()
	if _, err := application.store.DB().Exec(`UPDATE workflow_ai_executions SET status='running',output_text='',output_json='{}',output_sha256='',completed_at=NULL,updated_at=? WHERE id=?`, now.Format(time.RFC3339Nano), execution.ID); err != nil {
		t.Fatal(err)
	}
	execution.Status = "failed"
	execution.Output = nil
	execution.OutputSHA256 = ""
	execution.ErrorCode = "CONTEXT_BUILD_FAILED"
	execution.ErrorMessage = "context fixture"
	execution.UpdatedAt = now
	execution.CompletedAt = &now
	if err := sqlite.NewWorkflowRuntimeRepository(application.store.DB()).FinishAIExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	var status, output, code string
	if err := application.store.DB().QueryRow(`SELECT status,output_json,error_code FROM workflow_ai_executions WHERE id=?`, execution.ID).Scan(&status, &output, &code); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || output != "{}" || code != "CONTEXT_BUILD_FAILED" {
		t.Fatalf("terminal Workflow AI execution = status:%q output:%q code:%q", status, output, code)
	}
}

func TestWorkflowAIRetryTerminalizesAStaleRunningAttempt(t *testing.T) {
	application, _, profile := newWorkflowAIRecoveryApplication(t, t.TempDir())
	defer application.Close()
	project, _, detail := startWorkflowAIRecoveryFixture(t, application, profile)
	if len(detail.AIExecutions) != 1 || len(detail.Steps) != 1 {
		t.Fatalf("Workflow AI fixture = %#v", detail)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := application.store.DB().Exec(`UPDATE workflow_runs SET status='failed',error_code='CONTEXT_BUILD_FAILED',error_message='context fixture',completed_at=?,updated_at=? WHERE id=?`, now, now, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`UPDATE workflow_steps SET status='failed',error_code='CONTEXT_BUILD_FAILED',error_message='context fixture',completed_at=?,updated_at=? WHERE id=?`, now, now, detail.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`UPDATE workflow_ai_executions SET status='running',output_text='',output_json='{}',output_sha256='',error_code='',error_message='',completed_at=NULL,updated_at=? WHERE id=?`, now, detail.AIExecutions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: detail.Run.ID, StepID: detail.Steps[0].ID, Note: "重试上下文构建失败的 AI 阶段"}); err != nil {
		t.Fatal(err)
	}
	retried := waitForWorkflowState(t, application, project.ID, detail.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	if len(retried.AIExecutions) != 2 || retried.AIExecutions[0].Status != "interrupted" || retried.AIExecutions[0].ErrorCode != "WORKFLOW_AI_RETRY_SUPERSEDED" || retried.AIExecutions[1].Status != "completed" {
		t.Fatalf("retried Workflow AI history = %#v", retried.AIExecutions)
	}
}

func TestWorkflowAIRecoveryInterruptsAgentStageUntilExplicitRetry(t *testing.T) {
	root := t.TempDir()
	application, _, profile := newWorkflowAIRecoveryApplication(t, root)
	project, _, detail := startWorkflowAIRecoveryFixture(t, application, profile)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, mutation := range []struct {
		query string
		id    string
	}{
		{`UPDATE workflow_runs SET status='running',completed_at=NULL,updated_at=? WHERE id=?`, detail.Run.ID},
		{`UPDATE workflow_steps SET status='running',output_json='{}',completed_at=NULL,updated_at=? WHERE id=?`, detail.Steps[0].ID},
		{`UPDATE workflow_ai_executions SET status='running',output_text='',output_json='{}',output_sha256='',completed_at=NULL,updated_at=? WHERE id=?`, detail.AIExecutions[0].ID},
		{`UPDATE runs SET status='running',completed_at=NULL,updated_at=? WHERE id=?`, detail.AIExecutions[0].ChatRunID},
	} {
		if _, err := application.store.DB().Exec(mutation.query, now, mutation.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.Startup(context.Background())
	interrupted := waitForWorkflowState(t, restarted, project.ID, detail.Run.ID, workflow.RunInterrupted, 10*time.Second)
	if len(interrupted.AIExecutions) != 1 || interrupted.AIExecutions[0].Status != "interrupted" || interrupted.Steps[0].Status != workflow.StepInterrupted {
		t.Fatalf("interrupted Agent Stage recovery = %#v", interrupted)
	}
	if _, err := restarted.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: detail.Run.ID, StepID: detail.Steps[0].ID, Note: "用户确认重试中断的 AI 阶段"}); err != nil {
		t.Fatal(err)
	}
	retried := waitForWorkflowState(t, restarted, project.ID, detail.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	if len(retried.AIExecutions) != 2 || retried.AIExecutions[0].Attempt != 1 || retried.AIExecutions[0].Status != "interrupted" || retried.AIExecutions[1].Attempt != 2 || retried.AIExecutions[1].Status != "completed" {
		t.Fatalf("explicit Agent Stage retry history = %#v", retried.AIExecutions)
	}
}

func newWorkflowAIRecoveryApplication(t *testing.T, root string) (*Application, *httptest.Server, modelprofile.Profile) {
	t.Helper()
	application, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	return application, server, profile
}

func startWorkflowAIRecoveryFixture(t *testing.T, application *Application, profile modelprofile.Profile) (project.Project, workflow.SaveResult, workflow.RunDetail) {
	t.Helper()
	project, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "AI recovery lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "AI recovery",
		Nodes: []workflow.Node{{
			ID: "review", Name: "AI review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`),
			PromptVersion: "recovery-fixture-v1", Prompt: "Review the frozen stage input.",
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`),
		}},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "review", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitForWorkflowState(t, application, project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	if len(detail.Steps) != 1 || len(detail.AIExecutions) != 1 {
		t.Fatalf("Workflow AI recovery fixture = %#v", detail)
	}
	return project, saved, detail
}
