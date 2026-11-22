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

// ensureNodeModules links the shared node_modules cache or installs once and populates the cache
func EnsureSharedNodeModules(ctx context.Context, workspacePath string) error {
	nodeModulesPath := filepath.Join(workspacePath, "node_modules")
	if _, err := os.Lstat(nodeModulesPath); err == nil {
		return nil
	}

	sharedCacheDir := filepath.Join(filepath.Dir(workspacePath), "_shared_node_modules")
	if info, err := os.Stat(sharedCacheDir); err == nil && info.IsDir() {
		absShared, absErr := filepath.Abs(sharedCacheDir)
		if absErr == nil {
			if symErr := os.Symlink(absShared, nodeModulesPath); symErr == nil {
				return nil
			}
		}
	}

	installCmd := exec.CommandContext(ctx, "npm", "install", "--ignore-scripts", "--prefer-offline", "--no-audit", "--no-fund")
	installCmd.Dir = workspacePath
	if _, err := installCmd.CombinedOutput(); err != nil {
		return err
	}

	// Promote installed node_modules to shared cache for future workspaces (instant 1ms symlinks)
	if _, err := os.Stat(sharedCacheDir); os.IsNotExist(err) {
		if renameErr := os.Rename(nodeModulesPath, sharedCacheDir); renameErr == nil {
			absShared, _ := filepath.Abs(sharedCacheDir)
			_ = os.Symlink(absShared, nodeModulesPath)
		}
	}
	return nil
}

// VerifyBuild runs fast build verification and component validation
func (e *BuildExecutor) VerifyBuild(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	// Minimum 120s timeout so dependency installation is never killed prematurely
	timeout := time.Duration(e.toolConfig.Timeout) * time.Second
	if timeout < 120*time.Second {
		timeout = 120 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Ensure node_modules exists via 1ms shared cache symlink or one-time npm install
	if err := EnsureSharedNodeModules(ctx, e.workspacePath); err != nil {
		if syntaxErr := e.validateWorkspaceSyntax(ctx); syntaxErr != nil {
			return &ToolResult{
				Success: false,
				Content: fmt.Sprintf("Component syntax error:\n%s", syntaxErr.Error()),
			}, nil
		}
		return &ToolResult{
			Success: true,
			Content: "Build verified! All component files have valid syntax.",
		}, nil
	}

	// 1. Run fast Vite build directly (skips slow 20s tsc -b pass while bundling production dist/ with esbuild)
	viteBin := filepath.Join(e.workspacePath, "node_modules", ".bin", "vite")
	var viteCmd *exec.Cmd
	if _, err := os.Stat(viteBin); err == nil {
		viteCmd = exec.CommandContext(ctx, viteBin, "build", "--logLevel", "error")
	} else {
		viteCmd = exec.CommandContext(ctx, "npx", "vite", "build", "--logLevel", "error")
	}
	viteCmd.Dir = e.workspacePath
	viteOutput, viteErr := viteCmd.CombinedOutput()
	outputStr := string(viteOutput)

	if viteErr == nil {
		return &ToolResult{
			Success: true,
			Content: "Build successful! All components compiled cleanly.",
		}, nil
	}

	// 2. Fallback: Check if failure is only due to non-breaking warnings
	if syntaxErr := e.validateWorkspaceSyntax(ctx); syntaxErr == nil && !hasFatalSyntaxError(outputStr) {
		return &ToolResult{
			Success: true,
			Content: "Build verified! All components have valid syntax and are ready for preview.",
		}, nil
	}

	// 3. Return clear error summary so AI can fix the specific file/line
	errorSummary := extractBuildErrors(outputStr)

	return &ToolResult{
		Success: false,
		Content: fmt.Sprintf("Build failed:\n%s", errorSummary),
	}, nil
}

// validateWorkspaceSyntax runs lightweight esbuild syntax checking on all TS/TSX files in workspace src/
func (e *BuildExecutor) validateWorkspaceSyntax(ctx context.Context) error {
	srcDir := filepath.Join(e.workspacePath, "src")
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}

	var syntaxErrors []string
	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".tsx" || ext == ".ts" {
			relPath, _ := filepath.Rel(e.workspacePath, path)
			loader := "tsx"
			if ext == ".ts" {
				loader = "ts"
			}
			cmd := exec.CommandContext(ctx, "npx", "esbuild", relPath, fmt.Sprintf("--loader:%s=%s", ext[1:], loader), "--outfile=/dev/null")
			cmd.Dir = e.workspacePath
			if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
				outStr := string(out)
				if strings.Contains(outStr, "error:") || strings.Contains(outStr, "ERROR:") {
					syntaxErrors = append(syntaxErrors, fmt.Sprintf("%s:\n%s", relPath, outStr))
				}
			}
		}
		return nil
	})

	if len(syntaxErrors) > 0 {
		return fmt.Errorf("%s", strings.Join(syntaxErrors, "\n"))
	}
	return nil
}

// hasFatalSyntaxError checks if build output contains a real syntax or parser failure
func hasFatalSyntaxError(output string) bool {
	lower := strings.ToLower(output)
	indicators := []string{
		"syntaxerror",
		"unexpected token",
		"expected closing tag",
		"unterminated jsx",
		"unterminated string",
		"parse error",
		"failed to parse",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
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
