package gateway

import (
	"context"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type ProfileLoader interface {
	Secret(ctx context.Context, profileID string) (modelprofile.Profile, []byte, error)
}

type Resolver struct{ profiles ProfileLoader }

func NewResolver(profiles ProfileLoader) *Resolver { return &Resolver{profiles: profiles} }

func (r *Resolver) Resolve(ctx context.Context, profileID, modelID string) (model.ResolvedChatModel, error) {
	profile, secret, err := r.profiles.Secret(ctx, profileID)
	if err != nil {
		return model.ResolvedChatModel{}, err
	}
	if !profile.Enabled {
		return model.ResolvedChatModel{}, &apperr.Error{Code: "MODEL_PROFILE_DISABLED", UserMessage: "当前模型配置已停用，请选择其他模型。", Cause: fmt.Errorf("model profile is disabled")}
	}
	supported := []modelcap.ReasoningLevel{}
	selected := false
	for _, item := range profile.Models {
		if item.ID == modelID && item.Enabled {
			selected = true
			supported = append([]modelcap.ReasoningLevel(nil), item.ReasoningLevels...)
			break
		}
	}
	if !selected {
		return model.ResolvedChatModel{}, &apperr.Error{Code: "MODEL_NOT_CONFIGURED", UserMessage: "所选模型未在该 API 配置中启用，请重新选择。", Cause: fmt.Errorf("model %q is not enabled for profile", modelID)}
	}
	profile.ModelID = modelID
	return model.ResolvedChatModel{Model: openai.New(profile, secret), SupportedReasoningLevels: supported}, nil
}
