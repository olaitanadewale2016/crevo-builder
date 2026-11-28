package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crevo-builder/internal/client"
	"crevo-builder/internal/client/laravel"
	"crevo-builder/internal/executor"
	"crevo-builder/internal/logging"
	"crevo-builder/internal/models"
	"crevo-builder/internal/summarizer"

	"github.com/sirupsen/logrus"
)

// FactoryImport is a type alias to avoid import issues
type Factory = client.Factory

// Fun phrase pools for action messages
var createPhrases = []string{
	"Laying the bricks for",
	"Conjuring",
	"Cooking up",
	"Crafting",
	"Bringing to life",
	"Building",
	"Weaving",
	"Whipping up",
}

var editPhrases = []string{
	"Polishing",
	"Sprinkling magic on",
	"Fine-tuning",
	"Adding sparkle to",
	"Seasoning",
	"Touching up",
}

var readPhrases = []string{
	"Studying",
	"Checking the blueprints in",
	"Peeking at",
	"Consulting",
	"Scanning",
}

var explorePhrases = []string{
	"Surveying the land",
	"Getting the lay of the land",
	"Mapping things out",
	"Scouting the project",
}

// randomPhrase returns a random phrase from the given pool
func randomPhrase(phrases []string) string {
	return phrases[rand.Intn(len(phrases))]
}

// formatProjectCapabilities formats project capabilities as JSON for tool result
func formatProjectCapabilities(caps *models.ProjectCapabilities) string {
	var firebase, supabase, storage, internalAI string

	if caps.Firebase != nil {
		firebase = fmt.Sprintf(`"firebase":{"enabled":%t,"available":%t,"collection_prefix":"%s"}`,
			caps.Firebase.Enabled, caps.Firebase.Available, caps.Firebase.CollectionPrefix)
	} else {
		firebase = `"firebase":{"enabled":false,"available":false,"collection_prefix":""}`
	}

	if caps.Supabase != nil {
		supabase = fmt.Sprintf(`"supabase":{"enabled":%t,"available":%t,"url":"%s","table_prefix":"%s"}`,
			caps.Supabase.Enabled, caps.Supabase.Available, caps.Supabase.URL, caps.Supabase.TablePrefix)
	} else {
		supabase = `"supabase":{"enabled":false,"available":false,"url":"","table_prefix":""}`
	}

	if caps.Storage != nil {
		storage = fmt.Sprintf(`"storage":{"enabled":%t,"max_file_size_mb":%d}`,
			caps.Storage.Enabled, caps.Storage.MaxFileSizeMB)
	} else {
		storage = `"storage":{"enabled":false,"max_file_size_mb":0}`
	}

	if caps.InternalAI != nil {
		internalAI = fmt.Sprintf(`"internal_ai":{"enabled":%t,"admin_only":%t}`,
			caps.InternalAI.Enabled, caps.InternalAI.AdminOnly)
	} else {
		internalAI = `"internal_ai":{"enabled":false,"admin_only":true}`
	}

	return fmt.Sprintf("{%s,%s,%s,%s}", firebase, supabase, storage, internalAI)
}

// handleGetFirestoreCollections fetches Firestore collections from Laravel
func (r *Runner) handleGetFirestoreCollections(ctx context.Context, session *Session) string {
	// Check if Firebase is enabled via capabilities
	caps := session.GetProjectCapabilities()
	if caps == nil || caps.Firebase == nil || !caps.Firebase.Enabled {
		return `{"success":false,"error":"firebase_not_enabled","message":"Firebase is not enabled for this project.","collections":[]}`
	}

	// Get Laravel URL from session
	laravelURL := session.GetLaravelURL()
	if laravelURL == "" {
		return `{"success":false,"error":"configuration_error","message":"Laravel URL not configured.","collections":[]}`
	}

	// Get server key from executor
	serverKey := r.executor.GetServerKey()
	if serverKey == "" {
		return `{"success":false,"error":"configuration_error","message":"Server key not configured.","collections":[]}`
	}

	// Create client and fetch collections
	client := laravel.NewClient(laravelURL, serverKey)

	// Use workspace ID as project ID (they map 1:1)
	resp, err := client.GetFirestoreCollections(ctx, session.WorkspaceID)
	if err != nil {
		r.logger.WithFields(logrus.Fields{
			"session_id": session.ID,
			"project_id": session.WorkspaceID,
			"error":      err.Error(),
		}).Warn("Failed to fetch Firestore collections")

		return fmt.Sprintf(`{"success":false,"error":"api_error","message":"Failed to fetch collections: %s","collections":[]}`, err.Error())
	}

	// Return JSON response
	jsonBytes, err := json.Marshal(resp)
	if err != nil {
		return `{"success":false,"error":"encoding_error","message":"Failed to encode response","collections":[]}`
	}

	return string(jsonBytes)
}

// HistoryMsg represents a message in conversation history
type HistoryMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HistoryInput wraps history with metadata for the runner
type HistoryInput struct {
	Messages    []HistoryMsg
	IsCompacted bool // If true, skip summarization (history already compacted)
}

// Runner executes the agent loop
type Runner struct {
	executor        *executor.Executor
	aiConfig        models.ProviderConfig
	summarizer      *summarizer.Summarizer
	logger          *logrus.Logger
	providerFactory *Factory
}

// NewRunner creates a new runner with configuration
func NewRunner(workspacePath, templatePath string, agentCfg, summarizerCfg models.ProviderConfig, logger *logrus.Logger, toolConfig models.ToolExecutionConfig) *Runner {
	return &Runner{
		executor:        executor.NewExecutor(workspacePath, logger, toolConfig),
		aiConfig:        agentCfg,
		summarizer:      summarizer.NewSummarizerWithConfig(summarizerCfg),
		logger:          logger,
		providerFactory: client.NewFactory(logger, models.DefaultRetryConfig(), nil),
	}
}

// NewRunnerWithTemplate creates a new runner with template fetching support
func NewRunnerWithTemplate(workspacePath, templatePath string, agentCfg, summarizerCfg models.ProviderConfig, logger *logrus.Logger, serverKey, laravelURL string, toolConfig models.ToolExecutionConfig) *Runner {
	return &Runner{
		executor:        executor.NewExecutorWithTemplate(workspacePath, serverKey, laravelURL, logger, toolConfig),
		aiConfig:        agentCfg,
		summarizer:      summarizer.NewSummarizerWithConfig(summarizerCfg),
		logger:          logger,
		providerFactory: client.NewFactory(logger, models.DefaultRetryConfig(), nil),
	}
}

// loadTemplatePrompts reads template.json from workspace and extracts prompt sections
func (r *Runner) loadTemplatePrompts(workspacePath string) *models.TemplatePrompts {
	templatePath := filepath.Join(workspacePath, "template.json")

	data, err := os.ReadFile(templatePath)
	if err != nil {
		r.logger.WithFields(logrus.Fields{
			"path": templatePath,
		}).Debug("No template.json found, using default prompts")
		return nil
	}

	var metadata models.TemplateMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		r.logger.WithFields(logrus.Fields{
			"path":  templatePath,
			"error": err.Error(),
		}).Warn("Failed to parse template.json for prompts")
		return nil
	}

	if metadata.Prompts != nil {
		r.logger.WithFields(logrus.Fields{
			"prompt_count": len(metadata.Prompts.Prompts),
		}).Debug("Loaded template prompts")
	}

	return metadata.Prompts
}

// loadTemplateMetadata reads template.json from workspace and returns the full metadata.
// Returns nil if template.json doesn't exist or is invalid (graceful fallback).
func (r *Runner) loadTemplateMetadata(workspacePath string) *models.TemplateMetadata {
	templatePath := filepath.Join(workspacePath, "template.json")

	data, err := os.ReadFile(templatePath)
	if err != nil {
		return nil
	}

	var metadata models.TemplateMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		r.logger.WithFields(logrus.Fields{
			"path":  templatePath,
			"error": err.Error(),
		}).Warn("Failed to parse template.json for metadata")
		return nil
	}

	r.logger.WithFields(logrus.Fields{
		"template_name": metadata.Name,
		"pages":         len(metadata.AvailablePages),
	}).Debug("Loaded template metadata")

	return &metadata
}

// getToolRecoveryGuidance returns specific recovery steps for a failed tool
func getToolRecoveryGuidance(toolName string) string {
	switch toolName {
	case "editFile":
		return `RECOVERY STEPS:
1. Use readFile to see the CURRENT file content (it may have changed since you last read it)
2. Copy the EXACT text you want to replace, including all whitespace and newlines
3. If the text doesn't exist anymore, use searchFiles to find where it moved
4. For changes affecting >20 lines, consider using createFile instead
5. If this file has failed multiple edits, use createFile to rewrite it completely`
	case "createFile":
		return `RECOVERY STEPS:
1. Read the error message - look for the specific line/column with the syntax issue
2. Check for: missing closing brackets }, unclosed JSX tags </>, unmatched quotes
3. Verify all imports reference files that actually exist
4. Ensure there's exactly ONE default export`
	case "verifyBuild":
		return `RECOVERY STEPS:
1. Read the SPECIFIC file and line mentioned in the error using readFile
2. Common issues: missing imports, typos in component names, unclosed tags
3. Fix ONE error at a time, then rerun verifyBuild
4. Don't guess - always read the problematic code first before attempting a fix`
	case "verifyIntegration":
		return `RECOVERY STEPS:
1. Read src/routes.tsx to see current imports and routes
2. Ensure the page file exists in src/pages/
3. Check that the import path matches the actual file name (case-sensitive)
4. Verify the route entry has all required fields: path, label, element, showInNav, layout`
	default:
		return "Try a different approach, or use readFile to understand the current state before retrying."
	}
}

func normalizeRasterImageMime(mimeType string) string {
	clean := strings.TrimSpace(strings.ToLower(strings.Split(mimeType, ";")[0]))
	switch clean {
	case "image/jpg":
		return "image/jpeg"
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return clean
	default:
		return ""
	}
}

func isPublicRemoteHTTPURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return false
	}
	return true
}

func prepareImageAttachments(ctx context.Context, raw []models.ImageAttachment, logger *logrus.Logger) []models.ImageAttachment {
	if len(raw) == 0 {
		return nil
	}
	const maxImages = 6
	const maxBytes = 4 * 1024 * 1024
	httpClient := &http.Client{Timeout: 10 * time.Second}

	result := make([]models.ImageAttachment, 0, len(raw))
	for _, img := range raw {
		if len(result) >= maxImages {
			break
		}
		mime := normalizeRasterImageMime(img.MimeType)
		if mime == "" {
			continue
		}
		dataB64 := strings.TrimSpace(img.DataBase64)
		if dataB64 == "" && isPublicRemoteHTTPURL(img.URL) {
			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, img.URL, nil)
			if reqErr == nil {
				req.Header.Set("User-Agent", models.HTTPUserAgent)
				resp, doErr := httpClient.Do(req)
				if doErr == nil {
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
						if readErr == nil && len(body) > 0 && len(body) <= maxBytes {
							dataB64 = base64.StdEncoding.EncodeToString(body)
						}
					}
					resp.Body.Close()
				} else if logger != nil {
					logger.WithError(doErr).Debugf("Failed to download attached image URL %s", img.URL)
				}
			}
			cancel()
		}
		if dataB64 == "" {
			continue
		}
		result = append(result, models.ImageAttachment{
			Filename:   img.Filename,
			MimeType:   mime,
			URL:        img.URL,
			DataBase64: dataB64,
		})
	}
	return result
}

// Run executes the agent loop for a session
func (r *Runner) Run(ctx context.Context, session *Session, goal string, historyInput HistoryInput, maxIterations int) error {
	return r.RunWithImages(ctx, session, goal, historyInput, maxIterations, nil)
}

