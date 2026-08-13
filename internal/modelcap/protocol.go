package modelcap

type APIProtocol string

const (
	ProtocolOpenAIChat      APIProtocol = "openai_chat_completions"
	ProtocolOpenAIResponses APIProtocol = "openai_responses"
	ProtocolAnthropic       APIProtocol = "anthropic_messages"
)

func (p APIProtocol) Valid() bool {
	return p == ProtocolOpenAIChat || p == ProtocolOpenAIResponses || p == ProtocolAnthropic
}
