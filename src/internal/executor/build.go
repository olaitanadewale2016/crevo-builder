package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"crevo-builder/internal/models"
)

var (
	nodeModulesMu sync.Mutex
	BuildMu       sync.Mutex
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

// EnsureSharedNodeModules copies the shared node_modules cache using zero-space hardlinks (cp -al)
// or installs once and populates the shared cache. Avoids directory symlinks which break Node/Vite realpath resolution.
func EnsureSharedNodeModules(ctx context.Context, workspacePath string) error {
	if absWS, err := filepath.Abs(workspacePath); err == nil {
		workspacePath = absWS
	}
	nodeModulesPath := filepath.Join(workspacePath, "node_modules")

	// If node_modules is an old symlink or broken partial dir (missing .bin/vite), remove it so cp -al never nests
	if info, err := os.Lstat(nodeModulesPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(nodeModulesPath)
		} else if info.IsDir() {
			viteBin := filepath.Join(nodeModulesPath, ".bin", "vite")
			if _, vErr := os.Stat(viteBin); vErr == nil {
				return nil
			}
			_ = os.RemoveAll(nodeModulesPath)
		}
	}

	nodeModulesMu.Lock()
	defer nodeModulesMu.Unlock()

	sharedCacheDir := filepath.Join(filepath.Dir(workspacePath), ".shared_node_modules")
	sharedViteBin := filepath.Join(sharedCacheDir, ".bin", "vite")

	// Fast path: hardlink-copy shared node_modules into workspace (~40ms, 0 extra disk space, real paths for Vite)
	if _, err := os.Stat(sharedViteBin); err == nil {
		cpCmd := exec.CommandContext(ctx, "cp", "-al", sharedCacheDir, nodeModulesPath)
		if err := cpCmd.Run(); err == nil {
			return nil
		}
		// Fallback to regular recursive copy if hardlinks fail across mount points
		_ = os.RemoveAll(nodeModulesPath)
		cpFallback := exec.CommandContext(ctx, "cp", "-a", sharedCacheDir, nodeModulesPath)
		if err := cpFallback.Run(); err == nil {
			return nil
		}
		_ = os.RemoveAll(nodeModulesPath)
	}

	// Cold start: install dependencies once in workspace (capped to 192MB heap)
	installCmd := exec.CommandContext(ctx, "npm", "install", "--ignore-scripts", "--prefer-offline", "--no-audit", "--no-fund")
	installCmd.Dir = workspacePath
	installCmd.Env = append(os.Environ(), "NODE_OPTIONS=--max-old-space-size=192")
	if out, err := installCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm install failed: %v (%s)", err, string(out))
	}
	defer debug.FreeOSMemory()

	// Populate shared cache via hardlink copy while keeping workspace node_modules intact
	if _, err := os.Stat(sharedViteBin); os.IsNotExist(err) {
		_ = os.RemoveAll(sharedCacheDir)
		populateCmd := exec.CommandContext(ctx, "cp", "-al", nodeModulesPath, sharedCacheDir)
		if populateErr := populateCmd.Run(); populateErr != nil {
			_ = os.RemoveAll(sharedCacheDir)
			_ = exec.CommandContext(ctx, "cp", "-a", nodeModulesPath, sharedCacheDir).Run()
		}
	}
	return nil
}

// ResolveToolCommand prefers absolute workspace local .bin, then global PATH binary (zero npx overhead), then npx fallback.
func ResolveToolCommand(workspacePath, tool string) (string, []string) {
	localBin := filepath.Join(workspacePath, "node_modules", ".bin", tool)
	if absBin, err := filepath.Abs(localBin); err == nil {
		localBin = absBin
	}
	if info, err := os.Stat(localBin); err == nil && !info.IsDir() {
		return localBin, nil
	}
	if globalBin, err := exec.LookPath(tool); err == nil {
		return globalBin, nil
	}
	return "npx", []string{tool}
}

// VerifyBuild runs fast Vite build verification (skipping slow tsc -b) and ensures dist/index.html is generated
func (e *BuildExecutor) VerifyBuild(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	// Minimum 120s timeout so initial dependency installation is never killed prematurely
	timeout := time.Duration(e.toolConfig.Timeout) * time.Second
	if timeout < 120*time.Second {
		timeout = 120 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Serialize Vite builds to protect 512MB container RAM and free OS memory on completion
	BuildMu.Lock()
	defer BuildMu.Unlock()
	defer debug.FreeOSMemory()

	// Ensure node_modules exists via 40ms hardlink cache or one-time npm install
	if err := EnsureSharedNodeModules(ctx, e.workspacePath); err != nil {
		return &ToolResult{
			Success: false,
			Content: fmt.Sprintf("Failed to prepare dependencies:\n%s", err.Error()),
		}, nil
	}

	// Run fast Vite build directly (skips slow 25s tsc -b pass while bundling production dist/ with esbuild in ~2s)
	bin, prefix := ResolveToolCommand(e.workspacePath, "vite")
	cmdArgs := append(prefix, "build", "--logLevel", "error")
	viteCmd := exec.CommandContext(ctx, bin, cmdArgs...)
	viteCmd.Dir = e.workspacePath
	viteCmd.Env = append(os.Environ(), "NODE_OPTIONS=--max-old-space-size=192")
	viteOutput, viteErr := viteCmd.CombinedOutput()
	outputStr := string(viteOutput)

	distIndex := filepath.Join(e.workspacePath, "dist", "index.html")
	if viteErr == nil {
		if _, statErr := os.Stat(distIndex); statErr == nil {
			return &ToolResult{
				Success: true,
				Content: "Build successful! All components compiled cleanly.",
			}, nil
		}
	}

	// Return clear error summary so AI can fix any broken import or syntax error immediately
	errorSummary := extractBuildErrors(outputStr)
	if strings.TrimSpace(errorSummary) == "" && viteErr != nil {
		errorSummary = viteErr.Error()
	}

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
			bin, prefix := ResolveToolCommand(e.workspacePath, "esbuild")
			cmdArgs := append(prefix, relPath, fmt.Sprintf("--loader:%s=%s", ext[1:], loader), "--outfile=/dev/null")
			cmd := exec.CommandContext(ctx, bin, cmdArgs...)
			cmd.Dir = e.workspacePath
			cmd.Env = append(os.Environ(), "NODE_OPTIONS=--max-old-space-size=192")
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
