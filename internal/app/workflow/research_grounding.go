package workflow

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/research"
)

type ResearchTimeContext struct {
	Date             string                    `json:"date"`
	Zone             string                    `json:"zone"`
	RelativeQuote    string                    `json:"relativeQuote,omitempty"`
	PublicationYears research.PublicationYears `json:"publicationYears"`
	Interpretation   string                    `json:"interpretation"`
}

func newResearchTimeContext(text string, now time.Time) *ResearchTimeContext {
	zone, _ := now.Zone()
	v := &ResearchTimeContext{Date: now.Format("2006-01-02"), Zone: zone, Interpretation: "宿主冻结的本轮日期。相对发表时间须以此为锚，不使用模型知识截止日期；用户明确年份或后续确认的范围优先。"}
	resolved := resolvePublicationTime(text, now)
	if !resolved.Resolved {
		return v
	}
	v.PublicationYears = resolved.Years
	if resolved.Relative {
		v.RelativeQuote = resolved.Quote
	}
	v.Interpretation += " 已识别的发表时间与校验共用同一宿主解析：近N年回溯N年，起始年至今/以来以冻结年份结束；原引文不补写推导数字。数据库以发表年份作宽边界筛选（含两端年份），不冒充精确到日的筛选。规划与正文须说明此解释；需要排除当年或采用完整年度时与用户澄清。"
	return v
}

func researchHumanRequests(history []conversation.Message, fallback string) []string {
	result := []string{}
	for _, message := range history {
		if message.Role != conversation.RoleUser || message.Internal {
			continue
		}
		for _, part := range message.Parts {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				result = append(result, part.Text)
				if len(mustJSON(result)) > 48*1024 {
					// Full originals already live in the frozen planning discussion.
					// Do not duplicate megabytes of history into each stage, or
					// label the AI's condensed version as verbatim human requests.
					return nil
				}
			}
		}
	}
	if len(result) == 0 && strings.TrimSpace(fallback) != "" {
		result = append(result, fallback)
	}
	return result
}

func researchHumanTimeContext(history []conversation.Message, now time.Time) *ResearchTimeContext {
	result := newResearchTimeContext("", now)
	for _, message := range history {
		if message.Role != conversation.RoleUser || message.Internal {
			continue
		}
		for _, part := range message.Parts {
			if part.Type == "text" && resolvePublicationTime(part.Text, now).Relevant {
				result = newResearchTimeContext(part.Text, now)
			}
		}
	}
	return result
}

const researchGroundingInstruction = `
userIntent.userRequests 是宿主保存的用户原话；originalRequest/research_goal可能包含AI协商整理，不能把AI自己扩写的愿望当成用户新增承诺。已确认路线和后续用户范围变更仍有效。研究结论存在不确定性，不等于用户任务未完成；除非实际约定，不得把穷尽检索、阅读全文、必须发现专病效应点估计或长期阳性结果新增为必达目标。只把承诺的工作确实未执行记为任务缺项，把已有证据不支持确定结论写为研究结果/局限。不能把“未获取/未核读全文”写成“全文已经尝试但不可得”。
researchTimeContext 是宿主冻结的本轮时间和相对发表年份解释，不是科学证据。用户只说近十年等相对范围时，规划、检索与正文以该锚点为准；不得按模型记忆中的年份随意结束，也不能伪造用户给出了具体年份。`

const researchGroundingLocations = "\n初次规划时原话与日期在 starter_context.userRequests/researchTimeContext；执行阶段在 userIntent/researchTimeContext。长讨论不重复内嵌所有原话；userRequests 缺省且有 discussionRecord/readSection 时通过原 planning_discussion 读取冻结记录，只以非 internal 的 user 消息为用户原话。此时相对时间 yearEvidence 可逐字使用宿主 RelativeQuote。userIntent.confirmedClarifications 是宿主整理的已确认问答（不是逐字初始原话），与后续 _userRevision 优先于初始相对年份解释；不要用初始原话抹掉用户后续合法选择。"

func addResearchGrounding(d RunDetail, args map[string]any) {
	var inputs struct {
		Route struct {
			UserRequests   []string                    `json:"userRequests"`
			Time           *ResearchTimeContext        `json:"researchTimeContext"`
			Clarifications string                      `json:"confirmedClarifications"`
			Discussion     *ResearchPlanningDiscussion `json:"discussionRecord"`
		} `json:"route_context"`
	}
	if json.Unmarshal(d.Run.Inputs, &inputs) != nil {
		return
	}
	if len(inputs.Route.UserRequests) > 0 {
		args["userIntent"] = map[string]any{"userRequests": inputs.Route.UserRequests, "source": "host_saved_user_requests", "confirmedClarifications": inputs.Route.Clarifications, "interpretation": "原始用户表述，与AI整理和已确认路线分开保存；后续明确的用户变更仍有效。"}
	} else if inputs.Route.Time != nil && inputs.Route.Discussion != nil {
		args["userIntent"] = map[string]any{"source": "host_saved_planning_discussion", "readSection": "planning_discussion", "confirmedClarifications": inputs.Route.Clarifications, "interpretation": "原话保留在已冻结讨论记录；未内嵌不表示用户没有要求，不把AI摘要冒充原话。"}
	}
	if inputs.Route.Time != nil {
		args["researchTimeContext"] = inputs.Route.Time
	}
}
