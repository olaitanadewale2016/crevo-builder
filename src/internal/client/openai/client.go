package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"crevo-builder/internal/models"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sirupsen/logrus"
)

// Client implements AIProvider using OpenAI or compatible APIs
type Client struct {
	client      *openai.Client
	httpClient  *http.Client
	apiKey      string
	baseURL     string
	model       string
	maxTokens   int
	logger      *logrus.Logger
	retryConfig models.RetryConfig
	onRetry     models.RetryCallback
}

// Ensure Client implements AIProvider
var _ models.AIProvider = (*Client)(nil)

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Tools               []chatTool    `json:"tools,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Function     chatFunctionCall       `json:"function"`
	ExtraContent map[string]interface{} `json:"extra_content,omitempty"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string          `json:"type"`
	Function chatFunctionDef `json:"function"`
}

type chatFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string         `json:"role"`
			Content   *string        `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// NewClient creates a new OpenAI client (backward compatible)
func NewClient(apiKey string, model string, maxTokens int) *Client {
	return NewClientWithConfig(models.ProviderConfig{
		APIKey:    apiKey,
		Model:     model,
		MaxTokens: maxTokens,
	}, nil)
}

// NewClientWithConfig creates a client with full configuration
func NewClientWithConfig(cfg models.ProviderConfig, logger *logrus.Logger) *Client {
	return NewClientWithRetry(cfg, logger, models.DefaultRetryConfig(), nil)
}

// NewClientWithRetry creates a client with custom retry configuration
func NewClientWithRetry(cfg models.ProviderConfig, logger *logrus.Logger, retryConfig models.RetryConfig, onRetry models.RetryCallback) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	httpClient := &http.Client{
		Timeout: 90 * time.Second,
	}

	openaiConfig := openai.DefaultConfig(cfg.APIKey)
	openaiConfig.BaseURL = baseURL
	openaiConfig.HTTPClient = httpClient
	oaiClient := openai.NewClientWithConfig(openaiConfig)

	model := cfg.Model
	if model == "" {
		model = "gpt-4o"
	}

	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 16384
	}

	return &Client{
		client:      oaiClient,
		httpClient:  httpClient,
		apiKey:      cfg.APIKey,
		baseURL:     baseURL,
		model:       model,
		maxTokens:   maxTokens,
		logger:      logger,
		retryConfig: retryConfig,
		onRetry:     onRetry,
	}
}

// SetRetryCallback sets the callback for retry events
func (c *Client) SetRetryCallback(callback models.RetryCallback) {
	c.onRetry = callback
}

// convertMessage converts our Message to OpenAI format (for backwards compatibility)
func (c *Client) convertMessage(msg models.Message) openai.ChatCompletionMessage {
	m := openai.ChatCompletionMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}

	if msg.ToolCallID != "" {
		m.ToolCallID = msg.ToolCallID
	}
	if len(msg.ToolCalls) > 0 {
		m.ToolCalls = ConvertToolCallsToOpenAI(msg.ToolCalls)
	}
	return m
}

// getModelFallbackPool returns an ordered list of models to try if the primary model fails or hits quota
func getModelFallbackPool(currentModel string, baseURL string) []string {
	isGemini := strings.Contains(baseURL, "googleapis.com") || strings.Contains(currentModel, "gemini")
	isOpenRouter := strings.Contains(baseURL, "openrouter.ai")

	var pool []string
	if isGemini {
		pool = []string{
			"gemini-3.1-flash-lite",
			"gemini-3-flash-preview",
			"gemini-2.5-flash-lite",
			"gemini-3.5-flash",
			"gemini-2.5-flash",
			"gemini-2.0-flash",
		}
	} else if isOpenRouter {
		pool = []string{
			"nvidia/nemotron-3.5-super-vl:free",
			"google/gemini-2.0-flash-exp:free",
			"google/gemini-2.0-flash-thinking-exp:free",
			"openrouter/free",
			"nvidia/nemotron-3.5-lightning:free",
			"meta-llama/llama-3.3-70b-instruct:free",
		}
	} else {
		return []string{currentModel}
	}

	result := []string{currentModel}
	for _, m := range pool {
		if m != currentModel {
			result = append(result, m)
		}
	}
	return result
}

// Chat sends messages and returns response with potential tool calls
func (c *Client) Chat(ctx context.Context, messages []models.Message, tools []models.ToolDefinition) (*models.Response, error) {
	chatMessages := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		cm := chatMessage{
			Role: msg.Role,
		}
		if msg.Content != "" || (msg.Role != "assistant" && len(msg.ToolCalls) == 0) {
			cCopy := msg.Content
			cm.Content = &cCopy
		}
		if msg.ToolCallID != "" {
			cm.ToolCallID = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			cm.ToolCalls = make([]chatToolCall, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				argsBytes, _ := json.Marshal(tc.Arguments)
				cm.ToolCalls[i] = chatToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: chatFunctionCall{
						Name:      tc.Name,
						Arguments: string(argsBytes),
					},
					ExtraContent: tc.ExtraContent,
				}
			}
		}
		chatMessages = append(chatMessages, cm)
	}

	chatTools := make([]chatTool, len(tools))
	for i, tool := range tools {
		chatTools[i] = chatTool{
			Type: "function",
			Function: chatFunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		}
	}

	modelsToTry := getModelFallbackPool(c.model, c.baseURL)
	endpoint := fmt.Sprintf("%s/chat/completions", strings.TrimRight(c.baseURL, "/"))

	startTime := time.Now()

	var finalResp chatResponse
	var lastErr error
	success := false

	for modelIdx, candidateModel := range modelsToTry {
		reqPayload := chatRequest{
			Model:               candidateModel,
			MaxCompletionTokens: c.maxTokens,
			Messages:            chatMessages,
			Tools:               chatTools,
		}

		reqBodyBytes, err := json.Marshal(reqPayload)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize request: %w", err)
		}

		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"model":      candidateModel,
				"messages":   len(messages),
				"max_tokens": c.maxTokens,
			}).Debug("Sending chat request")
		}

		maxAttempts := 3
		for attempt := 0; attempt < maxAttempts; attempt++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			httpReq, reqErr := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBodyBytes))
			if reqErr != nil {
				lastErr = reqErr
				break
			}
			httpReq.Header.Set("Content-Type", "application/json")
			if c.apiKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
			}

			httpResp, doErr := c.httpClient.Do(httpReq)
			if doErr != nil {
				lastErr = doErr
				time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				continue
			}

			bodyBytes, readErr := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if readErr != nil {
				lastErr = readErr
				continue
			}

			if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
				var cr chatResponse
				if unmarshalErr := json.Unmarshal(bodyBytes, &cr); unmarshalErr != nil {
					lastErr = fmt.Errorf("failed to decode response: %w", unmarshalErr)
					continue
				}
				finalResp = cr
				success = true
				if candidateModel != c.model {
					if c.logger != nil {
						c.logger.WithFields(logrus.Fields{
							"previous_model": c.model,
							"new_model":      candidateModel,
						}).Info("Successfully switched to fallback model")
					}
					c.model = candidateModel
				}
				break
			}

			// Parse error from upstream
			var errObj struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(bodyBytes, &errObj)
			errMsg := errObj.Error.Message
			if errMsg == "" {
				errMsg = string(bodyBytes)
			}
			lastErr = fmt.Errorf("openai error: %d %s", httpResp.StatusCode, errMsg)

			if c.onRetry != nil {
				c.onRetry(attempt+1, maxAttempts, time.Second, lastErr)
			}

			if httpResp.StatusCode == 429 {
				isDailyQuota := strings.Contains(errMsg, "per day limit") ||
					strings.Contains(errMsg, "DayPerProject") ||
					strings.Contains(errMsg, "limit: 20") ||
					strings.Contains(errMsg, "3h") ||
					strings.Contains(errMsg, "RESOURCE_EXHAUSTED")

				if isDailyQuota && modelIdx < len(modelsToTry)-1 {
					if c.logger != nil {
						c.logger.Warnf("Model %s exhausted daily quota, failing over to next model: %s", candidateModel, modelsToTry[modelIdx+1])
					}
					break
				}

				// If there's another model in the pool, switch to it immediately instead of waiting
				if modelIdx < len(modelsToTry)-1 {
					if c.logger != nil {
						c.logger.Warnf("Model %s hit rate limit (429), failing over to next model: %s", candidateModel, modelsToTry[modelIdx+1])
					}
					break
				}

				// If this is the last or only model, wait and retry
				sleepDuration := time.Duration(12*(attempt+1)) * time.Second
				if c.logger != nil {
					c.logger.Warnf("Model %s rate limited. Waiting %v before attempt %d", candidateModel, sleepDuration, attempt+2)
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(sleepDuration):
				}
				continue
			}

			// For 500-504 server overload, try next model if available
			if httpResp.StatusCode >= 500 && httpResp.StatusCode <= 504 {
				if modelIdx < len(modelsToTry)-1 {
					if c.logger != nil {
						c.logger.Warnf("Model %s returned status %d, trying next model: %s", candidateModel, httpResp.StatusCode, modelsToTry[modelIdx+1])
					}
					break
				}
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}

			// Fatal errors (400, 401, 403, context length)
			break
		}

		if success {
			break
		}
	}

	duration := time.Since(startTime)

	if !success {
		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"model":    c.model,
				"duration": fmt.Sprintf("%.2fs", duration.Seconds()),
				"error":    lastErr.Error(),
			}).Debug("AI request failed")
		}
		return nil, fmt.Errorf("openai error: %w", lastErr)
	}

	if len(finalResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	resp := finalResp

	if c.logger != nil {
		c.logger.WithFields(logrus.Fields{
			"model":         c.model,
			"duration":      fmt.Sprintf("%.2fs", duration.Seconds()),
			"prompt_tokens": resp.Usage.PromptTokens,
			"comp_tokens":   resp.Usage.CompletionTokens,
			"total_tokens":  resp.Usage.TotalTokens,
		}).Debug("AI response received")
	}

	choice := resp.Choices[0]
	var respContent string
	if choice.Message.Content != nil {
		respContent = *choice.Message.Content
	}
	result := &models.Response{
		Content:          respContent,
		StopReason:       choice.FinishReason,
		TokensUsed:       resp.Usage.TotalTokens,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}

	for _, tc := range choice.Message.ToolCalls {
		args := make(map[string]interface{})
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("failed to parse tool arguments: %w", err)
		}
		result.ToolCalls = append(result.ToolCalls, models.ToolCall{
			ID:           tc.ID,
			Name:         tc.Function.Name,
			Arguments:    args,
			ExtraContent: tc.ExtraContent,
		})
	}

	return result, nil
}

// BuildToolResultMessage creates a message for tool result
func BuildToolResultMessage(toolCallID, content string) models.Message {
	return models.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: toolCallID,
	}
}

// BuildAssistantMessageWithToolCalls creates assistant message with tool calls
func BuildAssistantMessageWithToolCalls(content string, toolCalls []models.ToolCall) models.Message {
	return models.Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
}

// ConvertToolCallsToOpenAI converts our ToolCall to openai.ToolCall
func ConvertToolCallsToOpenAI(calls []models.ToolCall) []openai.ToolCall {
	result := make([]openai.ToolCall, len(calls))
	for i, tc := range calls {
		args, _ := json.Marshal(tc.Arguments)
		result[i] = openai.ToolCall{
			ID:   tc.ID,
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      tc.Name,
				Arguments: string(args),
			},
		}
	}
	return result
}
