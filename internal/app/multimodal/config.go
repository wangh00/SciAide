package multimodal

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/modelcap"
)

const (
	APIFormatOpenAI          = "openai"
	APIFormatOpenAIResponses = "openai_responses"
	APIFormatAnthropic       = "anthropic"
)

// Channel is the trusted runtime form of a user-defined fallback channel.
// Secret bytes are never persisted here or exported through Wails or JSON.
type Channel struct {
	ID             string
	Name           string
	BaseURL        string
	ModelID        string
	APIFormat      string
	Enabled        bool
	Priority       int
	TimeoutSeconds int
	MaxTokens      int
	SecretRef      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ChannelView struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	BaseURL          string               `json:"baseUrl,omitempty"`
	ModelID          string               `json:"modelId"`
	APIProtocol      modelcap.APIProtocol `json:"apiProtocol"`
	Priority         int                  `json:"priority"`
	Enabled          bool                 `json:"enabled"`
	SecretConfigured bool                 `json:"secretConfigured"`
	SecretMasked     string               `json:"secretMasked,omitempty"`
	TimeoutSeconds   int                  `json:"timeoutSeconds"`
	MaxTokens        int                  `json:"maxTokens"`
}

func (c Channel) Protocol() modelcap.APIProtocol {
	switch normalizeAPIFormat(c.APIFormat) {
	case APIFormatAnthropic:
		return modelcap.ProtocolAnthropic
	case APIFormatOpenAIResponses:
		return modelcap.ProtocolOpenAIResponses
	default:
		return modelcap.ProtocolOpenAIChat
	}
}

func protocolAPIFormat(protocol modelcap.APIProtocol) string {
	switch protocol {
	case modelcap.ProtocolAnthropic:
		return APIFormatAnthropic
	case modelcap.ProtocolOpenAIResponses:
		return APIFormatOpenAIResponses
	default:
		return APIFormatOpenAI
	}
}

func normalizeAPIFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case APIFormatAnthropic, string(modelcap.ProtocolAnthropic):
		return APIFormatAnthropic
	case APIFormatOpenAIResponses:
		return APIFormatOpenAIResponses
	default:
		return APIFormatOpenAI
	}
}

func channelProfileID(channel Channel) string {
	return "vision-fallback/custom/" + strings.TrimSpace(channel.ID)
}

func enabledChannels(channels []Channel) []Channel {
	result := make([]Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Enabled {
			result = append(result, channel)
		}
	}
	return result
}

func validateChannel(channel Channel) error {
	if strings.TrimSpace(channel.Name) == "" || strings.TrimSpace(channel.ModelID) == "" {
		return fmt.Errorf("name and model id are required")
	}
	parsedURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/"))
	validScheme := parsedURL.Scheme == "http" || parsedURL.Scheme == "https"
	if err != nil || !validScheme || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return fmt.Errorf("base URL is invalid")
	}
	if normalizeAPIFormat(channel.APIFormat) != channel.APIFormat {
		return fmt.Errorf("API protocol is unsupported")
	}
	if channel.Priority < 0 || channel.Priority > 1000 {
		return fmt.Errorf("priority must be between 0 and 1000")
	}
	if channel.TimeoutSeconds < 5 || channel.TimeoutSeconds > 600 {
		return fmt.Errorf("timeout seconds must be between 5 and 600")
	}
	if channel.MaxTokens <= 0 || channel.MaxTokens > 32_000 {
		return fmt.Errorf("max tokens must be between 1 and 32000")
	}
	return nil
}
