package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type taskDiscoveryService interface {
	Search(context.Context, research.DiscoverySearchCommand) (research.DiscoverySearchResult, error)
	ReadTaskDiscoveryPage(context.Context, research.CandidateListCommand) (research.CandidatePage, error)
	QueryHistory(context.Context, research.QueryPageCommand) ([]research.Query, error)
}
type discoveryDiscussionValidator interface {
	ValidateResearchDiscoveryTool(context.Context, string, string, string, string, string) error
}
type ResearchDiscovery struct {
	service taskDiscoveryService
	scope   discoveryDiscussionValidator
	read    bool
}

// A discovery page is navigation, not bulk source content. Bound serialized
// UTF-8 as well as item count, so escaping/wide fields cannot consume later
// tool continuations. The full snapshot remains in the existing repository.
const maxDiscoveryPageBytes = research.MaxDiscoveryPageBytes

func NewResearchDiscoveryTools(service taskDiscoveryService, scope discoveryDiscussionValidator) []tool.Tool {
	return []tool.Tool{&ResearchDiscovery{service: service, scope: scope}, &ResearchDiscovery{service: service, scope: scope, read: true}}
}

func (t *ResearchDiscovery) Definition(ctx context.Context) (tool.Definition, error) {
	if t.read {
		return tool.Definition{QualifiedName: workflow.ResearchDiscoveryReadTool, Version: "1", Risk: tool.RiskLow, Idempotent: true,
			Description: "Read saved discovery, never fetch. No queryId: list queries; queryId: candidate index (limit<=3); queryId+candidateId: exact saved title/abstract field (limit<=400, default128). offset/nextOffset use items for indexes, Unicode for fields; -1 ends. Snapshots remain unverified, not full text or approved evidence.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"queryId":{"type":"string","minLength":1,"maxLength":100},"candidateId":{"type":"string","minLength":1,"maxLength":100},"field":{"type":"string","enum":["title","abstract"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":400}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}, nil
	}
	definition, _ := (&ResearchSearch{}).Definition(ctx)
	definition.QualifiedName, definition.Version = workflow.ResearchDiscoverySearchTool, "1"
	definition.Description = "Search scholarly sources and save this discovery in the current research task, using the existing discovery service. Returns a queryId and up to 3 unverified previews; use research.discovery.read for more saved pages, not repeated searches. Omitted sourceIds exclude explicit-selection-only sources. Saving candidates does not import materials, approve evidence, change the goal or execute a route."
	definition.OutputSchema = json.RawMessage(`{"type":"object"}`)
	return definition, nil
}

