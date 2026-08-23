package multimodal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

type resolverFixture struct {
	mu      sync.Mutex
	models  map[string]model.ResolvedChatModel
	errors  map[string]error
	calls   []string
	secrets map[string]string
}

func (f *resolverFixture) Resolve(_ context.Context, channel Channel, secret []byte) (model.ResolvedChatModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, channel.ID)
	if f.secrets == nil {
		f.secrets = map[string]string{}
	}
	f.secrets[channel.ID] = string(secret)
	if err := f.errors[channel.ID]; err != nil {
		return model.ResolvedChatModel{}, err
	}
	value, ok := f.models[channel.ID]
	if !ok {
		return model.ResolvedChatModel{}, fmt.Errorf("missing channel %s", channel.ID)
	}
	return value, nil
}

type memoryRepository struct {
	values    map[string]Channel
	saveErr   error
	deleteErr error
}

func newMemoryRepository() *memoryRepository { return &memoryRepository{values: map[string]Channel{}} }
func (r *memoryRepository) Save(_ context.Context, value Channel) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.values[value.ID] = value
	return nil
}
func (r *memoryRepository) Get(_ context.Context, id string) (Channel, error) {
	value, ok := r.values[id]
	if !ok {
		return Channel{}, fmt.Errorf("not found")
	}
	return value, nil
}
func (r *memoryRepository) List(context.Context) ([]Channel, error) {
	values := make([]Channel, 0, len(r.values))
	for _, value := range r.values {
		values = append(values, value)
	}
	return values, nil
}
func (r *memoryRepository) Delete(_ context.Context, id string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.values, id)
	return nil
}

func visionChannel(id, modelID string, priority int) Channel {
	return Channel{ID: id, Name: id, BaseURL: "https://example.test/v1", ModelID: modelID, APIFormat: APIFormatOpenAI, Enabled: true, Priority: priority, TimeoutSeconds: 30, MaxTokens: 512, SecretRef: "sciaide/vision-fallback/" + id}
}

func successfulVision(text string) *fake.Model {
	return fake.New([]fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: text}},
		{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 12, OutputTokens: 8}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	})
}

func TestAnalyzeUsesEnabledChannelsInPriorityOrder(t *testing.T) {
	placeholder := successfulVision("I received [Unsupported Image].")
	vision := successfulVision("blue hair and a black bow")
	resolver := &resolverFixture{models: map[string]model.ResolvedChatModel{
		"first":  {Model: placeholder, APIProtocol: modelcap.ProtocolOpenAIChat},
		"second": {Model: vision, APIProtocol: modelcap.ProtocolOpenAIChat},
	}}
	first := visionChannel("first", "vision-1", 10)
	second := visionChannel("second", "vision-2", 20)
	channels := []Channel{
		second, first,
		{ID: "disabled", Name: "disabled", BaseURL: "https://example.test/v1", ModelID: "disabled", APIFormat: APIFormatOpenAI, Enabled: false, Priority: 0, TimeoutSeconds: 30, MaxTokens: 512, SecretRef: "sciaide/vision-fallback/disabled"},
	}
	repository := newMemoryRepository()
	for _, channel := range channels {
		repository.values[channel.ID] = channel
	}
	secrets := secretstore.NewMemory()
	if err := secrets.Put(context.Background(), first.SecretRef, []byte("first-key")); err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, secrets, resolver)
	imagePart := model.ContentPart{Type: "input_image", MediaType: "image/webp", Data: "AQID", Name: "figure.jpg", AttachmentID: "attachment"}
	result, err := service.Analyze(context.Background(), "describe", []model.ContentPart{imagePart})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProfileID != "vision-fallback/custom/second" || result.ModelID != "vision-2" || result.Text != "blue hair and a black bow" {
		t.Fatalf("Analyze() = %#v", result)
	}
	if !result.UsageReported || result.Usage.InputTokens != 12 {
		t.Fatalf("usage = %#v, reported=%v", result.Usage, result.UsageReported)
	}
	if !reflect.DeepEqual(resolver.calls, []string{"first", "second"}) || resolver.secrets["first"] != "first-key" {
		t.Fatalf("calls=%#v secrets=%#v", resolver.calls, resolver.secrets)
	}
	requests := vision.Requests()
	if len(requests) != 1 || requests[0].Messages[1].Parts[0].AttachmentID != "attachment" {
		t.Fatalf("vision requests = %#v", requests)
	}
}

