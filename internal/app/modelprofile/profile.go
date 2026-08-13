package modelprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
)

const ProviderOpenAICompatible = "openai_compatible"

type Profile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderType string `json:"providerType"`
	BaseURL      string `json:"baseUrl"`
	// ModelID is the default model kept for backwards compatibility with P1
	// clients and the original model_profiles schema. Models is the source of
	// truth for all selectable models on this connection.
	ModelID          string            `json:"modelId"`
	Models           []ProfileModel    `json:"models"`
	SecretRef        string            `json:"-"`
	SecretConfigured bool              `json:"secretConfigured"`
	SecretMasked     string            `json:"secretMasked,omitempty"`
	TimeoutSeconds   int               `json:"timeoutSeconds"`
	Temperature      *float64          `json:"temperature,omitempty"`
	MaxOutputTokens  *int              `json:"maxOutputTokens,omitempty"`
	CustomHeaders    map[string]string `json:"customHeaders"`
	Enabled          bool              `json:"enabled"`
	IsDefault        bool              `json:"isDefault"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
}

type ProfileModel struct {
	ID        string `json:"id"`
	OwnedBy   string `json:"ownedBy,omitempty"`
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"isDefault"`
}

type SaveCommand struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	BaseURL         string            `json:"baseUrl"`
	ModelID         string            `json:"modelId"`
	Models          []ProfileModel    `json:"models"`
	APIKey          string            `json:"apiKey"`
	TimeoutSeconds  int               `json:"timeoutSeconds"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxOutputTokens *int              `json:"maxOutputTokens,omitempty"`
	CustomHeaders   map[string]string `json:"customHeaders"`
	Enabled         bool              `json:"enabled"`
	IsDefault       bool              `json:"isDefault"`
}

type Repository interface {
	Save(ctx context.Context, value Profile) error
	Get(ctx context.Context, id string) (Profile, error)
	List(ctx context.Context) ([]Profile, error)
	Delete(ctx context.Context, id string) error
}

type SecretStore interface {
	Put(ctx context.Context, ref string, value []byte) error
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
	Configured(ctx context.Context, ref string) (bool, string, error)
}

type ConnectionTester interface {
	Test(ctx context.Context, profile Profile, secret []byte) error
	Discover(ctx context.Context, profile Profile, secret []byte) ([]AvailableModel, error)
}

type DiscoveryCommand struct {
	ProfileID     string            `json:"profileId"`
	BaseURL       string            `json:"baseUrl"`
	APIKey        string            `json:"apiKey"`
	CustomHeaders map[string]string `json:"customHeaders"`
}

type AvailableModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"ownedBy,omitempty"`
}

type Service struct {
	repository Repository
	secrets    SecretStore
	tester     ConnectionTester
	now        func() time.Time
}