// RunWithImages executes the agent loop for a session with optional attached raster images
func (r *Runner) RunWithImages(ctx context.Context, session *Session, goal string, historyInput HistoryInput, maxIterations int, attachedImages []models.ImageAttachment) error {
	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	session.SetCancel(cancel)
	session.SetStatus(models.StatusRunning)

	// Scale maxIterations based on reasoning_effort (low, medium, high)
	switch r.aiConfig.ReasoningEffort {
	case "low":
		if maxIterations > 12 {
			maxIterations = 12
		}
	case "high":
		if maxIterations < 28 {
			maxIterations = 28
		}
	}

	streamer := session.GetStreamer()
	features := session.GetFeatures()

	// Pass theme preset to executor for application after useTemplate
	if preset := session.GetThemePreset(); preset != nil {
		r.executor.SetThemePreset(preset)
	}

	// Initialize credit enforcement from config
	if r.aiConfig.RemainingBuildCredits > 0 {
		session.SetRemainingCredits(r.aiConfig.RemainingBuildCredits)
		r.logger.WithFields(logrus.Fields{
			"session_id":        session.ID,
			"remaining_credits": r.aiConfig.RemainingBuildCredits,
		}).Info("Credit enforcement enabled")
	}

	// Log session start with visual separator
	logging.LogSessionStart(r.logger, session.ID, session.WorkspaceID, goal, maxIterations)

	defer func() {
		if session.GetStatus() == models.StatusRunning {
			session.SetStatus(models.StatusCompleted)
		}
		streamer.Close()
	}()

	// Create AI provider with retry config based on feature flags
	var provider models.AIProvider
	if features.RetryLLM {
		// Create retry callback to send events
		retryCallback := func(attempt, maxRetries int, delay time.Duration, err error) {
			reason := err.Error()
			if features.GranularEvents {
				streamer.SendRetry(attempt, maxRetries, delay.Milliseconds(), reason)
			}
			r.logger.WithFields(logrus.Fields{
				"session_id":  session.ID,
				"attempt":     attempt,
				"max_retries": maxRetries,
				"delay_ms":    delay.Milliseconds(),
				"reason":      reason,
			}).Debug("LLM retry")
		}
		// Create factory with retry callback
		retryFactory := client.NewFactory(r.logger, models.DefaultRetryConfig(), retryCallback)
		var err error
		provider, err = retryFactory.CreateProvider(r.aiConfig)
		if err != nil {
			return fmt.Errorf("failed to create AI provider: %w", err)
		}
	} else {
		var err error
		provider, err = r.providerFactory.CreateProvider(r.aiConfig)
		if err != nil {
			return fmt.Errorf("failed to create AI provider: %w", err)
		}
	}

	// Load template prompts and metadata from workspace whenever template.json exists
	// (loadTemplatePrompts/loadTemplateMetadata return nil safely on empty workspaces)
	workspaceDir := r.executor.GetWorkspacePath()
	templatePrompts := r.loadTemplatePrompts(workspaceDir)
	templateMetadata := r.loadTemplateMetadata(workspaceDir)

	// Calculate token budget for the system prompt (reserve ~60% for history + response)
	tokenBudget := 0
	if r.aiConfig.ContextWindow > 0 {
		tokenBudget = int(float64(r.aiConfig.ContextWindow) * 0.4)
	}

	// Build system prompt with template injections and workspace reference
	promptCfg := PromptConfig{
		ProjectName:      "Project",
		WorkspacePath:    r.executor.GetWorkspacePath(),
		Goal:             goal,
		TemplatePrompts:  templatePrompts,
		TemplateMetadata: templateMetadata,
		Capabilities:     session.GetProjectCapabilities(),
		ThemePreset:      session.GetThemePreset(),
		Compact:          getModelTier(r.aiConfig.ProviderType, r.aiConfig.Model) == "standard",
		TokenBudget:      tokenBudget,
	}
	if customPrompts := session.GetCustomPrompts(); customPrompts != nil {
		promptCfg.CustomPrompt = customPrompts.SystemPrompt
		promptCfg.CustomCompactPrompt = customPrompts.CompactPrompt
	}
	systemPrompt, err := BuildSystemPrompt(promptCfg)
	if err != nil {
		session.SetError(err.Error())
		session.SetStatus(models.StatusFailed)
		return fmt.Errorf("building system prompt: %w", err)
	}

	// Append effort-level instruction guidance
	switch r.aiConfig.ReasoningEffort {
	case "low":
		systemPrompt += "\n\n[EFFORT LEVEL: LOW — FAST MODE]\nExecute the request directly and concisely in as few tool calls as possible. Skip lengthy exploratory planning and focus on immediate, high-speed delivery."
	case "high":
		systemPrompt += "\n\n[EFFORT LEVEL: HIGH — DEEP ARCHITECTURAL MODE]\nApply deep reasoning, comprehensive UI/UX polish, rich micro-interactions, responsive layouts, and thorough multi-component implementation."
	}

	// Initialize conversation with system prompt
	messages := []models.Message{
		{Role: "system", Content: systemPrompt},
	}

	// Process history
	history := historyInput.Messages
	if len(history) > 0 {
		if historyInput.IsCompacted {
			// Already compacted by Laravel - use directly without summarization
			r.logger.WithFields(logrus.Fields{
				"session_id":     session.ID,
				"history_length": len(history),
			}).Debug("Using pre-compacted history, skipping summarization")

			for _, h := range history {
				messages = append(messages, models.Message{
					Role:    h.Role,
					Content: h.Content,
				})
			}
		} else {
			// Not compacted - run through summarizer
			turns := make([]summarizer.Turn, len(history))
			for i, h := range history {
				turns[i] = summarizer.Turn{Role: h.Role, Content: h.Content}
			}

			// Send status BEFORE summarization to show animation during API call
			if len(turns) > r.summarizer.GetKeepRecent() {
				streamer.SendStatus("compacting", "Summarizing conversation history...")
			}

			// Process with summarization if needed
			state, err := r.summarizer.Process(ctx, turns)
			if err == nil {
				// Track summarization tokens for credit depletion
				if state.TotalTokens > 0 {
					session.UpdateTokens(state.PromptTokens, state.CompletionTokens, state.TotalTokens)
					promptTok, compTok, totalTok, ctxTok := session.GetTokenStats()
					streamer.SendTokenUsage(promptTok, compTok, totalTok, ctxTok)
					r.logger.WithFields(logrus.Fields{
						"session_id":            session.ID,
						"summarizer_prompt":     state.PromptTokens,
						"summarizer_completion": state.CompletionTokens,
						"summarizer_total":      state.TotalTokens,
					}).Debug("Added summarization tokens to session")
				}

				// Build compacted history to send back to Laravel
				var compactedHistory []models.HistoryMessage

				// Add summary if generated
				if state.Summary != "" {
					// Add summary as assistant message for Laravel storage
					compactedHistory = append(compactedHistory, models.HistoryMessage{
						Role:    "assistant",
						Content: "[Previous conversation summary]\n" + state.Summary,
					})

					messages = append(messages, models.Message{
						Role:    "system",
						Content: "CONVERSATION HISTORY:\n" + state.Summary,
					})
				}

				// Add recent turns in full
				for _, t := range state.RecentTurns {
					compactedHistory = append(compactedHistory, models.HistoryMessage{
						Role:    t.Role,
						Content: t.Content,
					})
					messages = append(messages, models.Message{
						Role:    t.Role,
						Content: t.Content,
					})
				}

				// Send summarization complete event with compacted history
				if state.Summary != "" {
					oldTokens := summarizer.EstimateTokens(state.Summary)
					for _, t := range turns {
						oldTokens += summarizer.EstimateTokens(t.Content)
					}
					// Estimate new tokens: summary + recent turns
					newTokens := summarizer.EstimateTokens(state.Summary)
					for _, t := range state.RecentTurns {
						newTokens += summarizer.EstimateTokens(t.Content)
					}
					turnsCompacted := len(turns) - len(state.RecentTurns)
					reductionPct := float64(0)
					if oldTokens > 0 {
						reductionPct = float64(oldTokens-newTokens) / float64(oldTokens) * 100
					}
					streamer.SendSummarizationComplete(models.SummarizationEventData{
						OldTokens:        oldTokens,
						NewTokens:        newTokens,
						ReductionPercent: reductionPct,
						TurnsCompacted:   turnsCompacted,
						TurnsKept:        len(state.RecentTurns),
						Message:          fmt.Sprintf("Compressed %d turns into summary, keeping %d recent turns", turnsCompacted, len(state.RecentTurns)),
						CompactedHistory: compactedHistory,
					})
				}
			} else {
				// Log summarizer failure for debugging
				r.logger.WithFields(logrus.Fields{
					"session_id": session.ID,
					"error":      err.Error(),
					"turns":      len(turns),
				}).Warn("Summarizer failed, using truncated history fallback")

				// Fallback: just use last 20 messages
				historyLimit := 20
				startIdx := 0
				if len(history) > historyLimit {
					startIdx = len(history) - historyLimit
				}
				for _, h := range history[startIdx:] {
					messages = append(messages, models.Message{
						Role:    h.Role,
						Content: h.Content,
					})
				}
			}
		}
	}

	// Inject persistent context (survives conversation compaction)
	if memContent := loadSiteMemory(r.executor.GetWorkspacePath()); memContent != "" {
		messages = append(messages, models.Message{
			Role:    "system",
			Content: "[SITE MEMORY — business context from previous sessions]\n" + memContent,
		})
	}
	if diContent := loadDesignIntelligence(r.executor.GetWorkspacePath()); diContent != "" {
		messages = append(messages, models.Message{
			Role:    "system",
			Content: "[DESIGN INTELLIGENCE — design decisions from previous sessions]\n" + diContent,
		})
	}

	// Prepare attached raster images for multimodal vision (skips SVG or oversized payloads)
	visionImages := prepareImageAttachments(ctx, attachedImages, r.logger)
	if len(visionImages) > 0 {
		streamer.SendAction("Analyzing", fmt.Sprintf("%d reference image(s)", len(visionImages)), "", "reading")
	}

	// Add current user message
	messages = append(messages, models.Message{
		Role:    "user",
		Content: goal,
		Images:  visionImages,
	})

	sessionCaps := session.GetProjectCapabilities()
	toolFilterOpts := ToolFilterOptions{
		WorkspaceHasFiles: workspaceHasSourceFiles(workspaceDir),
		FirebaseEnabled:   sessionCaps != nil && sessionCaps.Firebase != nil && sessionCaps.Firebase.Enabled,
		StorageEnabled:    sessionCaps != nil && sessionCaps.Storage != nil && sessionCaps.Storage.Enabled,
	}
	tools := GetToolsForSession(toolFilterOpts)

	// Send initial status
	streamer.SendStatus("running", "Let's build something awesome...")

	// Track last non-empty AI content for final message
	var lastAIContent string

	// Integration & build verification: track fix attempts to guarantee compiled dist/ before finishing
	integrationFixAttempts := 0
	const maxIntegrationFixes = 2
	var lastIntegrationError string
	integrationFinalNotified := false // Flag to track if we've sent final notification about unresolved issues
	buildFixAttempts := 0
	const maxBuildFixes = 3
	var lastBuildError string

	// Check whether prior conversation already contains an assistant clarification or greeting
	hasPriorAssistantTurn := false
	for _, h := range history {
		if h.Role == "assistant" {
			t := strings.TrimSpace(h.Content)
			if t != "" &&
				!strings.HasPrefix(t, "Build cancelled") &&
				!strings.HasPrefix(t, "Build failed") &&
				!strings.HasPrefix(t, "AI provider error") {
				hasPriorAssistantTurn = true
				break
			}
		}
	}

	// Empty response / no-code nudges & excessive scan steering
	emptyResponseNudged := false
	noCodeNudges := 0
	consecutiveReadOnlyTurns := 0
	clarifyingReplyAccepted := false

	// Set model-appropriate circuit breaker thresholds
	session.SetCircuitBreakerForModel(r.aiConfig.ProviderType, r.aiConfig.Model)
	circuitBreaker := session.GetCircuitBreaker()

	// Main agent loop - use local counter to allow continuation after reaching max_iterations
	// session.Iterations tracks cumulative count for billing/stats, but shouldn't block new runs
	iterationsRun := 0
	for iterationsRun < maxIterations {
		select {
		case <-ctx.Done():
			session.SetStatus(models.StatusCancelled)
			streamer.SendStatus("cancelled", "Agent cancelled")
			return ctx.Err()
		default:
		}

		iterationsRun++      // Track iterations for this run
		session.Iterations++ // Track cumulative iterations for billing

		// Send iteration_start event if granular events enabled
		if features.GranularEvents {
			streamer.SendIterationStart(iterationsRun, maxIterations)
		}

		// Log before AI call
		r.logger.WithFields(logrus.Fields{
			"session_id": session.ID,
			"iteration":  iterationsRun,
			"model":      r.aiConfig.Model,
			"messages":   len(messages),
		}).Debug("Calling AI provider")

		// Send thinking event at start to begin frontend timer
		streamer.SendThinking("", iterationsRun)

		// Call AI
		response, err := provider.Chat(ctx, messages, tools)
		if err != nil {
			// Treat as user cancellation ONLY when parent session ctx was cancelled (Stop / shutdown),
			// never when an individual provider HTTP call timed out (DeadlineExceeded).
			if errors.Is(ctx.Err(), context.Canceled) || (ctx.Err() != nil && models.IsCancelled(err)) {
				session.SetStatus(models.StatusCancelled)
				streamer.SendStatus("cancelled", "Build stopped")
				r.logger.WithFields(logrus.Fields{
					"session_id": session.ID,
					"iteration":  iterationsRun,
				}).Info("Session cancelled by user")
				return ctx.Err()
			}

			// Only complete gracefully if the AI ACTUALLY created or edited website source files during this run.
			// Metadata-only writes (updateSiteMemory, writeDesignIntelligence) and initial template extraction
			// MUST NOT hide an AI provider/rate-limit failure or trigger a fake "Build Complete" on the blank template!
			hasAiCodeChanges := r.executor != nil && r.executor.HasCodeChanges()
			if hasAiCodeChanges {
				session.SetFilesChanged(true)
				r.logger.WithFields(logrus.Fields{
					"session_id":    session.ID,
					"iteration":     iterationsRun,
					"files_changed": true,
					"error":         err.Error(),
				}).Warn("AI provider rate limited/errored after AI modified website code; completing session gracefully")

				// Compile Vite build into dist/ so preview works immediately
				if r.executor != nil {
					bResult, bErr := r.executor.Execute(ctx, "verifyBuild", nil)
					if bErr == nil && bResult != nil {
						session.SetBuildResult(bResult.Success, bResult.Content)
					}
				}

				successMsg := r.synthesizeBuildSummary(session.GetWorkspacePath(), session, "")
				streamer.SendMessage(successMsg)
				session.SetStatus(models.StatusCompleted)
				streamer.SendStatus("completed", "Build complete")
				streamer.SendComplete(models.CompleteData{
					Iterations:       iterationsRun,
					TokensUsed:       session.TokensUsed,
					FilesChanged:     true,
					BuildStatus:      session.GetBuildStatus(),
					BuildMessage:     "Website built successfully with updated files",
					BuildRequired:    session.IsBuildRequired(),
					Message:          successMsg,
					PromptTokens:     session.PromptTokens,
					CompletionTokens: session.CompletionTokens,
					Model:            r.aiConfig.Model,
				})
				return nil
			}

			// Real error before any files changed - handle as before
			session.SetStatus(models.StatusFailed)
			session.Error = err.Error()
			streamer.SendStatus("failed", "AI provider error: "+err.Error())
			streamer.SendError(err.Error())
			streamer.SendComplete(models.CompleteData{
				Iterations:       iterationsRun,
				TokensUsed:       session.TokensUsed,
				FilesChanged:     false,
				BuildStatus:      "failed",
				BuildMessage:     "AI provider error: " + err.Error(),
				PromptTokens:     session.PromptTokens,
				CompletionTokens: session.CompletionTokens,
				Model:            r.aiConfig.Model,
			})
			r.logger.WithFields(logrus.Fields{
				"session_id": session.ID,
				"iteration":  iterationsRun,
				"error":      err.Error(),
			}).Error("AI provider error")
			return fmt.Errorf("AI error: %w", err)
		}

		session.UpdateTokens(response.PromptTokens, response.CompletionTokens, response.TokensUsed)

		// Always emit token_usage after every AI turn for real-time credit deduction & UI updates
		if response.TokensUsed > 0 || features.GranularEvents {
			prompt, completion, total, context := session.GetTokenStats()
			streamer.SendTokenUsage(prompt, completion, total, context)
		}

		// Credit enforcement checks
		if session.ShouldWarnCredits() {
			percentUsed, _ := session.CheckCredits()
			_, _, runTotal := session.GetRunTokenStats()
			creditData := models.CreditEventData{
				UsedTokens:       runTotal, // Per-run tokens
				RemainingCredits: session.GetRemainingCredits() - runTotal,
				PercentUsed:      percentUsed,
				Message:          "You have used 80% of your remaining build credits. Buy or upgrade credits in Billing & Plans if needed.",
			}
			streamer.SendCreditWarning(creditData)
			r.logger.WithFields(logrus.Fields{
				"session_id":        session.ID,
				"percent_used":      percentUsed,
				"run_tokens_used":   runTotal,
				"total_tokens_used": session.TokensUsed,
			}).Warn("Credit warning threshold reached")
		}

		// Check if credits exceeded midway - stop immediately (no credit refund) and prompt user how to buy more
		percentUsed, exceeded := session.CheckCredits()
		if exceeded {
			_, _, runTotal := session.GetRunTokenStats()
			creditMsg := "Build credits exhausted mid-generation (consumed credits are non-refundable). Upgrade your plan or purchase more credits in Plans & Billing (/billing/plans), or connect your own AI API key in Profile Settings (/profile)."
			creditData := models.CreditEventData{
				UsedTokens:       runTotal, // Per-run tokens
				RemainingCredits: 0,
				PercentUsed:      percentUsed,
				Message:          creditMsg,
			}
			streamer.SendCreditExceeded(creditData)
			streamer.SendStatus("credit_limit", "Build credit limit reached")
			r.logger.WithFields(logrus.Fields{
				"session_id":        session.ID,
				"run_tokens_used":   runTotal,
				"total_tokens_used": session.TokensUsed,
				"limit":             session.GetRemainingCredits(),
			}).Warn("Credit limit exceeded, stopping session")

			// Preserve partial workspace output if home page was customized before exhaustion
			hasChanges := r.executor != nil && r.executor.HasCodeChanges() && executor.WorkspaceHasCustomizedApp(r.executor.GetWorkspacePath())
			session.SetFilesChanged(hasChanges)
			if hasChanges {
				if r.executor != nil && !executor.IsDistUpToDate(r.executor.GetWorkspacePath()) {
					if bResult, bErr := r.executor.Execute(ctx, "verifyBuild", nil); bErr == nil && bResult != nil {
						session.SetBuildResult(bResult.Success, bResult.Content)
					}
				}
				session.SetStatus(models.StatusCompleted)
			} else {
				session.SetStatus(models.StatusFailed)
				session.SetBuildResult(false, "Build credit limit reached")
			}
			exhaustedMsg := "Your build credits ran out midway during generation, so the build was stopped (credits already consumed during generation are non-refundable).\n\n**How to continue building:**\n1. Open **Billing & Plans** (`/billing/plans`) to upgrade your plan or purchase more monthly build credits.\n2. Or connect your own AI API key in **Profile Settings** (`/profile`) to build without consuming platform credits."
			streamer.SendMessage(exhaustedMsg)
			completeData := models.CompleteData{
				Iterations:       iterationsRun,
				TokensUsed:       session.TokensUsed,
				FilesChanged:     hasChanges,
				Message:          exhaustedMsg,
				BuildStatus:      session.GetBuildStatus(),
				BuildMessage:     "Build credit limit reached",
				BuildRequired:    session.IsBuildRequired(),
				PromptTokens:     session.PromptTokens,
				CompletionTokens: session.CompletionTokens,
				Model:            r.aiConfig.Model,
			}
			streamer.SendComplete(completeData)
			return nil
		}

		// Log AI response
		r.logger.WithFields(logrus.Fields{
			"session_id":  session.ID,
			"iteration":   iterationsRun,
			"tokens":      response.TokensUsed,
			"tool_calls":  len(response.ToolCalls),
			"stop_reason": response.StopReason,
		}).Debug("Received AI response")

		// Stream thinking/content and track last response
		if response.Content != "" {
			// Send thinking event
			streamer.SendThinking(response.Content, iterationsRun)
			lastAIContent = response.Content // Track for final message

			// Only stream intermediate progress messages while tool calls are still in progress;
			// final responses (len(ToolCalls)==0) are sent ONCE after verifyBuild passes so we never
			// emit premature messages (or leak "calling verifyBuild") before preview is compiled.
			if len(response.ToolCalls) > 0 && len(response.Content) > 100 && !strings.Contains(strings.ToLower(response.Content), "verifybuild") {
				streamer.SendMessage(response.Content)
			}

			// Log AI content (truncated)
			r.logger.WithFields(logrus.Fields{
				"session_id": session.ID,
				"content":    logging.TruncateForLog(response.Content, 500),
			}).Debug("AI response content")
		}

		// Check if done (no tool calls)
		if len(response.ToolCalls) == 0 {
			// Ensure actual customized code exists on '/' before allowing completion on a starter workspace
			// (unless this is a legitimate clarifying/storage-choice question or casual conversational reply)
			hasCodeChanges := r.executor != nil && r.executor.HasCodeChanges()
			isUneditedStarter := !executor.WorkspaceHasCustomizedApp(r.executor.GetWorkspacePath())
			if !hasCodeChanges && isUneditedStarter && isLegitimateClarifyingOrChatReply(goal, response.Content, hasPriorAssistantTurn) {
				clarifyingReplyAccepted = true
				r.logger.WithFields(logrus.Fields{
					"session_id": session.ID,
					"iteration":  iterationsRun,
				}).Info("Accepting legitimate clarifying/conversational reply without starter build error")
				break
			}
			if (!hasCodeChanges || isUneditedStarter) && noCodeNudges < 2 && (isUneditedStarter || iterationsRun <= 4) {
				noCodeNudges++
				r.logger.WithFields(logrus.Fields{
					"session_id":      session.ID,
					"iteration":       iterationsRun,
					"nudge_attempt":   noCodeNudges,
					"unedited_starts": isUneditedStarter,
				}).Warn("AI returned no tool calls before customizing root homepage, nudging to build website code")

				nudgeMsg := "STOP: You have NOT created or edited any website source files yet (such as src/custom.css or src/pages/Home.tsx). Reading files or saving site memory alone does NOT complete the build! Use writeFiles/createFile to write the complete app styling, components, and pages, then run verifyBuild before giving your final response."
				if hasCodeChanges && isUneditedStarter {
					nudgeMsg = "STOP: Your root homepage ('/') still renders the starter template placeholder ('Welcome to Your Website'). You MUST update src/pages/Home.tsx (or update the '/' route in src/routes.tsx) with the complete customized homepage requested by the user, then run verifyBuild before finishing."
				}
				messages = append(messages, models.Message{
					Role:    "user",
					Content: nudgeMsg,
				})
				continue
			}

			// If AI returned an empty response (no content, no tool calls), nudge it once
			// to provide a final summary.
			if response.Content == "" && !emptyResponseNudged {
				emptyResponseNudged = true
				r.logger.WithFields(logrus.Fields{
					"session_id": session.ID,
					"iteration":  iterationsRun,
				}).Debug("AI returned empty response, nudging for final summary")

				messages = append(messages, models.Message{
					Role:    "user",
					Content: "Please provide a brief summary of what has been set up and what the user can do next. Describe the key features and pages included in the project.",
				})
				continue
			}

			// Before completing, scan src/pages/ for unregistered pages and verify interactive links/routes
			// Skip on unedited starter workspaces or if we've already sent final notification about unresolved issues
			if (!isUneditedStarter || hasCodeChanges) && !integrationFinalNotified && integrationFixAttempts < maxIntegrationFixes {
				pageFiles := scanPageFiles(r.executor.GetWorkspacePath())
				if len(pageFiles) > 0 {
					streamer.SendStatus("verifying", "Checking page integration...")

					filesArg := make([]interface{}, len(pageFiles))
					for i, p := range pageFiles {
						filesArg[i] = p
					}

					verifyResult, err := r.executor.Execute(ctx, "verifyIntegration", map[string]interface{}{
						"files": filesArg,
					})
					if err == nil && !verifyResult.Success {
						// Check if this is the same error as last time (avoid infinite loop on same issue)
						currentError := verifyResult.Content
						if currentError == lastIntegrationError {
							r.logger.WithFields(logrus.Fields{
								"session_id": session.ID,
							}).Warn("Same integration error twice in a row, giving up on auto-fix")

							// Notify AI about unresolved issues before completing
							finalMsg := fmt.Sprintf(
								"⚠️ INTEGRATION ISSUES REMAIN (same error repeated):\n\n%s\n\nPlease inform the user about these unresolved integration issues in your final response.",
								currentError,
							)
							messages = append(messages, models.Message{
								Role:    "system",
								Content: finalMsg,
							})
							integrationFinalNotified = true
							continue // Let AI respond about the issues
						}
						lastIntegrationError = currentError

						integrationFixAttempts++
						if integrationFixAttempts >= maxIntegrationFixes {
							r.logger.WithFields(logrus.Fields{
								"session_id": session.ID,
								"attempt":    integrationFixAttempts,
							}).Warn("Max integration fix attempts reached")

							// Notify AI about unresolved issues before completing
							finalMsg := fmt.Sprintf(
								"⚠️ INTEGRATION ISSUES REMAIN after %d fix attempts:\n\n%s\n\nPlease inform the user about these unresolved integration issues in your final response.",
								integrationFixAttempts, verifyResult.Content,
							)
							messages = append(messages, models.Message{
								Role:    "system",
								Content: finalMsg,
							})
							integrationFinalNotified = true
							continue // Let AI respond about the issues
						}

						r.logger.WithFields(logrus.Fields{
							"session_id": session.ID,
							"attempt":    integrationFixAttempts,
						}).Info("Integration issues found, re-entering loop to fix")

						fixPrompt := fmt.Sprintf(
							"⚠️ INTEGRATION & NAVIGATION ISSUES DETECTED:\n\n%s\n\nFix EVERY issue before finishing: wire missing pages/routes in src/routes.tsx, replace any leading-slash `<a href=\"/...\">` with `<Link to=\"...\">` or `#section` scroll, replace any dead `href=\"#\"` / empty `onClick={() => {}}` handlers with real working behavior, and ensure all builds compile cleanly.",
							verifyResult.Content,
						)
						messages = append(messages, models.Message{
							Role:    "user",
							Content: fixPrompt,
						})

						streamer.SendMessage("I found some integration issues. Let me fix those...")
						streamer.SendStatus("fixing", "Fixing integration issues...")
						continue
					}
				}
			}

			// Mandatory build verification INSIDE the loop before completing!
			// Even if the AI forgot to call verifyBuild (or stopped after editing code),
			// compile now so any syntax/import error is automatically fed back to the AI to fix
			// and dist/index.html is guaranteed ready for live preview before SendComplete fires.
			if hasCodeChanges && !executor.IsDistUpToDate(r.executor.GetWorkspacePath()) {
				streamer.SendAction("Verifying", "build", "", "reading")
				streamer.SendStatus("verifying", "Verifying build...")

				verifyRes, vErr := r.executor.Execute(ctx, "verifyBuild", nil)
				buildOK := vErr == nil && verifyRes != nil && verifyRes.Success
				if buildOK {
					session.SetBuildResult(true, "Build succeeded")
					streamer.SendAction("Finalizing", "preview & summary", "", "creating")
				} else {
					errText := "Build failed"
					if vErr != nil {
						errText = vErr.Error()
					} else if verifyRes != nil && strings.TrimSpace(verifyRes.Content) != "" {
						errText = verifyRes.Content
					}
					session.SetBuildResult(false, errText)

					if buildFixAttempts < maxBuildFixes && errText != lastBuildError {
						lastBuildError = errText
						buildFixAttempts++
						r.logger.WithFields(logrus.Fields{
							"session_id": session.ID,
							"attempt":    buildFixAttempts,
							"error":      logging.TruncateForLog(errText, 300),
						}).Warn("Pre-completion verifyBuild failed, re-entering loop so AI can fix errors")

						// Keep conversation valid by appending assistant message if non-empty before user fix prompt
						if strings.TrimSpace(response.Content) != "" {
							messages = append(messages, models.Message{
								Role:    "assistant",
								Content: response.Content,
							})
						}
						messages = append(messages, models.Message{
							Role: "user",
							Content: fmt.Sprintf(
								"⚠️ BUILD VERIFICATION FAILED — live preview cannot load until this error is fixed:\n\n%s\n\nImmediately read/fix the broken file using editFile or createFile (check missing imports, JSX syntax, or invalid component/icon names), then call verifyBuild. NEVER ask the user to run verifyBuild.",
								errText,
							),
						})
						streamer.SendStatus("fixing", "Fixing build error...")
						continue
					}
				}
			}

			// No integration or build issues remaining, we're done
			streamer.SendStatus("completing", "Almost there...")
			break
		}

		// Build batch of tool call requests and stream events
		batchRequests := make([]executor.ToolCallRequest, 0, len(response.ToolCalls))

		// Pre-computed results for session-level tools (not handled by executor)
		sessionToolResults := make(map[string]executor.ToolResult)

		for _, toolCall := range response.ToolCalls {
			// Handle getProjectCapabilities specially (needs session access)
			if toolCall.Name == "getProjectCapabilities" {
				caps := session.GetProjectCapabilities()
				var content string
				if caps == nil {
					content = `{"firebase":{"enabled":false,"collection_prefix":""},"storage":{"enabled":false,"max_file_size_mb":0},"message":"No project capabilities configured."}`
				} else {
					content = formatProjectCapabilities(caps)
				}

				// Log session-level tool execution
				r.logger.WithFields(logrus.Fields{
					"session_id":  session.ID,
					"tool":        toolCall.Name,
					"caps_is_nil": caps == nil,
					"content":     content,
				}).Debug("Session-level tool: getProjectCapabilities")

				sessionToolResults[toolCall.ID] = executor.ToolResult{
					ToolCallID: toolCall.ID,
					Success:    true,
					Content:    content,
				}
				// Still stream the tool call event
				streamer.SendToolCall(toolCall.ID, toolCall.Name, toolCall.Arguments)
				action, target, category := getActionDescription(toolCall.Name, toolCall.Arguments)
				streamer.SendAction(action, target, "", category)
				// Stream the result immediately
				streamer.SendToolResult(toolCall.ID, toolCall.Name, true, content, 0, iterationsRun)
				continue
			}

			// Handle getFirestoreCollections specially (needs session + Laravel API)
			if toolCall.Name == "getFirestoreCollections" {
				content := r.handleGetFirestoreCollections(ctx, session)

				r.logger.WithFields(logrus.Fields{
					"session_id": session.ID,
					"tool":       toolCall.Name,
				}).Debug("Session-level tool: getFirestoreCollections")

				sessionToolResults[toolCall.ID] = executor.ToolResult{
					ToolCallID: toolCall.ID,
					Success:    true,
					Content:    content,
				}
				streamer.SendToolCall(toolCall.ID, toolCall.Name, toolCall.Arguments)
				action, target, category := getActionDescription(toolCall.Name, toolCall.Arguments)
				streamer.SendAction(action, target, "", category)
				streamer.SendToolResult(toolCall.ID, toolCall.Name, true, content, 0, iterationsRun)
				continue
			}

			// Stream tool call event
			streamer.SendToolCall(toolCall.ID, toolCall.Name, toolCall.Arguments)

			// Special handling for createPlan - send structured plan event
			if toolCall.Name == "createPlan" {
				summary, _ := toolCall.Arguments["summary"].(string)
				stepsArray, _ := toolCall.Arguments["steps"].([]interface{})

				var steps []models.PlanStep
				for _, stepInterface := range stepsArray {
					if step, ok := stepInterface.(map[string]interface{}); ok {
						steps = append(steps, models.PlanStep{
							File:        step["file"].(string),
							Action:      step["action"].(string),
							Description: step["description"].(string),
						})
					}
				}

				var deps, risks []string
				if depsRaw, ok := toolCall.Arguments["dependencies"].([]interface{}); ok {
					for _, dep := range depsRaw {
						if s, ok := dep.(string); ok {
							deps = append(deps, s)
						}
					}
				}
				if risksRaw, ok := toolCall.Arguments["risks"].([]interface{}); ok {
					for _, risk := range risksRaw {
						if r, ok := risk.(string); ok {
							risks = append(risks, r)
						}
					}
				}

				streamer.SendPlan(summary, steps, deps, risks)
			}

			// Send human-friendly action event
			action, target, category := getActionDescription(toolCall.Name, toolCall.Arguments)
			streamer.SendAction(action, target, "", category)

			// Log tool arguments
			r.logger.WithFields(logrus.Fields{
				"session_id": session.ID,
				"tool":       toolCall.Name,
				"args":       logging.FormatArgsForLog(toolCall.Arguments, 200),
			}).Debug("Tool arguments")

			// Add to batch
			batchRequests = append(batchRequests, executor.ToolCallRequest{
				ID:        toolCall.ID,
				Name:      toolCall.Name,
				Arguments: toolCall.Arguments,
			})
		}

		// Log batch execution start
		readOnlyCount := 0
		writeCount := 0
		for _, req := range batchRequests {
			if executor.IsReadOnly(req.Name) {
				readOnlyCount++
			} else {
				writeCount++
			}
		}
		r.logger.WithFields(logrus.Fields{
			"session_id":      session.ID,
			"total_tools":     len(batchRequests),
			"read_only_tools": readOnlyCount,
			"write_tools":     writeCount,
			"parallel":        features.ParallelTools,
		}).Debug("Executing tool batch")

		// Execute tools (batch/parallel or sequential based on feature flag)
		var toolResults []executor.ToolResult
		if features.ParallelTools {
			// Parallel execution for read-only tools, sequential for write tools
			toolResults = r.executor.ExecuteBatch(ctx, batchRequests)
		} else {
			// Sequential execution for all tools
			toolResults = make([]executor.ToolResult, len(batchRequests))
			for i, req := range batchRequests {
				result, err := r.executor.Execute(ctx, req.Name, req.Arguments)
				if err != nil {
					toolResults[i] = executor.ToolResult{
						ToolCallID: req.ID,
						Success:    false,
						Content:    fmt.Sprintf("Error: %s", err.Error()),
					}
				} else {
					result.ToolCallID = req.ID
					toolResults[i] = *result
				}
			}
		}

		// Stream results in original order (align with batchRequests, NOT response.ToolCalls, because session-level tools were handled separately)
		for i, result := range toolResults {
			toolName := batchRequests[i].Name

			// Log tool result
			r.logger.WithFields(logrus.Fields{
				"session_id": session.ID,
				"tool":       toolName,
				"success":    result.Success,
			}).Debug("Tool completed")

			// Stream tool result
			streamer.SendToolResult(
				result.ToolCallID,
				toolName,
				result.Success,
				truncateOutput(result.Content, 1500),
				result.DurationMs,
				iterationsRun,
			)

			// Track build results from verifyBuild calls
			if toolName == "verifyBuild" {
				success := result.Success
				var message string
				if !success {
					message = result.Content
				} else {
					message = "Build succeeded"
					// Advance UI status immediately so frontend never appears stuck on "Verifying build" while AI writes final summary
					streamer.SendAction("Finalizing", "preview & summary", "", "creating")
				}
				session.SetBuildResult(success, message)
			}

			// If useTemplate succeeded on a previously empty workspace, prune template tools immediately
			if toolName == "useTemplate" && result.Success && !toolFilterOpts.WorkspaceHasFiles {
				toolFilterOpts.WorkspaceHasFiles = true
				tools = GetToolsForSession(toolFilterOpts)
			}
		}

		// Circuit breaker: detect repeated failures and prevent infinite loops
		var shouldExit bool
		for i, result := range toolResults {
			toolName := batchRequests[i].Name
			filePath, _ := batchRequests[i].Arguments["path"].(string)
			if !result.Success {
				action := circuitBreaker.RecordFileFailure(toolName, filePath, result.Content)
				switch action {
				case ActionGracefulExit:
					// Too many failures - stop the session gracefully
					r.logger.WithFields(logrus.Fields{
						"session_id": session.ID,
						"tool":       toolName,
						"stats":      circuitBreaker.GetStats(),
					}).Warn("Circuit breaker: forcing graceful exit")
					shouldExit = true

				case ActionInjectGuidance:
					// Inject recovery guidance
					circuitMsg := fmt.Sprintf(
						"⚠️ SYSTEM: %s has failed repeatedly.\n\n%s",
						toolName, getToolRecoveryGuidance(toolName),
					)
					messages = append(messages, models.Message{
						Role:    "system",
						Content: circuitMsg,
					})
					r.logger.WithFields(logrus.Fields{
						"session_id": session.ID,
						"tool":       toolName,
						"stats":      circuitBreaker.GetStats(),
					}).Warn("Circuit breaker: injected recovery guidance")
				}
			} else {
				circuitBreaker.RecordFileSuccess(toolName, filePath)
			}
		}

		// Handle graceful exit due to circuit breaker
		if shouldExit {
			hasCodeChanges := r.executor != nil && r.executor.HasCodeChanges()
			session.SetFilesChanged(hasCodeChanges)
			session.SetStatus(models.StatusCompleted)
			streamer.SendStatus("circuit_breaker", "Stopping session cleanly")

			exitMsg := "I encountered repeated errors and couldn't complete the task. Please try again with a simpler request or review the changes made so far."
			if hasCodeChanges {
				// Ensure build is compiled into dist/ so preview works immediately
				if r.executor != nil && !executor.IsDistUpToDate(session.GetWorkspacePath()) {
					bResult, bErr := r.executor.Execute(ctx, "verifyBuild", nil)
					if bErr == nil && bResult != nil {
						session.SetBuildResult(bResult.Success, bResult.Content)
					}
				}
				exitMsg = r.synthesizeBuildSummary(session.GetWorkspacePath(), session, "")
			}
			streamer.SendMessage(exitMsg)

			completeData := models.CompleteData{
				Iterations:       iterationsRun,
				TokensUsed:       session.TokensUsed,
				FilesChanged:     hasCodeChanges,
				Message:          exitMsg,
				BuildStatus:      session.GetBuildStatus(),
				BuildMessage:     session.GetBuildMessage(),
				BuildRequired:    session.IsBuildRequired(),
				PromptTokens:     session.PromptTokens,
				CompletionTokens: session.CompletionTokens,
				Model:            r.aiConfig.Model,
			}
			streamer.SendComplete(completeData)
			return nil
		}

		// Add assistant message with tool calls
		messages = append(messages, models.Message{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		// Add tool results as separate messages (truncated to save context window)

		// First add session-level tool results (e.g., getProjectCapabilities)
		for toolCallID, result := range sessionToolResults {
			messages = append(messages, models.Message{
				Role:       "tool",
				Content:    result.Content,
				ToolCallID: toolCallID,
			})
		}

		// Then add executor tool results
		for i, result := range toolResults {
			toolName := batchRequests[i].Name
			maxLen := maxResultForTool(toolName)
			content := result.Content
			if len(content) > maxLen {
				half := maxLen / 2
				content = content[:half] +
					fmt.Sprintf("\n\n... (%d chars truncated) ...\n\n", len(result.Content)-maxLen) +
					content[len(content)-half+50:]
			}
			messages = append(messages, models.Message{
				Role:       "tool",
				Content:    content,
				ToolCallID: result.ToolCallID,
			})
		}

		// Steering guard: prevent wasted read-only scanning turns or reading src/components/ui/* boilerplate
		allReadOnly := len(response.ToolCalls) > 0
		readUIPrimitive := false
		for _, tc := range response.ToolCalls {
			if !executor.IsReadOnly(tc.Name) {
				allReadOnly = false
			}
			if isReadingUIPrimitive(tc) {
				readUIPrimitive = true
			}
		}
		if allReadOnly {
			consecutiveReadOnlyTurns++
		} else {
			consecutiveReadOnlyTurns = 0
		}
		if readUIPrimitive || consecutiveReadOnlyTurns >= 2 {
			steerMsg := "STOP scanning more files — all project structure, component exports, packages, routes, and stock images are already in your Workspace Reference. Write the website files NOW using writeFiles (or parallel createFile/editFile calls), then call verifyBuild."
			if readUIPrimitive {
				steerMsg = "STOP reading files under src/components/ui/ — every shadcn/ui export is already listed in your Workspace Reference. Write the website files NOW using writeFiles (or parallel createFile/editFile calls), then call verifyBuild."
			}
			messages = append(messages, models.Message{
				Role:    "user",
				Content: steerMsg,
			})
			consecutiveReadOnlyTurns = 0
			r.logger.WithFields(logrus.Fields{
				"session_id":        session.ID,
				"iteration":         iterationsRun,
				"read_ui_primitive": readUIPrimitive,
			}).Debug("Injected anti-scan steering nudge")
		}

		// Auto-compaction: check if context is getting too large (if enabled)
		contextTokens := summarizer.EstimateConversationTokens(messages)
		session.SetContextTokens(contextTokens)

		if features.AutoCompaction {
			contextWindow := r.aiConfig.ContextWindow
			if contextWindow == 0 {
				contextWindow = 128000 // Default
			}
			threshold := r.aiConfig.CompactionThreshold
			if threshold == 0 {
				threshold = 0.7 // Default: 70%
			}

			thresholdTokens := int(float64(contextWindow) * threshold)
			if contextTokens > thresholdTokens {
				r.logger.WithFields(logrus.Fields{
					"session_id":     session.ID,
					"context_tokens": contextTokens,
					"threshold":      thresholdTokens,
				}).Debug("Context threshold reached, triggering compaction")

				streamer.SendStatus("compacting", "Compressing history...")

				// Convert messages to turns for summarization
				turns := make([]summarizer.Turn, 0, len(messages))
				for _, msg := range messages {
					if msg.Role == "system" {
						continue // Skip system messages
					}
					turns = append(turns, summarizer.Turn{
						Role:    msg.Role,
						Content: msg.Content,
					})
				}

				// Process with summarization
				state, err := r.summarizer.Process(ctx, turns)
				if err == nil && state.Summary != "" {
					// Track auto-compaction tokens for credit depletion
					if state.TotalTokens > 0 {
						session.UpdateTokens(state.PromptTokens, state.CompletionTokens, state.TotalTokens)
						promptTok, compTok, totalTok, ctxTok := session.GetTokenStats()
						streamer.SendTokenUsage(promptTok, compTok, totalTok, ctxTok)
						r.logger.WithFields(logrus.Fields{
							"session_id":            session.ID,
							"summarizer_prompt":     state.PromptTokens,
							"summarizer_completion": state.CompletionTokens,
							"summarizer_total":      state.TotalTokens,
						}).Debug("Added auto-compaction tokens to session")
					}

					// Build compacted history for Laravel
					var compactedHistory []models.HistoryMessage
					compactedHistory = append(compactedHistory, models.HistoryMessage{
						Role:    "assistant",
						Content: "[Previous conversation summary]\n" + state.Summary,
					})

					// Rebuild messages with summary
					// Re-load template prompts & metadata in case the AI selected a template during the run
					compactWorkspace := r.executor.GetWorkspacePath()
					compactTemplatePrompts := r.loadTemplatePrompts(compactWorkspace)
					compactTemplateMetadata := r.loadTemplateMetadata(compactWorkspace)
					compactCfg := PromptConfig{
						ProjectName:      "Project",
						WorkspacePath:    compactWorkspace,
						Goal:             goal,
						TemplatePrompts:  compactTemplatePrompts,
						TemplateMetadata: compactTemplateMetadata,
						Capabilities:     session.GetProjectCapabilities(),
						ThemePreset:      session.GetThemePreset(),
						Compact:          promptCfg.Compact,
						TokenBudget:      tokenBudget,
					}
					if cp := session.GetCustomPrompts(); cp != nil {
						compactCfg.CustomPrompt = cp.SystemPrompt
						compactCfg.CustomCompactPrompt = cp.CompactPrompt
					}
					systemPrompt, promptErr := BuildSystemPrompt(compactCfg)
					if promptErr != nil {
						r.logger.WithField("error", promptErr.Error()).Warn("Failed to build compact prompt during compaction, using current prompt")
						break
					}
					switch r.aiConfig.ReasoningEffort {
					case "low":
						systemPrompt += "\n\n[EFFORT LEVEL: LOW — FAST MODE]\nExecute the request directly and concisely in as few tool calls as possible. Skip lengthy exploratory planning and focus on immediate, high-speed delivery."
					case "high":
						systemPrompt += "\n\n[EFFORT LEVEL: HIGH — DEEP ARCHITECTURAL MODE]\nApply deep reasoning, comprehensive UI/UX polish, rich micro-interactions, responsive layouts, and thorough multi-component implementation."
					}
					newMessages := []models.Message{
						{Role: "system", Content: systemPrompt},
						{Role: "system", Content: "CONVERSATION HISTORY:\n" + state.Summary},
					}

					// Add recent turns back
					for _, t := range state.RecentTurns {
						compactedHistory = append(compactedHistory, models.HistoryMessage{
							Role:    t.Role,
							Content: t.Content,
						})
						newMessages = append(newMessages, models.Message{
							Role:    t.Role,
							Content: t.Content,
						})
					}

					messages = newMessages
					newContextTokens := summarizer.EstimateConversationTokens(messages)
					session.SetContextTokens(newContextTokens)

					r.logger.WithFields(logrus.Fields{
						"session_id":    session.ID,
						"old_tokens":    contextTokens,
						"new_tokens":    newContextTokens,
						"reduction_pct": fmt.Sprintf("%.1f%%", float64(contextTokens-newContextTokens)/float64(contextTokens)*100),
					}).Debug("Context compacted")

					// Send summarization complete event with compacted history
					turnsCompacted := len(turns) - len(state.RecentTurns)
					reductionPct := float64(0)
					if contextTokens > 0 {
						reductionPct = float64(contextTokens-newContextTokens) / float64(contextTokens) * 100
					}
					streamer.SendSummarizationComplete(models.SummarizationEventData{
						OldTokens:        contextTokens,
						NewTokens:        newContextTokens,
						ReductionPercent: reductionPct,
						TurnsCompacted:   turnsCompacted,
						TurnsKept:        len(state.RecentTurns),
						Message:          fmt.Sprintf("Auto-compaction: reduced context from %d to %d tokens (%.1f%% reduction)", contextTokens, newContextTokens, reductionPct),
						CompactedHistory: compactedHistory,
					})
				} else {
					// Mid-turn compaction failed
					errMsg := "empty summary returned"
					if err != nil {
						errMsg = err.Error()
					}
					r.logger.WithFields(logrus.Fields{
						"session_id":     session.ID,
						"error":          errMsg,
						"context_tokens": contextTokens,
						"turns":          len(turns),
					}).Warn("Mid-turn compaction failed, truncating to recent messages")

					// Fallback: preserve all system messages + last N non-system messages
					if len(messages) > 20 {
						var systemMsgs []models.Message
						var otherMsgs []models.Message
						for _, msg := range messages {
							if msg.Role == "system" {
								systemMsgs = append(systemMsgs, msg)
							} else {
								otherMsgs = append(otherMsgs, msg)
							}
						}
						keepCount := 20 - len(systemMsgs)
						if keepCount < 4 {
							keepCount = 4
						}
						if len(otherMsgs) > keepCount {
							otherMsgs = otherMsgs[len(otherMsgs)-keepCount:]
						}
						messages = append(systemMsgs, otherMsgs...)
					}
					newContextTokens := summarizer.EstimateConversationTokens(messages)
					session.SetContextTokens(newContextTokens)
					streamer.SendStatus("compaction_warning",
						fmt.Sprintf("Context compaction failed (%s), using truncated history", errMsg))
				}
			}
		}
	}

	// Track whether the AI actually modified website code AND customized '/' beyond the starter template
	hasCodeChanges := r.executor != nil && r.executor.HasCodeChanges()
	isCustomizedApp := executor.WorkspaceHasCustomizedApp(session.GetWorkspacePath())
	effectiveCodeChanges := hasCodeChanges && isCustomizedApp
	hasChanges := effectiveCodeChanges || (r.executor != nil && r.executor.HasFileChanges() && isCustomizedApp)
	session.SetFilesChanged(effectiveCodeChanges)

	// Only compile & show "Website Build Complete!" when actual customized website code exists on '/'.
	// If the agent asked a legitimate clarifying question (e.g. Firebase vs Supabase vs localStorage),
	// complete cleanly with BuildStatus="skipped" and preserve its message intact.
	// If the workspace still renders the untouched starter template without a valid chat/clarification reply,
	// mark the run failed rather than pretending or deploying a starter template preview.
	if effectiveCodeChanges {
		bResult, bErr := r.executor.Execute(ctx, "verifyBuild", nil)
		if bErr == nil && bResult != nil {
			session.SetBuildResult(bResult.Success, bResult.Content)
		}
		lastAIContent = r.synthesizeBuildSummary(session.GetWorkspacePath(), session, lastAIContent)
		streamer.SendMessage(lastAIContent)
	} else if clarifyingReplyAccepted && strings.TrimSpace(lastAIContent) != "" {
		session.SetStatus(models.StatusCompleted)
		session.SetBuildResult(true, "Clarification requested")
		lastAIContent = strings.TrimSpace(lastAIContent)
		streamer.SendMessage(lastAIContent)
		streamer.SendComplete(models.CompleteData{
			Iterations:       session.Iterations,
			TokensUsed:       session.TokensUsed,
			FilesChanged:     false,
			BuildStatus:      "skipped",
			BuildMessage:     "Clarification requested",
			BuildRequired:    false,
			Message:          lastAIContent,
			PromptTokens:     session.PromptTokens,
			CompletionTokens: session.CompletionTokens,
			Model:            r.aiConfig.Model,
		})
		logging.LogSessionEnd(r.logger, session.ID, session.Iterations, session.TokensUsed, false, string(models.StatusCompleted))
		return nil
	} else if !isCustomizedApp {
		session.SetStatus(models.StatusFailed)
		failMsg := "The AI did not generate a customized home page (the starter template was not updated). Please resend your prompt or try a different AI model."
		if hasCodeChanges {
			failMsg = "The AI modified helper or styling files but did not update the home page ('/'). Please resend your instructions so the full website is built."
		} else if strings.TrimSpace(lastAIContent) != "" {
			failMsg = strings.TrimSpace(lastAIContent) + "\n\n*(Note: No website pages were built yet. Please resend your build instructions to generate the website.)*"
		}
		session.Error = failMsg
		lastAIContent = failMsg
		streamer.SendStatus("failed", "AI did not customize the website home page")
		streamer.SendMessage(lastAIContent)
		streamer.SendError("AI finished without customizing website home page")
	} else if lastAIContent == "" {
		lastAIContent = "I've reviewed your request. Please specify which pages or components you'd like to update."
		streamer.SendMessage(lastAIContent)
	} else {
		streamer.SendMessage(lastAIContent)
	}

	// Brief delay to ensure async message event is dispatched before sync complete
	time.Sleep(100 * time.Millisecond)

	// Send completion (synchronous - blocks until Pusher confirms delivery)
	streamer.SendComplete(models.CompleteData{
		Iterations:       session.Iterations,
		TokensUsed:       session.TokensUsed,
		FilesChanged:     effectiveCodeChanges,
		Message:          lastAIContent,
		BuildStatus:      session.GetBuildStatus(),
		BuildMessage:     session.GetBuildMessage(),
		BuildRequired:    session.IsBuildRequired(),
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
		Model:            r.aiConfig.Model,
	})

	// Log session complete with visual separator
	status := string(session.GetStatus())
	logging.LogSessionEnd(r.logger, session.ID, session.Iterations, session.TokensUsed, hasChanges && effectiveCodeChanges, status)

	return nil
}

// synthesizeBuildSummary produces a comprehensive, elegant markdown summary of what was built
func (r *Runner) synthesizeBuildSummary(workspacePath string, session *Session, existingContent string) string {
	// If the AI already provided a rich, descriptive response (>200 chars with markdown structure or bullet points)
	// AND does not leak internal tool instructions like "calling verifyBuild", keep it
	trimmed := strings.TrimSpace(existingContent)
	lowerTrimmed := strings.ToLower(trimmed)
	leaksInternalTools := strings.Contains(lowerTrimmed, "verifybuild") ||
		strings.Contains(lowerTrimmed, "verifyintegration") ||
		strings.Contains(lowerTrimmed, "createfile") ||
		strings.Contains(lowerTrimmed, "writefiles") ||
		strings.Contains(lowerTrimmed, "editfile")
	if !leaksInternalTools && len(trimmed) > 200 && (strings.Contains(trimmed, "###") || strings.Contains(trimmed, "- ") || strings.Contains(trimmed, "* ")) {
		return trimmed
	}

	// Find all pages in src/pages/
	pages := scanPageFiles(workspacePath)
	var pagesList []string
	if len(pages) > 0 {
		for _, p := range pages {
			base := filepath.Base(p)
			pageName := strings.TrimSuffix(base, filepath.Ext(base))
			routePath := "/" + strings.ToLower(pageName)
			if strings.EqualFold(pageName, "Home") || strings.EqualFold(pageName, "Index") {
				routePath = "/"
			}
			pagesList = append(pagesList, fmt.Sprintf("- **%s** (`%s`): Responsive page layout with customized sections and components.", pageName, routePath))
		}
	} else {
		pagesList = append(pagesList, "- **Home** (`/`): Customized responsive landing page with full layout structure.")
	}

	// Detect key components and sections from Home.tsx and custom.css
	var features []string
	var stylingNotes []string
	homePath := filepath.Join(workspacePath, "src", "pages", "Home.tsx")
	if homeBytes, err := os.ReadFile(homePath); err == nil {
		homeStr := string(homeBytes)
		hasFooter := strings.Contains(strings.ToLower(homeStr), "<footer") || strings.Contains(homeStr, "Footer")
		if strings.Contains(homeStr, "Hero") || strings.Contains(homeStr, "hero") {
			features = append(features, "Hero section with high-impact headline and primary call-to-action")
		}
		if strings.Contains(homeStr, "Feature") || strings.Contains(homeStr, "feature") || strings.Contains(homeStr, "grid") {
			features = append(features, "Feature cards with icon highlights and responsive multi-column layout")
		}
		if strings.Contains(homeStr, "Pricing") || strings.Contains(homeStr, "pricing") {
			features = append(features, "Interactive pricing tiers with feature breakdown")
		}
		if strings.Contains(homeStr, "Testimonial") || strings.Contains(homeStr, "testimonial") || strings.Contains(homeStr, "review") {
			features = append(features, "Customer testimonials and trust badges")
		}
		if strings.Contains(homeStr, "Contact") || strings.Contains(homeStr, "form") || strings.Contains(homeStr, "input") {
			features = append(features, "Interactive forms and controls with live feedback")
		}
		if strings.Contains(homeStr, "FAQ") || strings.Contains(homeStr, "accordion") {
			features = append(features, "Accordion FAQ section for common questions")
		}
		if hasFooter {
			stylingNotes = append(stylingNotes, "Sticky header navigation with logo, nav links, and footer")
		} else {
			stylingNotes = append(stylingNotes, "Responsive navigation bar and interactive workspace layout")
		}
	} else {
		stylingNotes = append(stylingNotes, "Responsive navigation and modular page layout")
	}
	if len(features) == 0 {
		features = append(features, "Responsive layout tailored to your goals", "Interactive components with smooth hover and active states")
	}

	// Check custom.css for styling highlights
	cssPath := filepath.Join(workspacePath, "src", "custom.css")
	if _, err := os.Stat(cssPath); err == nil {
		stylingNotes = append(stylingNotes, "Cohesive color palette and typography configured in `src/custom.css`")
	} else {
		stylingNotes = append(stylingNotes, "Clean Tailwind styling with harmonious contrast and fluid spacing")
	}
	stylingNotes = append(stylingNotes, "Mobile-first responsive design adapted for desktop, tablet, and mobile screens")

	var b strings.Builder
	b.WriteString("### 🎉 Website Build Complete!\n\n")
	b.WriteString("I've built and compiled your website based on your instructions. Here is a summary of what's ready:\n\n")

	b.WriteString("#### 📄 Pages & Routes\n")
	for _, p := range pagesList {
		b.WriteString(p + "\n")
	}
	b.WriteString("\n")

	b.WriteString("#### ✨ Key Sections & Components\n")
	for _, f := range features {
		b.WriteString(fmt.Sprintf("- **%s**\n", f))
	}
	b.WriteString("\n")

	b.WriteString("#### 🎨 Design & Styling\n")
	for _, s := range stylingNotes {
		b.WriteString(fmt.Sprintf("- %s\n", s))
	}
	b.WriteString("\n")

	b.WriteString("#### 🚀 Live Preview Ready\n")
	b.WriteString("The production bundle has been compiled cleanly. You can test and interact with your site in the live preview panel on the right!\n\n")
	b.WriteString("*Tip: Let me know if you'd like to adjust any text, tweak colors, or add more pages or sections.*")

	return b.String()
}

// scanPageFiles returns relative paths of all .tsx page files in src/pages/.
// Excludes NotFound.tsx since it doesn't need routing.
func scanPageFiles(workspacePath string) []string {
	pagesDir := filepath.Join(workspacePath, "src", "pages")
	entries, err := os.ReadDir(pagesDir)
	if err != nil {
		return nil
	}

	var pages []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".tsx") && name != "NotFound.tsx" {
			pages = append(pages, "src/pages/"+name)
		}
	}
	return pages
}

