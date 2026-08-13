package gateway

import (
	"context"
	"io"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
)

// ConnectionTester keeps discovery compatible with gateways exposing
// /v1/models. Anthropic deployments often omit it; callers retain manual Model
// ID entry, while Test validates the saved protocol endpoint directly.
type ConnectionTester struct{}

func NewConnectionTester() *ConnectionTester { return &ConnectionTester{} }

func (t *ConnectionTester) Discover(ctx context.Context, profile modelprofile.Profile, secret []byte) ([]modelprofile.AvailableModel, error) {
	return openai.New(profile, secret).Discover(ctx, profile, secret)
}

func (t *ConnectionTester) Test(ctx context.Context, profile modelprofile.Profile, secret []byte) error {
	switch profile.APIProtocol {
	case modelprofile.ProtocolAnthropic:
		return anthropic.TestConnection(ctx, profile, secret)
	case modelprofile.ProtocolOpenAIResponses:
		stream, err := responses.New(profile, secret).Stream(ctx, model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "Reply OK."}}})
		if err != nil {
			return err
		}
		defer stream.Close()
		for {
			event, recvErr := stream.Recv()
			if recvErr == io.EOF || event.Type == model.EventDone {
				return nil
			}
			if recvErr != nil {
				return recvErr
			}
		}
	default:
		_, err := t.Discover(ctx, profile, secret)
		return err
	}
}