func NewService(repository Repository, secrets SecretStore, tester ConnectionTester) *Service {
	return &Service{repository: repository, secrets: secrets, tester: tester, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Save(ctx context.Context, cmd SaveCommand) (Profile, error) {
	var existing Profile
	var err error
	if strings.TrimSpace(cmd.ID) != "" {
		existing, err = s.repository.Get(ctx, strings.TrimSpace(cmd.ID))
		if err != nil {
			return Profile{}, fmt.Errorf("get model profile: %w", err)
		}
	}
	if err := validateCommand(cmd); err != nil {
		return Profile{}, err
	}
	now := s.now()
	value := existing
	if value.ID == "" {
		value.ID, err = id.New()
		if err != nil {
			return Profile{}, err
		}
		value.SecretRef = "sciaide/model/" + value.ID
		value.CreatedAt = now
	}
	value.Name = strings.TrimSpace(cmd.Name)
	value.ProviderType = ProviderOpenAICompatible
	value.BaseURL = strings.TrimRight(strings.TrimSpace(cmd.BaseURL), "/")
	value.Models, value.ModelID = normalizeModels(cmd.Models, cmd.ModelID)
	value.TimeoutSeconds = cmd.TimeoutSeconds
	value.Temperature = cmd.Temperature
	value.MaxOutputTokens = cmd.MaxOutputTokens
	value.CustomHeaders = cloneHeaders(cmd.CustomHeaders)
	value.Enabled = cmd.Enabled
	value.IsDefault = cmd.IsDefault
	value.UpdatedAt = now

	key := strings.TrimSpace(cmd.APIKey)
	storedNewKey := false
	if key != "" {
		if err := s.secrets.Put(ctx, value.SecretRef, []byte(key)); err != nil {
			return Profile{}, fmt.Errorf("store model secret: %w", err)
		}
		storedNewKey = true
	}
	if err := s.repository.Save(ctx, value); err != nil {
		if storedNewKey && existing.ID == "" {
			_ = s.secrets.Delete(ctx, value.SecretRef)
		}
		return Profile{}, fmt.Errorf("save model profile: %w", err)
	}
	return s.decorate(ctx, value)
}

func (s *Service) List(ctx context.Context) ([]Profile, error) {
	values, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i], err = s.decorate(ctx, values[i])
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *Service) Get(ctx context.Context, profileID string) (Profile, error) {
	value, err := s.repository.Get(ctx, profileID)
	if err != nil {
		return Profile{}, err
	}
	return s.decorate(ctx, value)
}

func (s *Service) Secret(ctx context.Context, profileID string) (Profile, []byte, error) {
	value, err := s.repository.Get(ctx, profileID)
	if err != nil {
		return Profile{}, nil, err
	}
	configured, _, err := s.secrets.Configured(ctx, value.SecretRef)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("read model secret status: %w", err)
	}
	if !configured {
		return value, nil, nil
	}
	secret, err := s.secrets.Get(ctx, value.SecretRef)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("get model secret: %w", err)
	}
	return value, secret, nil
}

func (s *Service) DeleteKey(ctx context.Context, profileID string) error {
	value, err := s.repository.Get(ctx, profileID)
	if err != nil {
		return err
	}
	return s.secrets.Delete(ctx, value.SecretRef)
}

func (s *Service) Delete(ctx context.Context, profileID string) error {
	value, err := s.repository.Get(ctx, profileID)
	if err != nil {
		return err
	}
	if err := s.repository.Delete(ctx, profileID); err != nil {
		return fmt.Errorf("delete model profile: %w", err)
	}
	if err := s.secrets.Delete(ctx, value.SecretRef); err != nil {
		return fmt.Errorf("delete model secret: %w", err)
	}
	return nil
}

func (s *Service) Test(ctx context.Context, profileID string) error {
	profile, secret, err := s.Secret(ctx, profileID)
	if err != nil {
		return err
	}
	return s.tester.Test(ctx, profile, secret)
}

func (s *Service) Discover(ctx context.Context, cmd DiscoveryCommand) ([]AvailableModel, error) {
	profile := Profile{BaseURL: strings.TrimRight(strings.TrimSpace(cmd.BaseURL), "/"), TimeoutSeconds: 30, CustomHeaders: cloneHeaders(cmd.CustomHeaders)}
	if profile.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if err := validateBaseURL(profile.BaseURL); err != nil {
		return nil, err
	}
	for name, value := range profile.CustomHeaders {
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") || sensitiveHeader(name) {
			return nil, fmt.Errorf("invalid or sensitive custom header %q", name)
		}
	}
	secret := []byte(strings.TrimSpace(cmd.APIKey))
	if strings.TrimSpace(cmd.ProfileID) != "" && len(secret) == 0 {
		_, storedSecret, err := s.Secret(ctx, strings.TrimSpace(cmd.ProfileID))
		if err != nil {
			return nil, err
		}
		secret = storedSecret
	}
	return s.tester.Discover(ctx, profile, secret)
}

