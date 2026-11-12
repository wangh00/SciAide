package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type ProfileLoader interface {
	Secret(ctx context.Context, profileID string) (modelprofile.Profile, []byte, error)
}

type Resolver struct {
	profiles            ProfileLoader
	recorder            modelcap.ReasoningRecorder
	mu                  sync.Mutex
	clients             map[string]cachedProtocolClient
	requestGate         func(context.Context) error
	debugObserver       func(responses.DebugProviderError)
	debugStreamObserver func(responses.DebugStreamFault)
	debugHTTPObserver   func(responses.DebugHTTPResponse)
}

type cachedProtocolClient struct {
	fingerprint [sha256.Size]byte
	client      model.ChatModel
}

func NewResolver(profiles ProfileLoader, gates ...func(context.Context) error) *Resolver {
	resolver := &Resolver{profiles: profiles, clients: make(map[string]cachedProtocolClient)}
	if len(gates) > 0 {
		resolver.requestGate = gates[0]
	}
	if recorder, ok := profiles.(modelcap.ReasoningRecorder); ok {
		resolver.recorder = recorder
	}
	return resolver
}

// SetDebugErrorObserver is only used by explicitly opted-in development runs.
// Configure it before resolving any clients; never mutate shared clients.
func (r *Resolver) SetDebugErrorObserver(observe func(responses.DebugProviderError)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) != 0 {
		return fmt.Errorf("debug observer must be configured before model resolution")
	}
	r.debugObserver = observe
	return nil
}

func (r *Resolver) SetDebugStreamFaultObserver(observe func(responses.DebugStreamFault)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) != 0 {
		return fmt.Errorf("debug observer must be configured before model resolution")
	}
	r.debugStreamObserver = observe
	return nil
}

// Configure before resolving any shared client. Responses-only, default off.
func (r *Resolver) SetDebugHTTPResponseObserver(observe func(responses.DebugHTTPResponse)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) != 0 {
		return fmt.Errorf("HTTP observer must be configured before model resolution")
	}
	r.debugHTTPObserver = observe
	return nil
}

func (r *Resolver) Resolve(ctx context.Context, profileID, modelID string) (model.ResolvedChatModel, error) {
	profile, secret, err := r.profiles.Secret(ctx, profileID)
	if err != nil {
		return model.ResolvedChatModel{}, err
	}
	if !profile.Enabled {
		return model.ResolvedChatModel{}, &apperr.Error{Code: "MODEL_PROFILE_DISABLED", UserMessage: "当前模型配置已停用，请选择其他模型。", Cause: fmt.Errorf("model profile is disabled")}
	}
	protocol := profile.APIProtocol
	if !protocol.Valid() {
		protocol = modelprofile.ProtocolOpenAIChat
	}
	supported := []modelcap.ReasoningLevel{}
	contextBudget := modelcap.ResolveContextBudget(0, 0, "")
	selected := false
	for _, item := range profile.Models {
		if item.ID == modelID && item.Enabled {
			selected = true
			contextBudget = modelcap.ResolveContextBudget(item.ContextWindowTokens, item.AutoCompactTokenLimit, item.ContextWindowSource)
			if item.ReasoningCapabilitySource == "manual" || item.ReasoningCapabilitySource == "provider" || item.ReasoningCapabilitySource == "builtin" {
				supported = modelcap.NormalizeReasoningLevels(item.ReasoningLevels)
			} else {
				// Recompute inferred capabilities on load so installing a newer
				// SciAide immediately unlocks future tiers for existing profiles.
				supported = modelcap.InferredReasoningLevelsForProtocol(protocol, item.ID)
			}
			if item.ReasoningControlUnsupported {
				supported = nil
			} else {
				supported = modelcap.WithoutRejectedReasoningLevels(supported, item.ReasoningRejectedLevels)
			}
			break
		}
	}
	if !selected {
		return model.ResolvedChatModel{}, &apperr.Error{Code: "MODEL_NOT_CONFIGURED", UserMessage: "所选模型未在该 API 配置中启用，请重新选择。", Cause: fmt.Errorf("model %q is not enabled for profile", modelID)}
	}
	profile.ModelID = modelID
	if protocol != modelprofile.ProtocolOpenAIChat && protocol != modelprofile.ProtocolOpenAIResponses && protocol != modelprofile.ProtocolAnthropic {
		return model.ResolvedChatModel{}, &apperr.Error{Code: "MODEL_PROTOCOL_UNSUPPORTED", UserMessage: "当前 API 协议不受支持，请检查模型配置。", Cause: fmt.Errorf("unsupported protocol %q", protocol)}
	}
	chatModel, err := r.clientFor(profile, secret, protocol)
	if err != nil {
		return model.ResolvedChatModel{}, err
	}
	return model.ResolvedChatModel{Model: chatModel, SupportedReasoningLevels: supported, APIProtocol: protocol, ContextBudget: contextBudget}, nil
}

func (r *Resolver) clientFor(profile modelprofile.Profile, secret []byte, protocol modelcap.APIProtocol) (model.ChatModel, error) {
	fingerprintInput, err := json.Marshal(struct {
		BaseURL         string               `json:"baseUrl"`
		Protocol        modelcap.APIProtocol `json:"protocol"`
		ModelID         string               `json:"modelId"`
		TimeoutSeconds  int                  `json:"timeoutSeconds"`
		Temperature     *float64             `json:"temperature,omitempty"`
		MaxOutputTokens *int                 `json:"maxOutputTokens,omitempty"`
		CustomHeaders   map[string]string    `json:"customHeaders"`
		Secret          []byte               `json:"secret"`
	}{profile.BaseURL, protocol, profile.ModelID, profile.TimeoutSeconds, profile.Temperature, profile.MaxOutputTokens, profile.CustomHeaders, secret})
	if err != nil {
		return nil, fmt.Errorf("fingerprint model client: %w", err)
	}
	fingerprint := sha256.Sum256(fingerprintInput)
	key := profile.ID + "\x00" + profile.ModelID + "\x00" + string(protocol)
	if profile.ID == "" {
		key = profile.BaseURL + "\x00" + profile.ModelID + "\x00" + string(protocol)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.clients[key]; ok && existing.fingerprint == fingerprint {
		return existing.client, nil
	}
	var client model.ChatModel
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		client = openai.New(profile, secret, r.recorder)
	case modelprofile.ProtocolOpenAIResponses:
		responseClient := responses.New(profile, secret, r.recorder)
		responseClient.SetDebugErrorObserver(r.debugObserver)
		responseClient.SetDebugStreamFaultObserver(r.debugStreamObserver)
		responseClient.SetDebugHTTPResponseObserver(r.debugHTTPObserver)
		client = responseClient
	case modelprofile.ProtocolAnthropic:
		client = anthropic.New(profile, secret, r.recorder)
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
	if gated, ok := client.(interface {
		SetRequestGate(func(context.Context) error)
	}); ok {
		gated.SetRequestGate(r.requestGate)
	}
	r.clients[key] = cachedProtocolClient{fingerprint: fingerprint, client: client}
	return client, nil
}
