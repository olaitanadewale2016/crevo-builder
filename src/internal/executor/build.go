package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"crevo-builder/internal/models"
)

// BuildExecutor handles build verification
type BuildExecutor struct {
	workspacePath string
	toolConfig    models.ToolExecutionConfig
}

// NewBuildExecutor creates a new build executor
func NewBuildExecutor(workspacePath string, toolConfig models.ToolExecutionConfig) *BuildExecutor {
	return &BuildExecutor{
		workspacePath: workspacePath,
		toolConfig:    toolConfig,
	}
}

// VerifyBuild runs fast TypeScript type & syntax check and returns the result (10x faster)
func (e *BuildExecutor) VerifyBuild(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	// Use configurable timeout with 45s minimum
	timeout := time.Duration(e.toolConfig.Timeout) * time.Second
	if timeout < 45*time.Second {
		timeout = 45 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Check if node_modules exists, if not run npm install first
	nodeModulesPath := filepath.Join(e.workspacePath, "node_modules")
	if _, err := os.Stat(nodeModulesPath); os.IsNotExist(err) {
		installCmd := exec.CommandContext(ctx, "npm", "install", "--ignore-scripts")
		installCmd.Dir = e.workspacePath
		if output, err := installCmd.CombinedOutput(); err != nil {
			return &ToolResult{
				Success: false,
				Content: fmt.Sprintf("Failed to install dependencies:\n%s", string(output)),
			}, nil
		}
	}

	// 1. FAST PATH: Check with local tsc binary (typically completes in 1-2 seconds)
	tscBin := filepath.Join(nodeModulesPath, ".bin", "tsc")
	if _, err := os.Stat(tscBin); err == nil {
		tscCmd := exec.CommandContext(ctx, tscBin, "--noEmit")
		tscCmd.Dir = e.workspacePath
		output, err := tscCmd.CombinedOutput()
		outputStr := string(output)

		if err != nil {
			errorSummary := extractBuildErrors(outputStr)
			return &ToolResult{
				Success: false,
				Content: fmt.Sprintf("TypeScript validation failed:\n%s", errorSummary),
			}, nil
		}

		return &ToolResult{
			Success: true,
			Content: "Build successful! All TypeScript files and components compile without errors.",
		}, nil
	}

	// 2. Fallback: npx tsc --noEmit or npm run build if tsc bin not directly found
	cmd := exec.CommandContext(ctx, "npx", "tsc", "--noEmit")
	cmd.Dir = e.workspacePath

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// Fallback to npm run build if npx tsc failed with command not found
		if strings.Contains(strings.ToLower(outputStr), "not found") || strings.Contains(strings.ToLower(outputStr), "cannot find") {
			buildCmd := exec.CommandContext(ctx, "npm", "run", "build")
			buildCmd.Dir = e.workspacePath
			buildOutput, buildErr := buildCmd.CombinedOutput()
			buildStr := string(buildOutput)
			if buildErr != nil {
				return &ToolResult{
					Success: false,
					Content: fmt.Sprintf("Build failed:\n%s", extractBuildErrors(buildStr)),
				}, nil
			}
			return &ToolResult{
				Success: true,
				Content: "Build successful! The project compiles without errors.",
			}, nil
		}

		// Extract the most relevant error info
		errorSummary := extractBuildErrors(outputStr)
		return &ToolResult{
			Success: false,
			Content: fmt.Sprintf("Build failed:\n%s", errorSummary),
		}, nil
	}

	return &ToolResult{
		Success: true,
		Content: "Build successful! The project compiles without errors.",
	}, nil
}

// extractBuildErrors extracts relevant error messages from build output
func extractBuildErrors(output string) string {
	lines := strings.Split(output, "\n")
	var errors []string

	for _, line := range lines {
		lineLower := strings.ToLower(line)
		// Look for TypeScript/Vite error patterns
		if strings.Contains(lineLower, "error") ||
			strings.Contains(line, "TS") ||
			strings.Contains(line, "Cannot find") ||
			strings.Contains(line, "is not defined") ||
			strings.Contains(line, "has no exported member") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				errors = append(errors, trimmed)
			}
		}
	}

	if len(errors) == 0 {
		// Return last 30 lines if no specific errors found
		start := len(lines) - 30
		if start < 0 {
			start = 0
		}
		return strings.Join(lines[start:], "\n")
	}

	// Limit to first 15 errors to avoid overwhelming
	if len(errors) > 15 {
		errors = errors[:15]
		errors = append(errors, "... (more errors omitted)")
	}

	return strings.Join(errors, "\n")
}
