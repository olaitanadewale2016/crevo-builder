package executor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	tailwindApplyOrVariantRe = regexp.MustCompile(`@(apply|variant)\b`)
	tailwindRootImportRe     = regexp.MustCompile(`@import\s+["']tailwindcss["']`)
	cssReferenceRe           = regexp.MustCompile(`@reference\s+["']([^"']+)["']\s*;?`)
	cssLocalImportRe         = regexp.MustCompile(`@import\s+["'](\.[^"']+\.css)["']\s*;?`)
)

// NormalizeCSSContent ensures a CSS file under src/ is compatible with @tailwindcss/vite v4.
// Files @import-ed by src/index.css must not have circular @reference "../index.css" directives;
// standalone CSS files imported from JS/TSX with @apply/@variant need @reference to src/index.css.
func NormalizeCSSContent(content string, fullPath string, workspacePath string) string {
	srcDir := filepath.Join(workspacePath, "src")
	indexCSSPath := filepath.Join(srcDir, "index.css")

	cleanFull := filepath.Clean(fullPath)
	cleanIndex := filepath.Clean(indexCSSPath)

	// Never modify @reference logic for files outside src/ or index.css itself
	if !strings.HasSuffix(strings.ToLower(cleanFull), ".css") || cleanFull == cleanIndex {
		return content
	}
	if !strings.HasPrefix(cleanFull, filepath.Clean(srcDir)+string(os.PathSeparator)) {
		return content
	}

	// If this file is already @import-ed by src/index.css, strip any circular @reference to index.css
	importedSet := collectImportedCSSFromIndex(indexCSSPath, srcDir)
	if importedSet[cleanFull] {
		cleaned := stripCircularIndexReference(content)
		return cleaned
	}

	// Standalone CSS file (e.g. imported directly in main.tsx): if it uses @apply/@variant
	// without @import "tailwindcss" or @reference, prepend @reference to src/index.css.
	if _, err := os.Stat(indexCSSPath); err == nil {
		if tailwindApplyOrVariantRe.MatchString(content) &&
			!tailwindRootImportRe.MatchString(content) &&
			!cssReferenceRe.MatchString(content) {
			relToIndex, relErr := filepath.Rel(filepath.Dir(cleanFull), cleanIndex)
			if relErr == nil {
				slashRel := filepath.ToSlash(relToIndex)
				if !strings.HasPrefix(slashRel, ".") {
					slashRel = "./" + slashRel
				}
				return "@reference \"" + slashRel + "\";\n" + content
			}
		}
	}

	return content
}

// NormalizeWorkspaceTailwindCSS heals all .css files under src/ before running vite build.
func NormalizeWorkspaceTailwindCSS(workspacePath string) {
	srcDir := filepath.Join(workspacePath, "src")
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return
	}

	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(strings.ToLower(info.Name()), ".css") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		orig := string(raw)
		normalized := NormalizeCSSContent(orig, path, workspacePath)
		if normalized != orig {
			_ = os.WriteFile(path, []byte(normalized), 0644)
		}
		return nil
	})
}

func collectImportedCSSFromIndex(indexCSSPath string, srcDir string) map[string]bool {
	imported := make(map[string]bool)
	data, err := os.ReadFile(indexCSSPath)
	if err != nil {
		return imported
	}

	matches := cssLocalImportRe.FindAllStringSubmatch(string(data), -1)
	indexDir := filepath.Dir(indexCSSPath)
	cleanSrc := filepath.Clean(srcDir)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		target := filepath.Clean(filepath.Join(indexDir, filepath.FromSlash(m[1])))
		if strings.HasPrefix(target, cleanSrc+string(os.PathSeparator)) {
			imported[target] = true
		}
	}
	return imported
}

func stripCircularIndexReference(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := cssReferenceRe.FindStringSubmatch(trimmed); len(m) >= 2 {
			refTarget := strings.ToLower(filepath.Base(filepath.FromSlash(m[1])))
			if refTarget == "index.css" && strings.HasPrefix(trimmed, "@reference") {
				changed = true
				continue
			}
		}
		out = append(out, line)
	}
	if !changed {
		return content
	}
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}