func TestAnalyzeUsesCreationOrderWhenPrioritiesMatch(t *testing.T) {
	older := visionChannel("z-older", "vision-old", 10)
	newer := visionChannel("a-newer", "vision-new", 10)
	older.CreatedAt = time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	newer.CreatedAt = older.CreatedAt.Add(time.Hour)
	resolver := &resolverFixture{
		models: map[string]model.ResolvedChatModel{
			"z-older": {Model: successfulVision("[Unsupported Image]"), APIProtocol: modelcap.ProtocolOpenAIChat},
			"a-newer": {Model: successfulVision("usable image"), APIProtocol: modelcap.ProtocolOpenAIChat},
		},
	}
	repository := newMemoryRepository()
	repository.values[older.ID] = older
	repository.values[newer.ID] = newer
	service := NewService(repository, secretstore.NewMemory(), resolver)

	if _, err := service.Analyze(context.Background(), "describe", []model.ContentPart{{Type: "input_image", MediaType: "image/png", Data: "AQID"}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolver.calls, []string{"z-older", "a-newer"}) {
		t.Fatalf("channel order = %#v", resolver.calls)
	}
}

func TestSaveCustomChannelUsesSecretStoreAndDoesNotExposeKey(t *testing.T) {
	repository := newMemoryRepository()
	secrets := secretstore.NewMemory()
	service := NewService(repository, secrets, &resolverFixture{})
	view, err := service.Save(context.Background(), SaveCommand{
		Name: "custom", BaseURL: "https://vision.example/v1", ModelID: "vision-model",
		APIProtocol: modelcap.ProtocolOpenAIChat, APIKey: "private-key", Enabled: true,
		Priority: 30, TimeoutSeconds: 60, MaxTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.SecretConfigured || view.SecretMasked == "" || view.BaseURL == "" {
		t.Fatalf("saved view = %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil || strings.Contains(string(encoded), "private-key") {
		t.Fatalf("view leaked key: %s, %v", encoded, err)
	}
	stored := repository.values[view.ID]
	if stored.SecretRef != "sciaide/vision-fallback/"+view.ID {
		t.Fatalf("secret ref = %q", stored.SecretRef)
	}
	secret, err := secrets.Get(context.Background(), stored.SecretRef)
	if err != nil || string(secret) != "private-key" {
		t.Fatalf("stored secret = %q, %v", secret, err)
	}
}

func TestSaveRestoresPreviousSecretWhenRepositoryUpdateFails(t *testing.T) {
	ctx := context.Background()
	repository := newMemoryRepository()
	secrets := secretstore.NewMemory()
	service := NewService(repository, secrets, &resolverFixture{})
	view, err := service.Save(ctx, SaveCommand{
		Name: "custom", BaseURL: "https://vision.example/v1", ModelID: "vision-model",
		APIProtocol: modelcap.ProtocolOpenAIChat, APIKey: "old-key", Enabled: true,
		Priority: 30, TimeoutSeconds: 60, MaxTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.saveErr = fmt.Errorf("database unavailable")
	_, err = service.Save(ctx, SaveCommand{
		ID: view.ID, Name: "updated", BaseURL: "https://vision.example/v1", ModelID: "vision-model",
		APIProtocol: modelcap.ProtocolOpenAIChat, APIKey: "new-key", Enabled: true,
		Priority: 30, TimeoutSeconds: 60, MaxTokens: 2048,
	})
	if err == nil {
		t.Fatal("Save() succeeded despite repository failure")
	}
	stored := repository.values[view.ID]
	secret, secretErr := secrets.Get(ctx, stored.SecretRef)
	if secretErr != nil || string(secret) != "old-key" || stored.Name != "custom" {
		t.Fatalf("rollback state = channel:%#v secret:%q err:%v", stored, secret, secretErr)
	}
}

func TestDeleteRestoresSecretWhenRepositoryDeleteFails(t *testing.T) {
	ctx := context.Background()
	repository := newMemoryRepository()
	secrets := secretstore.NewMemory()
	service := NewService(repository, secrets, &resolverFixture{})
	view, err := service.Save(ctx, SaveCommand{
		Name: "custom", BaseURL: "https://vision.example/v1", ModelID: "vision-model",
		APIProtocol: modelcap.ProtocolOpenAIChat, APIKey: "private-key", Enabled: true,
		Priority: 30, TimeoutSeconds: 60, MaxTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.deleteErr = fmt.Errorf("database unavailable")
	if err := service.Delete(ctx, view.ID); err == nil {
		t.Fatal("Delete() succeeded despite repository failure")
	}
	stored := repository.values[view.ID]
	secret, secretErr := secrets.Get(ctx, stored.SecretRef)
	if secretErr != nil || string(secret) != "private-key" {
		t.Fatalf("restored secret = %q, %v", secret, secretErr)
	}
}

func TestForgetProfileClearsOnlyMatchingCapabilityObservations(t *testing.T) {
	service := NewService(nil, nil, nil)
	service.MarkUnsupported("profile-a", "model", modelcap.ProtocolOpenAIChat)
	service.MarkSupported("profile-b", "model", modelcap.ProtocolOpenAIChat)
	service.ForgetProfile("profile-a")
	if got := service.Capability("profile-a", "model", modelcap.ProtocolOpenAIChat); got != CapabilityUnknown {
		t.Fatalf("profile-a capability = %q", got)
	}
	if got := service.Capability("profile-b", "model", modelcap.ProtocolOpenAIChat); got != CapabilitySupported {
		t.Fatalf("profile-b capability = %q", got)
	}
}

func TestAnalyzeDoesNotRetryPermanentChannelFailure(t *testing.T) {
	vision := successfulVision("usable pixels")
	resolver := &resolverFixture{
		models: map[string]model.ResolvedChatModel{"second": {Model: vision, APIProtocol: modelcap.ProtocolOpenAIChat}},
		errors: map[string]error{"first": &apperr.Error{Code: "MODEL_AUTH_FAILED", UserMessage: "bad key", Retryable: false}},
	}
	first := visionChannel("first", "vision-1", 10)
	second := visionChannel("second", "vision-2", 20)
	repository := newMemoryRepository()
	repository.values[first.ID] = first
	repository.values[second.ID] = second
	service := NewService(repository, secretstore.NewMemory(), resolver)
	_, err := service.Analyze(context.Background(), "describe", []model.ContentPart{{Type: "input_image", MediaType: "image/png", Data: "AQID"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolver.calls, []string{"first", "second"}) {
		t.Fatalf("resolver calls = %#v", resolver.calls)
	}
}

func TestAnalyzeReportsNoEnabledCustomChannels(t *testing.T) {
	repository := newMemoryRepository()
	disabled := visionChannel("disabled", "custom-vision", 10)
	disabled.Enabled = false
	repository.values[disabled.ID] = disabled
	service := NewService(repository, secretstore.NewMemory(), &resolverFixture{})
	_, err := service.Analyze(context.Background(), "describe", []model.ContentPart{{Type: "input_image", MediaType: "image/png", Data: "AQID"}})
	if !errors.Is(err, ErrNoEnabledChannels) {
		t.Fatalf("Analyze() error = %v", err)
	}
}

func TestProtocolResolverSendsConfiguredAuthorization(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer channel-key" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	channel := visionChannel("public", "vision-free", 1)
	channel.BaseURL = server.URL + "/v1"
	repository := newMemoryRepository()
	repository.values[channel.ID] = channel
	secrets := secretstore.NewMemory()
	if err := secrets.Put(context.Background(), channel.SecretRef, []byte("channel-key")); err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, secrets, NewProtocolResolver())
	result, err := service.Analyze(context.Background(), "describe", []model.ContentPart{{Type: "input_image", MediaType: "image/webp", Data: "AQID"}})
	if err != nil || result.Text != "ok" {
		t.Fatalf("Analyze() = %#v, %v", result, err)
	}
	if requestBody["model"] != "vision-free" || requestBody["stream"] != false {
		t.Fatalf("request = %#v", requestBody)
	}
	if _, exists := requestBody["stream_options"]; exists {
		t.Fatalf("non-streaming request included stream_options: %#v", requestBody)
	}
}

func TestResponseIndicatesImageUnavailable(t *testing.T) {
	for _, value := range []string{
		"SCIAIDE_IMAGE_UNAVAILABLE", "I got [Unsupported Image]", "[image unsupported]",
		"I am a text-only model and cannot analyze images.", "当前模型仅支持文本，无法查看图片。",
	} {
		if !ResponseIndicatesImageUnavailable(value) {
			t.Fatalf("expected unavailable response: %q", value)
		}
	}
	for _, value := range []string{
		"I can see the image",
		"图片中写着：当前模型仅支持文本，无法查看图片。",
		"海报标题是 I am a text-only model and cannot analyze images.",
	} {
		if ResponseIndicatesImageUnavailable(value) {
			t.Fatalf("valid image description was rejected: %q", value)
		}
	}
}

func TestPublicFailureSummaryNamesModelsWithoutInternalDetails(t *testing.T) {
	err := &AllChannelsFailedError{Failures: []ChannelFailure{
		{ModelID: "custom-vision-a", Code: "MODEL_RATE_LIMITED", Message: "模型服务繁忙或已达到限额，请稍后重试。"},
		{ModelID: "custom-vision-b", Code: "MODEL_AUTH_FAILED", Message: "模型服务拒绝了密钥，请重新设置 API Key。"},
	}}
	summary := PublicFailureSummary(err)
	if !strings.Contains(summary, "custom-vision-a") || !strings.Contains(summary, "custom-vision-b") || strings.Contains(summary, "private") {
		t.Fatalf("PublicFailureSummary() = %q", summary)
	}
}