func (t *ResearchDiscovery) Invoke(ctx context.Context, inv tool.Invocation) (tool.Result, error) {
	if t == nil || t.service == nil || t.scope == nil || tool.NormalizeSubjectKind(inv.SubjectKind) != tool.SubjectChatRun {
		return tool.Result{}, tool.NewUserFacingError("需要当前科研任务的真实用户讨论和工具调用")
	}
	definition, err := t.Definition(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	if err = (tool.JSONSchemaValidator{}).Validate(definition.InputSchema, inv.Arguments); err != nil {
		return tool.Result{}, err
	}
	var readArgs discoveryReadArgs
	if t.read {
		if readArgs, err = parseDiscoveryReadArgs(inv.Arguments); err != nil {
			return tool.Result{}, err
		}
	}
	if err = t.scope.ValidateResearchDiscoveryTool(ctx, inv.ProjectID, inv.ResearchTaskID, inv.RunID, inv.CallID, definition.QualifiedName); err != nil {
		return tool.Result{}, tool.NewUserFacingError(err.Error())
	}
	command := research.CandidateListCommand{ProjectID: inv.ProjectID, ResearchTaskID: inv.ResearchTaskID, Limit: 3}
	var statuses []research.SourceSearch
	partial := false
	if t.read {
		if readArgs.CandidateID != "" {
			reader, ok := t.service.(interface {
				ReadTaskDiscoveryField(context.Context, research.DiscoveryFieldCommand) (json.RawMessage, error)
			})
			if !ok {
				return tool.Result{}, tool.NewUserFacingError("原查询字段读取未配置")
			}
			raw, err := reader.ReadTaskDiscoveryField(ctx, research.DiscoveryFieldCommand{ProjectID: inv.ProjectID, ResearchTaskID: inv.ResearchTaskID, QueryID: readArgs.QueryID, CandidateID: readArgs.CandidateID, Field: readArgs.Field, Offset: readArgs.Offset, Limit: readArgs.Limit})
			if err != nil {
				return tool.Result{}, tool.NewUserFacingError(err.Error())
			}
			return tool.Result{Status: tool.ResultSuccess, Structured: raw}, nil
		}
		command.QueryID, command.Offset = readArgs.QueryID, readArgs.Offset
		if readArgs.Limit > 0 {
			command.Limit = readArgs.Limit
		}
		if command.QueryID == "" {
			return t.queryIndex(ctx, inv, command.Offset, command.Limit)
		}
	} else {
		var args research.SearchCommand
		if err = json.Unmarshal(inv.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if args.Limit == 0 {
			args.Limit = 10
		}
		result, err := t.service.Search(ctx, research.DiscoverySearchCommand{ProjectID: inv.ProjectID, ResearchTaskID: inv.ResearchTaskID, Query: args.Query, SourceIDs: args.SourceIDs, Limit: args.Limit, SnapshotKey: inv.CallID})
		if err != nil {
			return tool.Result{}, err
		}
		command.QueryID, statuses, partial = result.Query.ID, result.Query.Sources, result.Query.Partial
	}
	page, err := t.service.ReadTaskDiscoveryPage(ctx, command)
	if err != nil {
		return tool.Result{}, tool.NewUserFacingError(err.Error())
	}
	payload := map[string]any{"queryId": command.QueryID, "researchTaskId": inv.ResearchTaskID, "status": "unverified_discovery", "total": page.Total, "offset": page.Offset, "nextOffset": page.Offset, "candidates": []map[string]any{}}
	if !t.read {
		sources := []map[string]any{}
		for _, s := range statuses {
			sources = append(sources, map[string]any{"sourceId": s.SourceID, "status": s.Status, "count": s.Count, "errorCode": s.ErrorCode, "retryable": s.Retryable})
		}
		payload["sources"], payload["partial"] = sources, partial
	}
	raw, err := fitDiscoveryIndex(payload, "candidates", discoveryCandidatePreviews(page.Items), page.Offset, page.Offset+len(page.Items) < page.Total, !t.read)
	if err != nil {
		return tool.Result{}, err
	}
	// The typed status already states the evidence boundary. Do not duplicate
	// a prose receipt in every paged result and consume its navigation budget.
	return tool.Result{Status: tool.ResultSuccess, Structured: raw}, nil
}

func (t *ResearchDiscovery) queryIndex(ctx context.Context, inv tool.Invocation, offset, limit int) (tool.Result, error) {
	queries, err := t.service.QueryHistory(ctx, research.QueryPageCommand{ProjectID: inv.ProjectID, Origin: inv.ResearchTaskID, Offset: offset, Limit: limit + 1})
	if err != nil {
		return tool.Result{}, err
	}
	more := len(queries) > limit
	if len(queries) > limit {
		queries = queries[:limit]
	}
	items := []map[string]any{}
	for _, q := range queries {
		if q.ProjectID != inv.ProjectID || q.ResearchTaskID != inv.ResearchTaskID {
			return tool.Result{}, fmt.Errorf("检索目录归属与当前任务不一致")
		}
		items = append(items, map[string]any{"queryId": q.ID, "query": discoveryTextPreview(q.Text, 120), "sourceIds": q.SourceIDs, "sourceRecordCount": q.ResultCount, "partial": q.Partial, "readableSnapshot": !q.LegacySnapshot, "createdAt": q.CreatedAt})
	}
	raw, err := fitDiscoveryIndex(map[string]any{"researchTaskId": inv.ResearchTaskID, "status": "unverified_discovery_index", "offset": offset}, "queries", items, offset, more, false)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Structured: raw}, nil
}

// Fit whole index entries, not serialized-string fragments. The continuation
// always starts after exactly the entries shown, including on a wide-field page.
func fitDiscoveryIndex(payload map[string]any, key string, items []map[string]any, offset int, more, allowEmpty bool) (json.RawMessage, error) {
	shown := []map[string]any{}
	setNext := func(n int) {
		payload["nextOffset"] = -1
		if more || n < len(items) {
			payload["nextOffset"] = offset + n
		}
	}
	for _, item := range items {
		payload[key] = append(shown, item)
		setNext(len(shown) + 1)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		if len(encoded) > maxDiscoveryPageBytes {
			break
		}
		shown = append(shown, item)
	}
	payload[key] = shown
	setNext(len(shown))
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDiscoveryPageBytes || !allowEmpty && len(items) > 0 && len(shown) == 0 {
		return nil, tool.NewUserFacingError("检索导航元数据超出单页预算；完整记录仍在本任务文献发现中，不以截断身份替代")
	}
	return raw, nil
}

func discoveryTextPreview(text string, limit int) map[string]any {
	runes := []rune(text)
	hash := sha256.Sum256([]byte(text))
	return map[string]any{"text": string(runes[:min(limit, len(runes))]), "totalRunes": len(runes), "omitted": len(runes) > limit, "sha256": hex.EncodeToString(hash[:])}
}
func discoveryCandidatePreviews(candidates []research.Candidate) []map[string]any {
	values := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		title := discoveryTextPreview(c.Preferred.Title, 64)
		delete(title, "sha256")
		abstract := discoveryTextPreview(c.Preferred.Abstract, 160)
		if utf8.RuneCountInString(c.Preferred.Abstract) > 160 {
			abstract["text"] = ""
		} // Long abstracts are content, not navigation.
		values = append(values, map[string]any{"candidateId": c.ID, "sourceId": c.Preferred.SourceID, "recordId": c.Preferred.SourceRecordID, "year": c.Preferred.Year, "title": title, "abstract": abstract})
	}
	return values
}