// maxResultForTool returns the per-tool context window truncation limit.
// Build tools need more context (error messages at end of long output).
// Read/analysis tools benefit from larger results. Other tools use a default.
func maxResultForTool(toolName string) int {
	switch toolName {
	case "readFiles":
		return 16000
	case "verifyBuild":
		return 12000
	case "readFile", "analyzeProject", "searchFiles":
		return 8000
	default:
		return 6000
	}
}

func truncateOutput(s string, maxLen int) string {
	// Strip HTML tags first to prevent HTML in logs/streamed output
	s = logging.StripHTMLTags(s)

	if len(s) <= maxLen {
		return s
	}
	// For longer outputs, show beginning + end (errors usually at end)
	if maxLen > 500 {
		headLen := 200
		tailLen := maxLen - headLen - 50
		return s[:headLen] + "\n\n...(truncated)...\n\n" + s[len(s)-tailLen:]
	}
	return s[:maxLen] + "..."
}

// getActionDescription returns a fun, human-friendly action description and category for icon
// loadSiteMemory reads memory.json from workspace root if it exists.
// Returns the formatted content or empty string if not present.
func loadSiteMemory(workspacePath string) string {
	data, err := executor.ReadJSONFile(filepath.Join(workspacePath, "memory.json"))
	if err != nil || len(data) == 0 {
		return ""
	}
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return ""
	}
	return string(content)
}

