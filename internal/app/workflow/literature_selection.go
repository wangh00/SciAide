package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Selection summarizes completed relevance decisions, not scientific sufficiency.
func (s *RuntimeService) finishLiteratureSelection(ctx context.Context, detail RunDetail, step Step, node CompiledNode) (bool, error) {
	if node.ID != "candidate_screening" || node.PromptVersion != literatureScreeningVersion {
		return false, nil
	}
	var input struct {
		Info literatureInput `json:"_literature"`
	}
	if json.Unmarshal(step.Input, &input) != nil || input.Info.Phase != "selection" {
		return false, nil
	}
	if code, err := s.validateRunSnapshots(ctx, detail); err != nil {
		return true, s.failBlockedStep(ctx, detail, code, err)
	}
	if hashJSON(step.Input) != step.InputSHA256 {
		return true, fmt.Errorf("selection input changed")
	}
	info := input.Info
	values, err := s.literature.LiteratureCandidates(ctx, detail.Run.ProjectID, info.State.QueryIDs)
	if err != nil {
		return true, err
	}
	prior, fingerprints := literaturePrior(detail, info.State)
	ids := []string{}
	core, support, excluded := 0, 0, 0
	verify, background := 0, 0
	abstracts := 0
	for _, c := range literatureCandidates(values) {
		if c.Abstract != "" {
			abstracts++
		}
		if automaticLiteratureExclusion(c, frozenLiteratureYears(detail)) != "" {
			excluded++
			continue
		}
		a, ok := prior[c.ID]
		if !ok || fingerprints[c.ID] != hashJSON(mustJSON(c)) {
			return true, fmt.Errorf("selection candidate has no current assessment: %s", c.ID)
		}
		if err := validateLiteratureImportAction(a); err != nil {
			return true, err
		}
		if literatureImportRecommended(a) {
			ids = append(ids, c.ID)
		}
		if a.ImportAction == "verify" {
			verify++
		}
		if a.ImportAction == "background" {
			background++
		}
		switch a.Decision {
		case "core":
			core++
		case "support":
			support++
		case "exclude":
			excluded++
		default:
			return true, fmt.Errorf("invalid relevance decision")
		}
	}
	result := map[string]any{"phase": "coverage", "summary": fmt.Sprintf("初筛完成：直接相关%d篇，待核验%d篇，排除%d篇。推荐清单仅表示值得纳入阅读，不代表研究结论已获证实。", core, support, excluded), "recommendedCandidateIds": ids, "candidateAssessments": []any{}, "coverage": map[string]any{"strength": "limited", "sufficientForClaimedScope": false, "gaps": []string{"纳入后的证据分析尚未执行。"}}, "recommendation": "use_recommendation", "recheckCandidateIds": []string{}, "rechecks": []any{}, "supplementalQueries": []string{}, "findings": []any{}, "uncertainties": []any{}}
	info.Phase = "coverage"
	result["summary"] = fmt.Sprintf("初筛完成：推荐直接相关%d篇、优先核验%d篇；背景保留%d篇不默认导入，排除%d篇。推荐核验不等于已确认可支持结论。", core, verify, background, excluded)
	if len(ids) == 0 {
		result["summary"] = fmt.Sprintf("初筛完成：暂无明确推荐导入项，背景保留%d篇，排除%d篇。未将背景材料自动升级为研究证据。", background, excluded)
	}
	coverage := result["coverage"].(map[string]any)
	coverage["abstractAvailable"] = abstracts
	coverage["metadataOnly"] = len(values) - abstracts
	step.Input = mustJSON(map[string]any{"_literature": info})
	return true, s.completeLiteratureScreening(ctx, detail, step, node, AIExecution{OutputText: ""}, mustJSON(result))
}

func literatureImportRecommended(a literatureAssessment) bool {
	return validateLiteratureImportAction(a) == nil && (a.ImportAction == "direct" || a.ImportAction == "verify")
}

func validateLiteratureImportAction(a literatureAssessment) error {
	valid := a.Decision == "core" && a.ImportAction == "direct" || a.Decision == "support" && (a.ImportAction == "verify" || a.ImportAction == "background") || a.Decision == "exclude" && a.ImportAction == "exclude"
	if !valid || strings.TrimSpace(a.Purpose) == "" {
		return fmt.Errorf("candidate %s requires consistent relevance/importAction and a concrete purpose", a.CandidateID)
	}
	return nil
}
