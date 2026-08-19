package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/modelutil"
)

const (
	SecretRef      = "sciaide/embedding/default"
	defaultTimeout = 30
	maxBatchSize   = 16
	maxResponse    = 32 * 1024 * 1024
)

type Identity struct {
	ModelID     string `json:"modelId"`
	Dimensions  int    `json:"dimensions"`
	Fingerprint string `json:"-"`
}

type Config struct {
	Enabled          bool       `json:"enabled"`
	BaseURL          string     `json:"baseUrl"`
	ModelID          string     `json:"modelId"`
	Dimensions       int        `json:"dimensions"`
	Fingerprint      string     `json:"-"`
	SecretRef        string     `json:"-"`
	SecretConfigured bool       `json:"secretConfigured"`
	SecretMasked     string     `json:"secretMasked,omitempty"`
	TimeoutSeconds   int        `json:"timeoutSeconds"`
	LastTestedAt     *time.Time `json:"lastTestedAt,omitempty"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (c Config) Identity() Identity {
	return Identity{ModelID: c.ModelID, Dimensions: c.Dimensions, Fingerprint: c.Fingerprint}
}

type SaveCommand struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"baseUrl"`
	ModelID        string `json:"modelId"`
	APIKey         string `json:"apiKey"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type Repository interface {
	Get(ctx context.Context) (Config, error)
	Save(ctx context.Context, value Config) error
}

type SecretStore interface {
	Put(ctx context.Context, ref string, value []byte) error
	Get(ctx context.Context, ref string) ([]byte, error)
	Configured(ctx context.Context, ref string) (bool, string, error)
}

type Client interface {
	Embed(ctx context.Context, config Config, secret []byte, inputs []string) ([][]float32, error)
}

type Provider interface {
	Current(ctx context.Context) (Identity, bool, error)
	Embed(ctx context.Context, expected Identity, inputs []string) ([][]float32, error)
}

type Service struct {
	repository Repository
	secrets    SecretStore
	client     Client
	now        func() time.Time
}

func NewService(repository Repository, secrets SecretStore, client Client) *Service {
	return &Service{repository: repository, secrets: secrets, client: client, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Get(ctx context.Context) (Config, error) {
	value, err := s.repository.Get(ctx)
	if err != nil {
		return Config{}, err
	}
	return s.decorate(ctx, value)
}

func (s *Service) Save(ctx context.Context, command SaveCommand) (Config, error) {
	if s == nil || s.repository == nil || s.secrets == nil || s.client == nil {
		return Config{}, fmt.Errorf("embedding service is not configured")
	}
	value, err := s.repository.Get(ctx)
	if err != nil {
		return Config{}, err
	}
	value.Enabled = command.Enabled
	value.BaseURL = strings.TrimRight(strings.TrimSpace(command.BaseURL), "/")
	value.ModelID = strings.TrimSpace(command.ModelID)
	value.TimeoutSeconds = command.TimeoutSeconds
	if value.TimeoutSeconds == 0 {
		value.TimeoutSeconds = defaultTimeout
	}
	if value.TimeoutSeconds < 5 || value.TimeoutSeconds > 300 {
		return Config{}, fmt.Errorf("Embedding 超时时间必须在 5 到 300 秒之间")
	}
	if value.SecretRef == "" {
		value.SecretRef = SecretRef
	}
	if value.Enabled {
		if value.BaseURL == "" || value.ModelID == "" {
			return Config{}, fmt.Errorf("启用语义检索需要 Base URL 和 Embedding Model ID")
		}
		if err := validateBaseURL(value.BaseURL); err != nil {
			return Config{}, err
		}
	}
	secret := []byte(strings.TrimSpace(command.APIKey))
	if len(secret) == 0 {
		configured, _, configuredErr := s.secrets.Configured(ctx, value.SecretRef)
		if configuredErr != nil {
			return Config{}, configuredErr
		}
		if configured {
			secret, err = s.secrets.Get(ctx, value.SecretRef)
			if err != nil {
				return Config{}, err
			}
		}
	}
	if value.Enabled {
		vectors, testErr := s.client.Embed(ctx, value, secret, []string{"SciAide semantic retrieval connection test"})
		if testErr != nil {
			return Config{}, fmt.Errorf("验证 /v1/embeddings 失败: %w", testErr)
		}
		if len(vectors) != 1 || len(vectors[0]) == 0 {
			return Config{}, fmt.Errorf("Embedding 服务没有返回有效向量")
		}
		value.Dimensions = len(vectors[0])
		value.Fingerprint = fingerprint(value.BaseURL, value.ModelID, value.Dimensions)
		tested := s.now()
		value.LastTestedAt = &tested
	} else {
		value.Dimensions = 0
		value.Fingerprint = ""
		value.LastTestedAt = nil
	}
	value.UpdatedAt = s.now()
	if key := strings.TrimSpace(command.APIKey); key != "" {
		if err := s.secrets.Put(ctx, value.SecretRef, []byte(key)); err != nil {
			return Config{}, fmt.Errorf("保存 Embedding API Key: %w", err)
		}
	}
	if err := s.repository.Save(ctx, value); err != nil {
		return Config{}, err
	}
	return s.decorate(ctx, value)
}

func (s *Service) Current(ctx context.Context) (Identity, bool, error) {
	value, err := s.repository.Get(ctx)
	if err != nil {
		return Identity{}, false, err
	}
	if !value.Enabled {
		return Identity{}, false, nil
	}
	if value.ModelID == "" || value.Dimensions < 1 || value.Fingerprint == "" {
		return Identity{}, false, fmt.Errorf("Embedding 配置尚未验证")
	}
	return value.Identity(), true, nil
}

func (s *Service) Embed(ctx context.Context, expected Identity, inputs []string) ([][]float32, error) {
	value, err := s.repository.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !value.Enabled || value.Fingerprint != expected.Fingerprint || value.ModelID != expected.ModelID || value.Dimensions != expected.Dimensions {
		return nil, fmt.Errorf("Embedding 配置已改变，需要重建知识索引")
	}
	configured, _, err := s.secrets.Configured(ctx, value.SecretRef)
	if err != nil {
		return nil, err
	}
	var secret []byte
	if configured {
		secret, err = s.secrets.Get(ctx, value.SecretRef)
		if err != nil {
			return nil, err
		}
	}
	result := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += maxBatchSize {
		end := min(len(inputs), start+maxBatchSize)
		batch, err := s.client.Embed(ctx, value, secret, inputs[start:end])
		if err != nil {
			return nil, err
		}
		for _, vector := range batch {
			if len(vector) != expected.Dimensions {
				return nil, fmt.Errorf("Embedding 向量维度从 %d 变为 %d，需要重新验证配置", expected.Dimensions, len(vector))
			}
			result = append(result, vector)
		}
	}
	return result, nil
}

func (s *Service) decorate(ctx context.Context, value Config) (Config, error) {
	if value.SecretRef == "" {
		value.SecretRef = SecretRef
	}
	configured, masked, err := s.secrets.Configured(ctx, value.SecretRef)
	if err != nil {
		return Config{}, err
	}
	value.SecretConfigured, value.SecretMasked = configured, masked
	value.SecretRef = ""
	return value, nil
}

func fingerprint(baseURL, modelID string, dimensions int) string {
	digest := sha256.Sum256([]byte(strings.TrimRight(strings.TrimSpace(baseURL), "/") + "\x00" + strings.TrimSpace(modelID) + fmt.Sprintf("\x00%d", dimensions)))
	return hex.EncodeToString(digest[:])
}

func validateBaseURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Embedding Base URL 必须是有效的 HTTP(S) 地址，且不能包含凭据、查询参数或片段")
	}
	return nil
}

type HTTPClient struct{}

func NewHTTPClient() *HTTPClient { return &HTTPClient{} }

func (*HTTPClient) Embed(ctx context.Context, config Config, secret []byte, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 || len(inputs) > maxBatchSize {
		return nil, fmt.Errorf("Embedding 批次大小无效")
	}
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{Model: config.ModelID, Input: inputs})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, modelutil.Endpoint(config.BaseURL, "embeddings"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	modelutil.ApplyBearerAndCustomHeaders(request, secret, nil)
	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout * time.Second
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("连接 Embedding 服务: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("读取 Embedding 响应: %w", err)
	}
	if len(responseBody) > maxResponse {
		return nil, fmt.Errorf("Embedding 响应超过大小限制")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := modelutil.ProviderErrorMessage(responseBody)
		if detail == "" {
			detail = http.StatusText(response.StatusCode)
		}
		return nil, fmt.Errorf("Embedding API HTTP %d: %s", response.StatusCode, detail)
	}
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, fmt.Errorf("解析 Embedding 响应: %w", err)
	}
	if len(payload.Data) != len(inputs) {
		return nil, fmt.Errorf("Embedding 响应数量不匹配")
	}
	result := make([][]float32, len(inputs))
	dimensions := 0
	for _, item := range payload.Data {
		if item.Index < 0 || item.Index >= len(result) || result[item.Index] != nil || len(item.Embedding) == 0 || len(item.Embedding) > 65_536 {
			return nil, fmt.Errorf("Embedding 响应包含无效索引或维度")
		}
		if dimensions == 0 {
			dimensions = len(item.Embedding)
		} else if dimensions != len(item.Embedding) {
			return nil, fmt.Errorf("Embedding 响应向量维度不一致")
		}
		vector := make([]float32, len(item.Embedding))
		var norm float64
		for index, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("Embedding 响应包含非有限数值")
			}
			vector[index] = float32(value)
			norm += value * value
		}
		if norm == 0 {
			return nil, fmt.Errorf("Embedding 响应包含零向量")
		}
		scale := float32(1 / math.Sqrt(norm))
		for index := range vector {
			vector[index] *= scale
		}
		result[item.Index] = vector
	}
	return result, nil
}