func (s *Service) decorate(ctx context.Context, value Profile) (Profile, error) {
	configured, masked, err := s.secrets.Configured(ctx, value.SecretRef)
	if err != nil {
		return Profile{}, fmt.Errorf("read model secret status: %w", err)
	}
	value.SecretConfigured, value.SecretMasked = configured, masked
	value.SecretRef = ""
	return value, nil
}

func validateCommand(cmd SaveCommand) error {
	models, _ := normalizeModels(cmd.Models, cmd.ModelID)
	if strings.TrimSpace(cmd.Name) == "" || len(models) == 0 {
		return fmt.Errorf("profile name and at least one model id are required")
	}
	if err := validateBaseURL(cmd.BaseURL); err != nil {
		return err
	}
	if cmd.TimeoutSeconds < 5 || cmd.TimeoutSeconds > 600 {
		return fmt.Errorf("timeout seconds must be between 5 and 600")
	}
	if cmd.Temperature != nil && (*cmd.Temperature < 0 || *cmd.Temperature > 2) {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if cmd.MaxOutputTokens != nil && *cmd.MaxOutputTokens <= 0 {
		return fmt.Errorf("max output tokens must be positive")
	}
	for name, value := range cmd.CustomHeaders {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid custom header")
		}
		if sensitiveHeader(canonical) {
			return fmt.Errorf("sensitive header %q must use SecretStore", canonical)
		}
	}
	return nil
}

func normalizeModels(input []ProfileModel, legacyDefault string) ([]ProfileModel, string) {
	legacyDefault = strings.TrimSpace(legacyDefault)
	if len(input) == 0 && legacyDefault != "" {
		input = []ProfileModel{{ID: legacyDefault, Enabled: true, IsDefault: true}}
	}
	models := make([]ProfileModel, 0, len(input))
	indexes := make(map[string]int, len(input))
	for _, item := range input {
		item.ID = strings.TrimSpace(item.ID)
		item.OwnedBy = strings.TrimSpace(item.OwnedBy)
		if item.ID == "" {
			continue
		}
		if index, exists := indexes[item.ID]; exists {
			models[index].OwnedBy = item.OwnedBy
			models[index].Enabled = models[index].Enabled || item.Enabled
			models[index].IsDefault = models[index].IsDefault || item.IsDefault
			continue
		}
		indexes[item.ID] = len(models)
		models = append(models, item)
	}
	defaultID := ""
	if index, exists := indexes[legacyDefault]; exists && models[index].Enabled {
		defaultID = legacyDefault
	}
	if defaultID == "" {
		for _, item := range models {
			if item.Enabled && item.IsDefault {
				defaultID = item.ID
				break
			}
		}
	}
	if defaultID == "" {
		for _, item := range models {
			if item.Enabled {
				defaultID = item.ID
				break
			}
		}
	}
	if defaultID == "" && len(models) > 0 {
		// A profile with no enabled models cannot be used. Enabling the first
		// model makes imports from early P1 clients deterministic.
		models[0].Enabled = true
		defaultID = models[0].ID
	}
	for index := range models {
		models[index].IsDefault = models[index].ID == defaultID
	}
	return models, defaultID
}

func validateBaseURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base URL must be an absolute HTTP(S) URL")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not contain credentials")
	}
	return nil
}

func sensitiveHeader(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(name))
	for _, marker := range []string{"authorization", "apikey", "token", "secret", "cookie", "password", "credential"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func cloneHeaders(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[http.CanonicalHeaderKey(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return output
}

func EncodeHeaders(headers map[string]string) (string, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	value, err := json.Marshal(headers)
	return string(value), err
}

func DecodeHeaders(value string) (map[string]string, error) {
	var headers map[string]string
	if err := json.Unmarshal([]byte(value), &headers); err != nil {
		return nil, err
	}
	if headers == nil {
		headers = map[string]string{}
	}
	return headers, nil
}