// loadDesignIntelligence reads design-intelligence.json from workspace root if it exists.
// Returns the formatted content or empty string if not present.
func loadDesignIntelligence(workspacePath string) string {
	data, err := executor.ReadJSONFile(filepath.Join(workspacePath, "design-intelligence.json"))
	if err != nil || len(data) == 0 {
		return ""
	}
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return ""
	}
	return string(content)
}

func getActionDescription(toolName string, args map[string]interface{}) (action, target, category string) {
	path, _ := args["path"].(string)

	switch toolName {
	case "createFile":
		return randomPhrase(createPhrases), path, "creating"
	case "writeFiles":
		if filesRaw, ok := args["files"].([]interface{}); ok && len(filesRaw) > 0 {
			return randomPhrase(createPhrases), fmt.Sprintf("%d files", len(filesRaw)), "creating"
		}
		return randomPhrase(createPhrases), "website files", "creating"
	case "editFile":
		return randomPhrase(editPhrases), path, "editing"
	case "readFile":
		return randomPhrase(readPhrases), path, "reading"
	case "readFiles":
		if pathsRaw, ok := args["paths"].([]interface{}); ok && len(pathsRaw) > 0 {
			return randomPhrase(readPhrases), fmt.Sprintf("%d files", len(pathsRaw)), "reading"
		}
		return randomPhrase(readPhrases), "project files", "reading"
	case "listFiles":
		return randomPhrase(explorePhrases), "project structure", "exploring"
	case "listComponents":
		return "Browsing", "available components", "exploring"
	case "getComponentUsage":
		component, _ := args["component"].(string)
		if component != "" {
			return "Learning about", component + " component", "reading"
		}
		return "Learning about", "component usage", "reading"
	case "verifyBuild":
		return "Verifying", "build", "reading"
	case "verifyIntegration":
		return "Checking", "page integration", "reading"
	case "createPlan":
		return "Planning", "your project", "planning"
	case "getProjectCapabilities":
		return "Checking", "available features", "reading"
	case "getFirestoreCollections":
		return "Checking", "existing data collections", "reading"
	case "writeDesignIntelligence":
		return "Saving", "design decisions", "creating"
	case "readDesignIntelligence":
		return "Recalling", "design decisions", "reading"
	case "updateSiteMemory":
		return "Saving", "business context", "creating"
	case "readSiteMemory":
		return "Recalling", "business context", "reading"
	case "generateAEO":
		return "Generating", "AI discovery files", "creating"
	case "listImages":
		return "Browsing", "stock images", "exploring"
	case "getImageUsage":
		return "Selecting", "stock image", "reading"
	default:
		if path != "" {
			return "Working on", path, ""
		}
		return "Working on", "your website", ""
	}
}

