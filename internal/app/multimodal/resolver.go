package multimodal

import (
	"context"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

// ProtocolResolver creates isolated clients from fallback-channel settings. It
// never reads ordinary conversation model profiles.
type ProtocolResolver struct{}

func NewProtocolResolver() *ProtocolResolver { return &ProtocolResolver{} }

func (*ProtocolResolver) Resolve(_ context.Context, channel Channel, secret []byte) (model.ResolvedChatModel, error) {
	maxTokens := channel.MaxTokens
	protocol := channel.Protocol()
	profile := modelprofile.Profile{
		ID: channelProfileID(channel), Name: channel.Name, ProviderType: modelprofile.ProviderOpenAICompatible,
		APIProtocol: protocol, BaseURL: channel.BaseURL, ModelID: channel.ModelID,
		Models:         []modelprofile.ProfileModel{{ID: channel.ModelID, Enabled: true, IsDefault: true}},
		TimeoutSeconds: channel.TimeoutSeconds, MaxOutputTokens: &maxTokens,
		CustomHeaders: map[string]string{}, Enabled: true,
	}
	var chatModel model.ChatModel
	switch protocol {
	case modelcap.ProtocolOpenAIChat:
		chatModel = openai.New(profile, secret)
	case modelcap.ProtocolOpenAIResponses:
		chatModel = responses.New(profile, secret)
	case modelcap.ProtocolAnthropic:
		chatModel = anthropic.New(profile, secret)
	default:
		return model.ResolvedChatModel{}, fmt.Errorf("unsupported vision fallback protocol %q", protocol)
	}
	return model.ResolvedChatModel{Model: chatModel, APIProtocol: protocol}, nil
}
