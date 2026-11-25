package research

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const MaxDiscoveryPageBytes = 2048
const MaxDiscoveryFieldRunes = 400
const DefaultDiscoveryFieldRunes = 128

type DiscoveryFieldCommand struct {
	ProjectID, ResearchTaskID, QueryID, CandidateID string
	Field                                           string
	Offset, Limit                                   int
}

// Read exactly the normalized preferred record frozen in this query, not a
// current merged bibliography, a fresh Fetch or an automatically imported file.
func (s *DiscoveryService) ReadTaskDiscoveryField(ctx context.Context, c DiscoveryFieldCommand) (json.RawMessage, error) {
	c.ProjectID, c.ResearchTaskID, c.QueryID, c.CandidateID = strings.TrimSpace(c.ProjectID), strings.TrimSpace(c.ResearchTaskID), strings.TrimSpace(c.QueryID), strings.TrimSpace(c.CandidateID)
	if c.Field == "" {
		c.Field = "abstract"
	}
	if c.ProjectID == "" || c.ResearchTaskID == "" || c.QueryID == "" || c.CandidateID == "" || c.Offset < 0 || c.Limit < 0 || c.Limit > MaxDiscoveryFieldRunes || c.Field != "abstract" && c.Field != "title" {
		return nil, fmt.Errorf("需要本任务的原查询、候选和有效题名/摘要读取范围")
	}
	if c.Limit == 0 {
		c.Limit = DefaultDiscoveryFieldRunes
	}
	if _, err := s.projects.Get(ctx, c.ProjectID); err != nil {
		return nil, err
	}
	if err := s.validateTask(ctx, c.ProjectID, c.ResearchTaskID); err != nil {
		return nil, err
	}
	reader, ok := s.repository.(interface {
		TaskDiscoveryCandidate(context.Context, string, string, string, string) (Candidate, error)
	})
	if !ok {
		return nil, fmt.Errorf("原查询候选快照读取未配置")
	}
	candidate, err := reader.TaskDiscoveryCandidate(ctx, c.ProjectID, c.ResearchTaskID, c.QueryID, c.CandidateID)
	if err != nil {
		return nil, err
	}
	return discoveryFieldPage(candidate, c)
}

func discoveryFieldPage(candidate Candidate, c DiscoveryFieldCommand) (json.RawMessage, error) {
	text := candidate.Preferred.Abstract
	if c.Field == "title" {
		text = candidate.Preferred.Title
	}
	runes := []rune(text)
	if c.Offset > len(runes) {
		return nil, fmt.Errorf("读取偏移超出原快照字段范围")
	}
	n := min(c.Limit, len(runes)-c.Offset)
	hash := hashText(text)
	encode := func(count int) ([]byte, error) {
		end := c.Offset + count
		next := -1
		if end < len(runes) {
			next = end
		}
		return json.Marshal(map[string]any{"status": "unverified_discovery_field", "snapshotView": "preferred_record", "queryId": c.QueryID, "candidateId": c.CandidateID, "sourceId": candidate.Preferred.SourceID, "recordId": candidate.Preferred.SourceRecordID, "field": c.Field, "sha256": hash, "available": len(runes) > 0, "totalRunes": len(runes), "offset": c.Offset, "nextOffset": next, "content": string(runes[c.Offset:end])})
	}
	raw, err := encode(n)
	if err != nil {
		return nil, err
	}
	if len(raw) <= MaxDiscoveryPageBytes {
		return raw, nil
	}
	// Full end-of-field was already tried; every remaining candidate has the
	// same continuation form. Fit a contiguous Unicode prefix after escaping.
	low, high := 0, max(0, n-1)
	for low < high {
		mid := low + (high-low+1)/2
		v, e := encode(mid)
		if e != nil {
			return nil, e
		}
		if len(v) <= MaxDiscoveryPageBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	raw, err = encode(low)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxDiscoveryPageBytes || n > 0 && low == 0 {
		return nil, fmt.Errorf("原快照字段身份超出单页预算，不能截断身份或伪造续页")
	}
	return raw, nil
}

// Read the same immutable query snapshots used by the discovery window, never
// the project's latest merged bibliography or an inferred historical source.
func (s *DiscoveryService) ReadTaskDiscoveryPage(ctx context.Context, c CandidateListCommand) (CandidatePage, error) {
	c.ProjectID, c.ResearchTaskID, c.QueryID = strings.TrimSpace(c.ProjectID), strings.TrimSpace(c.ResearchTaskID), strings.TrimSpace(c.QueryID)
	if c.ProjectID == "" || c.ResearchTaskID == "" || c.QueryID == "" || c.Offset < 0 || c.Limit < 1 || c.Limit > 3 {
		return CandidatePage{}, fmt.Errorf("需要本任务的检索记录及有效分页范围（每页1–3项）")
	}
	if _, err := s.projects.Get(ctx, c.ProjectID); err != nil {
		return CandidatePage{}, err
	}
	if err := s.validateTask(ctx, c.ProjectID, c.ResearchTaskID); err != nil {
		return CandidatePage{}, err
	}
	reader, ok := s.repository.(interface {
		TaskDiscoveryPage(context.Context, CandidateListCommand) (CandidatePage, error)
	})
	if !ok {
		return CandidatePage{}, fmt.Errorf("任务检索快照读取未配置")
	}
	c.Sort = "relevance"
	return reader.TaskDiscoveryPage(ctx, c)
}