// workspaceHasSourceFiles returns true if the workspace already has initialized source files or template.json.
func workspaceHasSourceFiles(workspacePath string) bool {
	if _, err := os.Stat(filepath.Join(workspacePath, "template.json")); err == nil {
		return true
	}
	srcDir := filepath.Join(workspacePath, "src")
	if entries, err := os.ReadDir(srcDir); err == nil && len(entries) > 0 {
		return true
	}
	return false
}

// isReadingUIPrimitive returns true if a readFile/readFiles call targets any path under src/components/ui/.
func isReadingUIPrimitive(tc models.ToolCall) bool {
	checkPath := func(p string) bool {
		norm := strings.ToLower(filepath.ToSlash(strings.TrimSpace(p)))
		norm = strings.TrimPrefix(norm, "./")
		return strings.HasPrefix(norm, "src/components/ui/") || strings.Contains(norm, "/components/ui/")
	}

	switch tc.Name {
	case "readFile":
		if p, ok := tc.Arguments["path"].(string); ok {
			return checkPath(p)
		}
	case "readFiles":
		if pathsRaw, ok := tc.Arguments["paths"].([]interface{}); ok {
			for _, item := range pathsRaw {
				if p, ok := item.(string); ok && checkPath(p) {
					return true
				}
			}
		}
	}
	return false
}

