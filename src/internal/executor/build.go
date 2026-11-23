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

// isNodeModulesHealthy checks if node_modules contains essential packages (react, vite)
func isNodeModulesHealthy(nodeModulesPath string) bool {
	if _, err := os.Stat(filepath.Join(nodeModulesPath, "vite", "bin", "vite.js")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(nodeModulesPath, ".bin", "vite")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(nodeModulesPath, "react")); err == nil {
		return true
	}
	return false
}

// EnsureSharedNodeModules copies or symlinks the shared node_modules cache for instant build readiness
func EnsureSharedNodeModules(ctx context.Context, workspacePath string) error {
	if absWS, err := filepath.Abs(workspacePath); err == nil {
		workspacePath = absWS
	}
	nodeModulesPath := filepath.Join(workspacePath, "node_modules")

	// If node_modules is already healthy (either directory or symlink), return immediately (0ms)
	if isNodeModulesHealthy(nodeModulesPath) {
		return nil
	}

	nodeModulesMu.Lock()
	defer nodeModulesMu.Unlock()

	// Double check after lock
	if isNodeModulesHealthy(nodeModulesPath) {
		return nil
	}

	// Clean broken node_modules directory/symlink
	_ = os.RemoveAll(nodeModulesPath)

	// Locate shared cache across standard paths
	cacheCandidates := []string{
		filepath.Join(filepath.Dir(workspacePath), ".shared_node_modules"),
		"/app/storage/workspaces/.shared_node_modules",
		"storage/workspaces/.shared_node_modules",
	}

	var sharedCacheDir string
	for _, cand := range cacheCandidates {
		if isNodeModulesHealthy(cand) {
			sharedCacheDir = cand
			break
		}
	}

	if sharedCacheDir != "" {
		// 1. Try GNU cp -al hardlinks (~40ms, 0 extra disk space)
		_ = os.MkdirAll(nodeModulesPath, 0755)
		cpCmd := exec.CommandContext(ctx, "cp", "-al", sharedCacheDir+"/.", nodeModulesPath)
		if err := cpCmd.Run(); err == nil && isNodeModulesHealthy(nodeModulesPath) {
			_ = os.Chmod(filepath.Join(nodeModulesPath, ".bin", "vite"), 0755)
			return nil
		}

		// 2. Fallback to instant directory symlink (0ms, 0 disk space, works across filesystems)
		_ = os.RemoveAll(nodeModulesPath)
		if symErr := os.Symlink(sharedCacheDir, nodeModulesPath); symErr == nil {
			if isNodeModulesHealthy(nodeModulesPath) {
				return nil
			}
			_ = os.Remove(nodeModulesPath)
		}
	}

	// 3. Cold start fallback: install dependencies once in workspace (capped to 256MB heap)
	installCmd := exec.CommandContext(ctx, "npm", "install", "--include=dev", "--ignore-scripts", "--prefer-offline", "--no-audit", "--no-fund")
	installCmd.Dir = workspacePath
	installCmd.Env = append(os.Environ(), "NODE_OPTIONS=--max-old-space-size=256")
	if out, err := installCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm install failed: %v (%s)", err, string(out))
	}
	defer debug.FreeOSMemory()

	// Populate shared cache via hardlink copy while keeping workspace node_modules intact
	primarySharedDir := filepath.Join(filepath.Dir(workspacePath), ".shared_node_modules")
	if !isNodeModulesHealthy(primarySharedDir) {
		_ = os.RemoveAll(primarySharedDir)
		_ = os.MkdirAll(primarySharedDir, 0755)
		_ = exec.CommandContext(ctx, "cp", "-al", nodeModulesPath+"/.", primarySharedDir).Run()
	}
	return nil
}

// ResolveToolCommand prefers direct node invocation of local/shared .js, then absolute workspace local .bin, then global PATH binary, then npx fallback.
func ResolveToolCommand(workspacePath, tool string) (string, []string) {
	if tool == "vite" {
		candidates := []string{
			filepath.Join(workspacePath, "node_modules", "vite", "bin", "vite.js"),
			"/app/storage/workspaces/.shared_node_modules/vite/bin/vite.js",
			"storage/workspaces/.shared_node_modules/vite/bin/vite.js",
		}
		for _, cand := range candidates {
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return "node", []string{cand}
			}
		}
	}

	if tool == "esbuild" {
		candidates := []string{
			filepath.Join(workspacePath, "node_modules", "esbuild", "bin", "esbuild"),
			"/app/storage/workspaces/.shared_node_modules/esbuild/bin/esbuild",
			"storage/workspaces/.shared_node_modules/esbuild/bin/esbuild",
		}
		for _, cand := range candidates {
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return cand, nil
			}
		}
	}

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

	sharedBin := filepath.Join("/app/storage/workspaces/.shared_node_modules/.bin", tool)
	if info, err := os.Stat(sharedBin); err == nil && !info.IsDir() {
		return sharedBin, nil
	}

	return "npx", []string{tool}
}

// IsDistUpToDate returns true if dist/index.html exists and is newer than all source files in src/ and root index.html
func IsDistUpToDate(workspacePath string) bool {
	distIndex := filepath.Join(workspacePath, "dist", "index.html")
	distInfo, err := os.Stat(distIndex)
	if err != nil {
		return false
	}
	distTime := distInfo.ModTime()

	if idxInfo, err := os.Stat(filepath.Join(workspacePath, "index.html")); err == nil && idxInfo.ModTime().After(distTime) {
		return false
	}

	srcDir := filepath.Join(workspacePath, "src")
	upToDate := true
	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.ModTime().After(distTime) {
			upToDate = false
			return filepath.SkipAll
		}
		return nil
	})
	return upToDate
}

// VerifyBuild runs fast Vite build verification (skipping slow tsc -b) and ensures dist/index.html is generated
func (e *BuildExecutor) VerifyBuild(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	// Fast path: if dist/index.html is already fresh and newer than all source files, return instantly (0ms)
	if IsDistUpToDate(e.workspacePath) {
		return &ToolResult{
			Success: true,
			Content: "Build successful! All components compiled cleanly (cached dist).",
		}, nil
	}

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

	// Ensure node_modules exists via 40ms hardlink cache or shared symlink
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
	nodeBinPath := filepath.Join(e.workspacePath, "node_modules", ".bin")
	sharedNodeModules := "/app/storage/workspaces/.shared_node_modules"
	localNodeModules := filepath.Join(e.workspacePath, "node_modules")
	viteCmd.Env = append(os.Environ(),
		"NODE_ENV=production",
		"NODE_OPTIONS=--max-old-space-size=256",
		fmt.Sprintf("NODE_PATH=%s:%s:%s", localNodeModules, sharedNodeModules, os.Getenv("NODE_PATH")),
		fmt.Sprintf("PATH=%s:%s:%s", nodeBinPath, filepath.Join(sharedNodeModules, ".bin"), os.Getenv("PATH")),
	)
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
