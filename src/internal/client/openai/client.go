package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"crevo-builder/internal/models"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sirupsen/logrus"
)

// Client implements AIProvider using OpenAI or compatible APIs
type Client struct {
	client          *openai.Client
	httpClient      *http.Client
	apiKey          string
	baseURL         string
	model           string
	maxTokens       int
	reasoningEffort string
	logger          *logrus.Logger
	retryConfig     models.RetryConfig
	onRetry         models.RetryCallback
}

// Ensure Client implements AIProvider
var _ models.AIProvider = (*Client)(nil)

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Tools               []chatTool    `json:"tools,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
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
		client:          oaiClient,
		httpClient:      httpClient,
		apiKey:          cfg.APIKey,
		baseURL:         baseURL,
		model:           model,
		maxTokens:       maxTokens,
		reasoningEffort: cfg.ReasoningEffort,
		logger:          logger,
		retryConfig:     retryConfig,
		onRetry:         onRetry,
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

// getModelFallbackPool returns an ordered list of models to try.
// If a specific model is set by the admin or user, we strictly use that model without overriding.
func getModelFallbackPool(currentModel string, baseURL string) []string {
	if currentModel != "" {
		return []string{currentModel}
	}

	// Only if no model was specified at all do we provide a sensible default:
	if strings.Contains(baseURL, "googleapis.com") || strings.Contains(baseURL, "gemini") {
		return []string{"gemini-2.5-flash-lite"}
	}
	if strings.Contains(baseURL, "openrouter.ai") {
		return []string{"openrouter/free"}
	}
	return []string{"gpt-4o"}
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
		if c.reasoningEffort != "" && supportsReasoningEffort(candidateModel) {
			reqPayload.ReasoningEffort = c.reasoningEffort
		} else if strings.Contains(strings.ToLower(candidateModel), "gemini-2.5") {
			// For Gemini 2.5, cap reasoning_effort to "low" to avoid excessive thinking latency
			reqPayload.ReasoningEffort = "low"
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

		maxAttempts := 5
		reasoningStripped := false
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

				// Check if the AI provider filtered out tool calls due to malformed call format
				if len(cr.Choices) > 0 {
					finishReason := cr.Choices[0].FinishReason
					if strings.Contains(finishReason, "MALFORMED_FUNCTION_CALL") && len(cr.Choices[0].Message.ToolCalls) == 0 {
						lastErr = fmt.Errorf("AI provider filtered malformed function call: %s", finishReason)
						if c.logger != nil {
							c.logger.Warnf("Model %s returned malformed function call (%s), failing over", candidateModel, finishReason)
						}
						break // Try next model in pool
					}
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

			// If upstream rejected reasoning_effort with HTTP 400, strip it and retry immediately
			if httpResp.StatusCode == 400 && reqPayload.ReasoningEffort != "" && !reasoningStripped {
				reasoningStripped = true
				reqPayload.ReasoningEffort = ""
				if reBytes, mErr := json.Marshal(reqPayload); mErr == nil {
					reqBodyBytes = reBytes
					if c.logger != nil {
						c.logger.Warnf("Model %s rejected reasoning_effort (400), retrying without reasoning_effort", candidateModel)
					}
					continue
				}
			}

			if c.onRetry != nil {
				c.onRetry(attempt+1, maxAttempts, time.Second, lastErr)
			}

			if httpResp.StatusCode == 429 {
				isTrueDailyLimit := strings.Contains(strings.ToLower(errMsg), "per day") ||
					strings.Contains(strings.ToLower(errMsg), "per-day") ||
					strings.Contains(errMsg, "DayPerProject") ||
					strings.Contains(errMsg, "limit: 20") ||
					strings.Contains(strings.ToLower(errMsg), "daily limit")

				// Only fail over to another model if it is an actual 24h daily quota exhaustion
				if isTrueDailyLimit && modelIdx < len(modelsToTry)-1 {
					if c.logger != nil {
						c.logger.Warnf("Model %s exhausted daily quota, failing over to next model: %s", candidateModel, modelsToTry[modelIdx+1])
					}
					break
				}

				// Extract retry delay from header, JSON details (e.g. Google's retryDelay), or adaptive backoff
				sleepDuration := extractRetryDelay(httpResp.Header.Get("Retry-After"), bodyBytes, attempt)

				if c.logger != nil {
					c.logger.Warnf("Model %s rate limited (429). Waiting %v before attempt %d/%d (upstream: %s)", candidateModel, sleepDuration, attempt+2, maxAttempts, errMsg)
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

// supportsReasoningEffort returns true for models that accept the reasoning_effort field
func supportsReasoningEffort(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") ||
		strings.HasPrefix(m, "gpt-5") ||
		strings.Contains(m, "reasoning") ||
		strings.Contains(m, "thinking") ||
		strings.Contains(m, "reasoner") ||
		strings.Contains(m, "grok-3-mini") ||
		strings.Contains(m, "gemini-2.5")
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

// extractRetryDelay attempts to parse retry delay from headers or Gemini/OpenAI error body
func extractRetryDelay(headerVal string, bodyBytes []byte, attempt int) time.Duration {
	// 1. Check HTTP header (standard Retry-After)
	if headerVal != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(headerVal)); err == nil && secs > 0 {
			return time.Duration(secs+1) * time.Second
		}
	}

	bodyStr := string(bodyBytes)

	// 2. Check JSON details for Google RPC RetryInfo: "retryDelay": "23s"
	var geminiErr struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &geminiErr); err == nil {
		for _, d := range geminiErr.Error.Details {
			if d.RetryDelay != "" {
				clean := strings.TrimSuffix(strings.TrimSpace(d.RetryDelay), "s")
				if secs, parseErr := strconv.ParseFloat(clean, 64); parseErr == nil && secs > 0 {
					return time.Duration(secs+1.5) * time.Second
				}
			}
		}
		if geminiErr.Error.Message != "" {
			bodyStr = geminiErr.Error.Message + " " + bodyStr
		}
	}

	// 3. Search for "retry after X" or "retry in X"
	lower := strings.ToLower(bodyStr)
	if idx := strings.Index(lower, "retry after "); idx != -1 {
		sub := lower[idx+len("retry after "):]
		var secs int
		if _, err := fmt.Sscanf(sub, "%d", &secs); err == nil && secs > 0 {
			return time.Duration(secs+2) * time.Second
		}
	}
	if idx := strings.Index(lower, "retry in "); idx != -1 {
		sub := lower[idx+len("retry in "):]
		var secs int
		if _, err := fmt.Sscanf(sub, "%d", &secs); err == nil && secs > 0 {
			return time.Duration(secs+2) * time.Second
		}
	}

	// 4. Default adaptive backoff capped to avoid multi-minute silent hangs
	backoffs := []int{6, 12, 20, 30, 40}
	if attempt < len(backoffs) {
		return time.Duration(backoffs[attempt]) * time.Second
	}
	return 40 * time.Second
}