// isExplicitBuildProceedGoal checks whether the user explicitly told the AI to build/proceed
// (e.g. answering a storage choice or saying "continue" / "use localStorage" / "use firebase").
func isExplicitBuildProceedGoal(goal string) bool {
	lower := strings.ToLower(strings.TrimSpace(goal))
	if lower == "continue" || lower == "proceed" || lower == "go ahead" || lower == "build it" || lower == "start" {
		return true
	}
	proceedSignals := []string{
		"use firebase", "with firebase", "firebase only",
		"use supabase", "with supabase", "supabase only",
		"use localstorage", "use local storage", "with localstorage", "with local storage",
		"localstorage", "local storage", "demo version", "client-side only", "frontend only",
		"just build", "go ahead and build", "build it now", "build without backend",
	}
	for _, sig := range proceedSignals {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// isCasualConversationalGoal checks if the user's message is a simple greeting, thanks, or praise.
func isCasualConversationalGoal(goal string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(goal))
	if idx := strings.Index(trimmed, "\n["); idx != -1 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	trimmed = strings.Trim(trimmed, "!.?, ")
	casual := map[string]bool{
		"hi": true, "hello": true, "hey": true, "good morning": true, "good afternoon": true,
		"good evening": true, "thanks": true, "thank you": true, "great": true, "awesome": true,
		"nice": true, "perfect": true, "looks good": true, "cool": true, "ok": true, "okay": true,
	}
	return casual[trimmed]
}

// isLegitimateClarifyingOrChatReply distinguishes intentional clarifying/storage questions
// or casual greetings from empty responses or hallucinated "I built your website" claims.
func isLegitimateClarifyingOrChatReply(goal, content string, hasPriorAssistantTurn bool) bool {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) < 15 {
		return false
	}
	lower := strings.ToLower(trimmed)

	// Reject replies leaking internal tool names or pretending the build is already complete
	disallowedPhrases := []string{
		"verifybuild", "verifyintegration", "writefiles", "createfile",
		"website build complete", "i have built", "i've built your", "live preview ready",
		"here is a summary of what", "🎉 what i built",
	}
	for _, p := range disallowedPhrases {
		if strings.Contains(lower, p) {
			return false
		}
	}

	if isCasualConversationalGoal(goal) {
		return true
	}

	// Never accept another no-code clarification if user already answered a prior question or explicitly said to proceed
	if hasPriorAssistantTurn || isExplicitBuildProceedGoal(goal) {
		return false
	}

	// Accept external-URL redirect explanation
	if strings.Contains(lower, "cannot visit") || strings.Contains(lower, "can't visit") || strings.Contains(lower, "unable to visit") {
		return true
	}

	// Accept genuine clarifying / backend-choice questions
	return strings.Contains(trimmed, "?")
}
