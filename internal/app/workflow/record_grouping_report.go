package workflow

import (
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
)

// A deterministic appendix from frozen query decisions, not model-authored
// methods or backfilled guesses about old source records.
func writeRecordedGroupingMethod(b *strings.Builder, q queryRecordAudit) {
	defer writeRecordedIdentityHints(b, q)
	if q.Grouping == nil {
		return
	}
	if _, ok := research.RecordGroupingRulesFor(q.Grouping.Version); !ok {
		return
	}
	fmt.Fprintf(b, "\n**本次检索实际题录归组方法**（`%s`）：%s\n\n已保存 %d 条本次处理决定；%d 条没有此版本决定。复用既有来源身份只证明本次复用，不能恢复旧记录首次归并过程。题录归组不等于独立试验去重。\n", q.Grouping.Version, researchTableText(q.Grouping.Rules), q.Grouping.Recorded, q.Grouping.Missing)
	b.WriteString("\n| 来源记录 ID | 本次处理 | 精确匹配依据或复用目标 |\n| --- | --- | --- |\n")
	for _, group := range q.Groups {
		for _, record := range group.Records {
			d := record.Grouping
			if d == nil || d.Version != q.Grouping.Version || d.CandidateID != group.CandidateID {
				fmt.Fprintf(b, "| %s | 未保存可核决定 | 不从当前标识倒推 |\n", researchTableText(record.ID))
				continue
			}
			action := map[string]string{"reuse_source_record": "复用相同来源记录的既有归属", "match_alias": "精确标识匹配到唯一候选", "create_unmatched": "未命中，新增候选", "create_conflict": "命中歧义或标识冲突，独立保存待核对"}[d.Action]
			if action == "" {
				action = "未知记录动作：" + d.Action
			}
			basis := []string{}
			for _, count := range d.MatchCounts {
				basis = append(basis, fmt.Sprintf("%s:%s 总命中%d，保留%d个判定见证", count.Kind, count.Value, count.Total, count.Retained))
			}
			for _, m := range d.Conflicts {
				basis = append(basis, "冲突现存值 "+m.Kind+":"+m.Value+" → "+m.CandidateID)
			}
			for _, m := range d.Matches {
				basis = append(basis, m.Kind+":"+m.Value+" → "+m.CandidateID)
			}
			if d.Action == "reuse_source_record" {
				basis = []string{record.Source + ":" + record.SourceRecordID + " → " + d.CandidateID}
			}
			if len(basis) == 0 {
				for _, a := range d.ComparedAliases {
					basis = append(basis, "已查 "+a.Kind+":"+a.Value)
				}
				if len(basis) == 0 {
					basis = []string{"无可用匹配标识"}
				}
			}
			fmt.Fprintf(b, "| %s | %s | %s |\n", researchTableText(record.ID), researchTableText(action), researchTableText(strings.Join(basis, "；")))
		}
	}
}

// Only bounded, frozen hints are rendered. They never change the model's
// scientific decision or silently subtract from a reported study count.
func writeRecordedIdentityHints(b *strings.Builder, q queryRecordAudit) {
	if q.Identity == nil || q.Identity.Version != research.RecordIdentityVersion {
		return
	}
	fmt.Fprintf(b, "\n**题录身份与材料类型线索**（`%s`）：仅覆盖本查询已保存题录；%d 个候选保存了线索检查，%d 个缺失。%d 条类型线索、%d 条候选关系条目（两端可分别记录，不是独立关系或研究数）。全部待核实，不自动归并/排除；没有提示不证明独立。补充资料仍可保留，但不能直接另计为独立研究。\n", q.Identity.Version, q.Identity.RecordedCandidates, q.Identity.MissingCandidates, q.Identity.RoleEntries, q.Identity.RelationEntries)
	fmt.Fprintf(b, "\n此附录最多展开16个候选的有界线索，另%d个未展开。各scope只保存有限见证及检测计数/流内容SHA，原题录保留；多查询条目不跨scope去重，不是完整关系并集。\n", q.Identity.OmittedCandidateDetails)
	b.WriteString("\n| 候选 | 待核实线索 | 保存的来源记录 / 目标候选 |\n| --- | --- | --- |\n")
	for _, g := range q.Groups {
		h := g.RecordIdentity
		if h == nil {
			continue
		}
		for _, r := range h.Roles {
			fmt.Fprintf(b, "| %s | %s（来源字段 %s） | %s:%s |\n", researchTableText(g.CandidateID), researchTableText(r.Kind), researchTableText(r.Field), researchTableText(r.Record.SourceID), researchTableText(r.Record.SourceRecordID))
		}
		for _, r := range h.Relations {
			refs := []string{}
			for _, ref := range r.Records {
				refs = append(refs, ref.SourceID+":"+ref.SourceRecordID)
			}
			fmt.Fprintf(b, "| %s | %s（%s） | %s → %s |\n", researchTableText(g.CandidateID), researchTableText(r.Kind), researchTableText(strings.Join(r.Basis, ", ")), researchTableText(strings.Join(refs, "; ")), researchTableText(r.TargetCandidateID))
		}
		if h.Preview != nil && h.Preview.Truncated {
			fmt.Fprintf(b, "| %s | 视图有省略，有界清单内容哈希（排除本地候选ID） | %s |\n", researchTableText(g.CandidateID), h.Preview.SHA256)
		}
	}
}
