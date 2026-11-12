package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// The stored review contract does not change. The model owns scientific text
// and decisions; the transport alone owns array positions and copied lists.
func SupportsIssueReview(schema json.RawMessage) bool {
	if !usesTrackedReview(CompiledNode{OutputSchema: schema}) {
		return false
	}
	s := decodeObject(schema)
	p, ok := s["properties"].(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"reviewFindings", "revisionPlan", "requiredCorrections"} {
		a, ok := p[key].(map[string]any)
		if !ok || a["type"] != "array" {
			return false
		}
		i, ok := a["items"].(map[string]any)
		if !ok {
			return false
		}
		if key == "requiredCorrections" {
			if i["type"] != "string" {
				return false
			}
			continue
		}
		ip, ok := i["properties"].(map[string]any)
		if !ok || i["type"] != "object" {
			return false
		}
		for _, field := range []string{"correctionIndex"} {
			if _, ok := ip[field].(map[string]any); !ok {
				return false
			}
		}
		if key == "reviewFindings" {
			if ip["correction"] != nil || ip["revision"] != nil {
				return false
			}
			for _, field := range []string{"id", "basis", "status"} {
				if _, ok := ip[field].(map[string]any); !ok {
					return false
				}
			}
		}
	}
	return true
}

func withoutRequired(s map[string]any, keys ...string) {
	remove := map[string]bool{}
	for _, key := range keys {
		remove[key] = true
	}
	required := []any{}
	items, _ := s["required"].([]any)
	for _, key := range items {
		name, _ := key.(string)
		if !remove[name] {
			required = append(required, key)
		}
	}
	s["required"] = required
}

func issueReviewSubmissionSchema(schema json.RawMessage) json.RawMessage {
	base := legacyReviewSubmissionSchema(schema)
	if !SupportsIssueReview(schema) {
		return base
	}
	s := decodeObject(base)
	props := s["properties"].(map[string]any)
	item := props["reviewFindings"].(map[string]any)["items"].(map[string]any)
	fp := item["properties"].(map[string]any)
	plan := props["revisionPlan"].(map[string]any)["items"].(map[string]any)
	delete(plan["properties"].(map[string]any), "correctionIndex")
	withoutRequired(plan, "correctionIndex")
	fp["correction"] = props["requiredCorrections"].(map[string]any)["items"]
	fp["revision"] = plan
	delete(fp, "correctionIndex")
	withoutRequired(item, "correctionIndex")
	delete(props, "requiredCorrections")
	delete(props, "revisionPlan")
	withoutRequired(s, "requiredCorrections", "revisionPlan")
	return mustJSON(s)
}

func isIssueReviewSubmission(fields map[string]any, schema json.RawMessage) bool {
	if !SupportsIssueReview(schema) {
		return false
	}
	if _, ok := fields["requiredCorrections"]; !ok {
		return true
	}
	findings, _ := fields["reviewFindings"].([]any)
	for _, raw := range findings {
		f, _ := raw.(map[string]any)
		if _, ok := f["correction"]; ok {
			return true
		}
		if _, ok := f["revision"]; ok {
			return true
		}
	}
	return false
}

func expandIssueReviewSubmission(value, schema json.RawMessage) (json.RawMessage, error) {
	if err := (tool.JSONSchemaValidator{}).Validate(issueReviewSubmissionSchema(schema), value); err != nil {
		return nil, fmt.Errorf("按问题提交的审查结构不完整：%w", err)
	}
	root := decodeObject(value)
	findings := root["reviewFindings"].([]any)
	corrections, plans := []any{}, []any{}
	for _, raw := range findings {
		f := raw.(map[string]any)
		id, _ := f["id"].(string)
		if f["status"] == "open" {
			c, _ := f["correction"].(string)
			p, ok := f["revision"].(map[string]any)
			if strings.TrimSpace(c) == "" || !ok {
				return nil, fmt.Errorf("问题 %s 为 open，须在该问题内提供 correction 和 revision；不要重新生成其它问题", id)
			}
			index := len(corrections)
			f["correctionIndex"] = index
			p["correctionIndex"] = index
			corrections = append(corrections, c)
			plans = append(plans, p)
		} else {
			if _, exists := f["correction"]; exists {
				return nil, fmt.Errorf("问题 %s 已解决或撤回，不能提供 correction", id)
			}
			if _, exists := f["revision"]; exists {
				return nil, fmt.Errorf("问题 %s 已解决或撤回，不能提供 revision", id)
			}
			f["correctionIndex"] = -1
		}
		delete(f, "correction")
		delete(f, "revision")
	}
	root["requiredCorrections"] = corrections
	root["revisionPlan"] = plans
	return mustJSON(root), nil
}

// Convert only an unambiguous existing result. Never choose which plan a bad
// correction index meant, discard surplus advice or fill a missing decision.
func IssueReviewSubmission(value, schema json.RawMessage) (json.RawMessage, error) {
	if !SupportsIssueReview(schema) {
		return nil, fmt.Errorf("not a tracked review with revision plan")
	}
	root := decodeObject(value)
	if isIssueReviewSubmission(root, schema) {
		if err := (tool.JSONSchemaValidator{}).Validate(ReviewSubmissionSchema(schema), value); err != nil {
			return nil, err
		}
		return cloneRaw(value), nil
	}
	// Validate legacy duplicate lists and scalar precision before removing any
	// derived fields. A contradictory old draft must not be silently repaired.
	normalized, _, err := projectReviewSubmission(string(value), CompiledNode{OutputSchema: schema})
	if err != nil {
		return nil, err
	}
	root = decodeObject(normalized)
	var fs []map[string]json.RawMessage
	var cs []json.RawMessage
	var ps []map[string]json.RawMessage
	if json.Unmarshal(mustJSON(root["reviewFindings"]), &fs) != nil || json.Unmarshal(mustJSON(root["requiredCorrections"]), &cs) != nil || json.Unmarshal(mustJSON(root["revisionPlan"]), &ps) != nil {
		return nil, fmt.Errorf("invalid canonical review")
	}
	byIndex := map[int]map[string]json.RawMessage{}
	for _, p := range ps {
		var i int
		if json.Unmarshal(p["correctionIndex"], &i) != nil || i < 0 || i >= len(cs) || byIndex[i] != nil {
			return nil, fmt.Errorf("ambiguous revision index")
		}
		byIndex[i] = p
	}
	used := map[int]bool{}
	for _, f := range fs {
		var status string
		var i int
		_ = json.Unmarshal(f["status"], &status)
		if json.Unmarshal(f["correctionIndex"], &i) != nil {
			return nil, fmt.Errorf("missing correction index")
		}
		if status == "open" {
			if i < 0 || i >= len(cs) || used[i] || byIndex[i] == nil {
				return nil, fmt.Errorf("ambiguous correction mapping")
			}
			used[i] = true
			f["correction"] = cs[i]
			p := byIndex[i]
			delete(p, "correctionIndex")
			f["revision"] = mustJSON(p)
		} else if i != -1 {
			return nil, fmt.Errorf("closed finding still requests correction")
		}
		delete(f, "correctionIndex")
	}
	if len(used) != len(cs) || len(used) != len(ps) {
		return nil, fmt.Errorf("unmapped advice; full review required")
	}
	for _, key := range append(append([]string{}, reviewCategoryFields...), "requiredCorrections", "revisionPlan") {
		delete(root, key)
	}
	root["reviewFindings"] = fs
	result := mustJSON(root)
	return result, (tool.JSONSchemaValidator{}).Validate(issueReviewSubmissionSchema(schema), result)
}
