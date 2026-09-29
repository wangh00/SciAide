package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const ResearchFullTextReadName = "builtin.research.full_text.read"

type ResearchFullTextRead struct{ service *research.DiscoveryService }

func NewResearchFullTextRead(s *research.DiscoveryService) *ResearchFullTextRead {
	return &ResearchFullTextRead{service: s}
}
func (*ResearchFullTextRead) Definition(context.Context) (tool.Definition, error) {
	return tool.ResearchMaterialDefinition(), nil
}
func (t *ResearchFullTextRead) Invoke(ctx context.Context, inv tool.Invocation) (tool.Result, error) {
	var a struct {
		CandidateID string `json:"candidateId"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(inv.Arguments, &a); err != nil {
		return tool.Result{}, err
	}
	v, err := t.service.ReadCandidateFullText(ctx, inv.ProjectID, inv.ResearchTaskID, a.CandidateID, a.Query)
	if err != nil {
		if ctx.Err() != nil {
			return tool.Result{}, ctx.Err()
		}
		return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("该文献全文核验未完成（%s）：%s；保留已有摘要与证据限制。", a.CandidateID, err.Error()))
	}
	data, _ := json.Marshal(v)
	return tool.Result{Status: tool.ResultSuccess, Text: string(data), Structured: data, Citations: []tool.CitationRef{}, Artifacts: []tool.ArtifactRef{}}, nil
}
