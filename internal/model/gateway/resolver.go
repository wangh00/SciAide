package gateway

import (
	"context"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/openai"
)

type ProfileLoader interface {
	Secret(ctx context.Context, profileID string) (modelprofile.Profile, []byte, error)
}

type Resolver struct{ profiles ProfileLoader }

func NewResolver(profiles ProfileLoader) *Resolver { return &Resolver{profiles: profiles} }

func (r *Resolver) Resolve(ctx context.Context, profileID string) (model.ChatModel, error) {
	profile, secret, err := r.profiles.Secret(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if !profile.Enabled {
		return nil, &apperr.Error{Code: "MODEL_PROFILE_DISABLED", UserMessage: "当前模型配置已停用，请选择其他模型。", Cause: fmt.Errorf("model profile is disabled")}
	}
	return openai.New(profile, secret), nil
}
