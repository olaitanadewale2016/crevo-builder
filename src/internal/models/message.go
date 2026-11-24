package models

// ImageAttachment represents a raster image file attached to a user prompt
type ImageAttachment struct {
	Filename   string `json:"filename,omitempty"`
	MimeType   string `json:"mime_type,omitempty"`
	URL        string `json:"url,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

// Message represents a conversation message
type Message struct {
	Role        string            `json:"role"`
	Content     string            `json:"content"`
	ToolCalls   []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID  string            `json:"tool_call_id,omitempty"`
	Images      []ImageAttachment `json:"images,omitempty"`
	ImageBase64 string            `json:"image_base64,omitempty"`
}

// ToolCall represents an AI tool call (simplified)
type ToolCall struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Arguments    map[string]interface{} `json:"arguments"`
	ExtraContent map[string]interface{} `json:"extra_content,omitempty"`
}

// Response from AI provider
type Response struct {
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls"`
	StopReason string     `json:"stop_reason"`
	TokensUsed int        `json:"tokens_used"`

	// Granular token tracking
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`

	// Cache tracking for Anthropic
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}
