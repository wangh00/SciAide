package workflow

type literaturePage struct {
	Query  string `json:"query"`
	Source string `json:"source"`
	Offset int    `json:"offset"`
}

func literaturePagesIncomplete(state literatureCheckpoint) bool {
	for _, offset := range state.PageLedger {
		if offset >= 0 {
			return true
		}
	}
	return len(state.Pages) > 0
}

func literatureBudgetMessage(state literatureCheckpoint) string {
	return "已完成计划内每式每源最多20条的首页检索，现有材料尚不足以支持声明的范围；未穷尽数据库，不据此推断整个领域没有相关研究。"
}
