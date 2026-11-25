package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type discoveryReadArgs struct {
	QueryID     string `json:"queryId"`
	CandidateID string `json:"candidateId"`
	Field       string `json:"field"`
	Offset      int    `json:"offset"`
	Limit       int    `json:"limit"`
}

func parseDiscoveryReadArgs(raw json.RawMessage) (discoveryReadArgs, error) {
	var args discoveryReadArgs
	// The schema is flat. Reject duplicate selectors rather than silently
	// choosing a different query/candidate/field from the original arguments.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return args, err
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return args, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return args, fmt.Errorf("检索读取参数包含重复或无效字段")
		}
		seen[key] = true
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return args, err
		}
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, err
	}
	if args.CandidateID != "" && args.QueryID == "" || args.Field != "" && args.CandidateID == "" {
		return args, fmt.Errorf("字段读取必须同时提供原查询和候选身份")
	}
	if args.CandidateID == "" && args.Limit > 3 {
		return args, fmt.Errorf("查询/候选索引每页最多3项；字段读取才使用字符范围")
	}
	return args, nil
}
