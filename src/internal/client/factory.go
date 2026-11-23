package client

import (
	"fmt"
	"strings"
	"time"

	"crevo-builder/internal/client/anthropic"
	"crevo-builder/internal/client/openai"
	"crevo-builder/internal/models"

	"github.com/sirupsen/logrus"
)

// ProviderType defines the AI provider type
type ProviderType string

const (
	ProviderTypeOpenAI    ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
	ProviderTypeGrok      ProviderType = "grok"
	ProviderTypeDeepSeek  ProviderType = "deepseek"
	ProviderTypeZhipu      ProviderType = "zhipu"
	ProviderTypeAbhibots   ProviderType = "abhibots"
	ProviderTypeGemini     ProviderType = "gemini"
	ProviderTypeOpenRouter ProviderType = "openrouter"
)

// Factory creates AI providers based on type
type Factory struct {
	logger      *logrus.Logger
	retryConfig models.RetryConfig
	onRetry     models.RetryCallback
}

// NewFactory creates a new provider factory
func NewFactory(logger *logrus.Logger, retryConfig models.RetryConfig, onRetry models.RetryCallback) *Factory {
	return &Factory{
		logger:      logger,
		retryConfig: retryConfig,
		onRetry:     onRetry,
	}
}

// isExplicitOpenAIEndpoint returns true when a custom base URL clearly targets an OpenAI-compatible API
func isExplicitOpenAIEndpoint(baseURL string) bool {
	lower := strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	return strings.HasSuffix(lower, "/chat/completions") ||
		strings.Contains(lower, "/openai") ||
		strings.Contains(lower, "openrouter.ai")
}

// CreateProvider creates an AI provider based on the config type
func (f *Factory) CreateProvider(cfg models.ProviderConfig) (models.AIProvider, error) {
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	providerType := ProviderType(cfg.ProviderType)

	newAnthropicClient := func(c models.ProviderConfig) models.AIProvider {
		anthropicRetryConfig := anthropic.RetryConfig{
			MaxRetries:    f.retryConfig.MaxRetries,
			InitialDelay:  f.retryConfig.InitialDelay,
			BackoffFactor: f.retryConfig.BackoffFactor,
			Jitter:        f.retryConfig.Jitter,
		}
		var anthropicOnRetry anthropic.RetryCallback
		if f.onRetry != nil {
			anthropicOnRetry = func(attempt, maxRetries int, delay time.Duration, reason string) {
				f.onRetry(attempt, maxRetries, delay, fmt.Errorf("retry: %s", reason))
			}
		}
		return anthropic.NewClientWithRetry(c, f.logger, anthropicRetryConfig, anthropicOnRetry)
	}

	switch providerType {
	case ProviderTypeOpenAI:
		return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
	case ProviderTypeAnthropic:
		if cfg.BaseURL != "" && isExplicitOpenAIEndpoint(cfg.BaseURL) {
			return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
		}
		return newAnthropicClient(cfg), nil
	case ProviderTypeGrok:
		// Grok uses OpenAI-compatible API
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.x.ai/v1"
		}
		return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
	case ProviderTypeDeepSeek:
		// DeepSeek uses OpenAI-compatible API
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.deepseek.com"
		}
		return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
	case ProviderTypeZhipu:
		// ZhipuAI defaults to Anthropic-compatible z.ai proxy, or OpenAI-compatible if custom non-Anthropic base URL is configured
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.z.ai/api/anthropic"
		}
		if cfg.Model == "" {
			cfg.Model = "glm-4.7"
		}
		if !strings.Contains(strings.ToLower(cfg.BaseURL), "anthropic") {
			return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
		}
		return newAnthropicClient(cfg), nil
	case ProviderTypeAbhibots:
		// Abhibots defaults to Anthropic-compatible API via opus.abhibots.com
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://opus.abhibots.com"
		}
		if cfg.Model == "" {
			cfg.Model = "claude-sonnet-5"
		}
		if isExplicitOpenAIEndpoint(cfg.BaseURL) {
			return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
		}
		return newAnthropicClient(cfg), nil
	case ProviderTypeGemini:
		// Gemini uses OpenAI-compatible API
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		if cfg.Model == "" {
			cfg.Model = "gemini-2.5-flash-lite"
		}
		return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
	case ProviderTypeOpenRouter:
		// OpenRouter uses OpenAI-compatible API
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://openrouter.ai/api/v1"
		}
		if cfg.Model == "" {
			cfg.Model = "openrouter/free"
		}
		return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
	default:
		// Default to OpenAI-compatible for any custom/other provider
		return openai.NewClientWithRetry(cfg, f.logger, f.retryConfig, f.onRetry), nil
	}
}
