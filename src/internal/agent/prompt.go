package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"crevo-builder/internal/models"
	"crevo-builder/internal/registry"
)

// PromptsDir is the directory containing prompt template files.
// Relative to the working directory by default. Tests may override this.
var PromptsDir = "prompts"

// PromptConfig holds configuration for building the system prompt
type PromptConfig struct {
	ProjectName         string
	WorkspacePath       string
	Goal                string // User goal for local domain stock image pre-selection
	TemplatePrompts     *models.TemplatePrompts
	TemplateMetadata    *models.TemplateMetadata
	Capabilities        *models.ProjectCapabilities
	ThemePreset         *models.ThemePreset
	Compact             bool   // When true, generates a shorter prompt for standard-tier models
	TokenBudget         int    // Max tokens for prompt (0 = no budget, include everything)
	CustomPrompt        string // Admin-editable full prompt (empty = use hardcoded)
	CustomCompactPrompt string // Admin-editable compact prompt (empty = use hardcoded)
}

// promptSection represents a prioritized section of the system prompt
type promptSection struct {
	Priority int    // 0 = always include, 1 = high, 2 = medium, 3 = low (drop first)
	Label    string // For logging which sections were dropped
	Content  string
}

// estimateTokens gives a rough token count (word-based heuristic ~1.3 tokens/word)
func estimateTokens(text string) int {
	words := len(strings.Fields(text))
	return int(float64(words) * 1.3)
}

// assemblePrioritized builds the prompt from sections, respecting the token budget.
// If budget is 0, all sections are included. Otherwise, sections are added by priority
// until the budget is reached. P0 sections are always included.
func assemblePrioritized(sections []promptSection, budget int) string {
	if budget <= 0 {
		// No budget — include everything in order
		var sb strings.Builder
		for _, s := range sections {
			sb.WriteString(s.Content)
		}
		return sb.String()
	}

	var sb strings.Builder
	used := 0

	// Always include P0
	for _, s := range sections {
		if s.Priority == 0 {
			sb.WriteString(s.Content)
			used += estimateTokens(s.Content)
		}
	}

	// Add higher priority sections first
	for priority := 1; priority <= 3; priority++ {
		for _, s := range sections {
			if s.Priority != priority {
				continue
			}
			cost := estimateTokens(s.Content)
			if used+cost <= budget {
				sb.WriteString(s.Content)
				used += cost
			}
		}
	}

	return sb.String()
}

// validSections defines allowed injection points in order
var validSections = []string{
	"after_role",
	"after_mandatory_steps",
	"after_patterns",
	"after_dynamic_features",
	"before_response_format",
	"footer",
}

// injectTemplatePrompts injects template prompts for a specific section,
// defensively filtering out legacy prompts that contradict system.md or instruct
// calling pruned/inlined scan tools.
func injectTemplatePrompts(sb *strings.Builder, prompts *models.TemplatePrompts, caps *models.ProjectCapabilities, section string) {
	if prompts == nil || len(prompts.Prompts) == 0 {
		return
	}

	filtered := filterAndSortPrompts(prompts.Prompts, section, caps)
	for _, p := range filtered {
		if isLegacyContradictoryTemplatePrompt(p) {
			continue
		}
		content := sanitizeLegacyPromptContent(p.Content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		fmt.Fprintf(sb, "\n## %s\n\n%s\n", p.Title, content)
	}
}

// isLegacyContradictoryTemplatePrompt returns true for legacy template.json prompt
// sections that instruct calling removed/inlined tools or outdated multi-turn scan steps.
func isLegacyContradictoryTemplatePrompt(p models.TemplatePrompt) bool {
	combined := strings.ToLower(p.Title + "\n" + p.Content)

	if strings.Contains(combined, "check project capabilities first") ||
		(strings.Contains(combined, "getprojectcapabilities") && (strings.Contains(combined, "immediately") || strings.Contains(combined, "first"))) {
		return true
	}
	if strings.Contains(combined, `readfile("template.json")`) || strings.Contains(combined, `readfile('template.json')`) {
		return true
	}
	if (strings.Contains(combined, "readsitememory") || strings.Contains(combined, "readdesignintelligence")) &&
		(strings.Contains(combined, "first") || strings.Contains(combined, "start of") || strings.Contains(combined, "continuation")) {
		return true
	}
	if strings.Contains(combined, "getimageusage") {
		return true
	}
	if strings.Contains(combined, "generateaeo") && strings.Contains(combined, "verifybuild") {
		return true
	}
	return false
}

// sanitizeLegacyPromptContent removes individual lines referencing pruned tools
// (such as getProjectCapabilities) from otherwise-useful surviving prompts.
func sanitizeLegacyPromptContent(content string) string {
	if !strings.Contains(strings.ToLower(content), "getprojectcapabilities") {
		return content
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "getprojectcapabilities") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// filterAndSortPrompts returns prompts matching section and conditions, sorted by priority
func filterAndSortPrompts(prompts []models.TemplatePrompt, section string, caps *models.ProjectCapabilities) []models.TemplatePrompt {
	var result []models.TemplatePrompt
	for _, p := range prompts {
		if p.Section == section && evaluateConditions(p.Conditions, caps) {
			result = append(result, p)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Priority < result[j].Priority
	})
	return result
}

// evaluateConditions checks if all prompt conditions are met (AND logic)
func evaluateConditions(cond *models.PromptConditions, caps *models.ProjectCapabilities) bool {
	if cond == nil {
		return true // No conditions = always include
	}

	if cond.FirebaseEnabled != nil {
		enabled := caps != nil && caps.Firebase != nil && caps.Firebase.Enabled
		if *cond.FirebaseEnabled != enabled {
			return false
		}
	}

	if cond.StorageEnabled != nil {
		enabled := caps != nil && caps.Storage != nil && caps.Storage.Enabled
		if *cond.StorageEnabled != enabled {
			return false
		}
	}

	return true
}

// BuildSystemPrompt creates the system prompt for the AI
func BuildSystemPrompt(cfg PromptConfig) (string, error) {
	// Check for admin-editable custom prompts first
	if cfg.Compact && cfg.CustomCompactPrompt != "" {
		return buildFromCustomPrompt(cfg, cfg.CustomCompactPrompt), nil
	}
	if !cfg.Compact && cfg.CustomPrompt != "" {
		return buildFromCustomPrompt(cfg, cfg.CustomPrompt), nil
	}

	// Load template from disk
	templateFile := filepath.Join(PromptsDir, "system.md")
	if cfg.Compact {
		templateFile = filepath.Join(PromptsDir, "compact.md")
	}
	template, err := os.ReadFile(templateFile)
	if err != nil {
		return "", fmt.Errorf("loading prompt template %s: %w", templateFile, err)
	}

	result := processTemplate(string(template), cfg)

	// Apply token budget if set — trim from the end if over budget
	// This preserves the most critical sections (role, mandatory steps) at the top
	if cfg.TokenBudget > 0 {
		tokens := estimateTokens(result)
		if tokens > cfg.TokenBudget {
			// Trim to approximately fit the budget
			// Use word-based trimming to avoid cutting mid-word
			words := strings.Fields(result)
			targetWords := int(float64(cfg.TokenBudget) / 1.3)
			if targetWords < len(words) {
				result = strings.Join(words[:targetWords], " ") + "\n\n[Prompt truncated to fit context window]"
			}
		}
	}

	return result, nil
}

// processTemplate replaces all markers in a template string with dynamic content.
func processTemplate(template string, cfg PromptConfig) string {
	result := strings.ReplaceAll(template, "{{PROJECT_NAME}}", cfg.ProjectName)

	// Process INJECT markers (template prompt injection points)
	for _, section := range validSections {
		marker := "{{INJECT:" + section + "}}"
		if strings.Contains(result, marker) {
			var sb strings.Builder
			injectTemplatePrompts(&sb, cfg.TemplatePrompts, cfg.Capabilities, section)
			result = replaceMarker(result, marker, sb.String())
		}
	}

	// Process DYNAMIC markers (conditional sections)
	workspaceRef := buildWorkspaceReferenceSection(cfg)
	result = replaceMarker(result, "{{DYNAMIC:TEMPLATE_METADATA}}", workspaceRef)
	result = replaceMarker(result, "{{DYNAMIC:WORKSPACE_REFERENCE}}", workspaceRef)
	result = replaceMarker(result, "{{DYNAMIC:THEME_PRESET}}", buildThemePresetSection(cfg.ThemePreset))
	result = replaceMarker(result, "{{DYNAMIC:THEME_PRESET_COMPACT}}", buildThemePresetCompactSection(cfg.ThemePreset))
	result = replaceMarker(result, "{{DYNAMIC:FILE_TREE}}", buildFileTreeSection(cfg.WorkspacePath))
	result = replaceMarker(result, "{{DYNAMIC:CAPABILITIES}}", buildCapabilitiesSection(cfg.Capabilities))

	return result
}

// replaceMarker replaces a marker in the template. It replaces the marker AND
// its trailing newline (the full marker line) so that empty replacements don't
// leave extra blank lines, and non-empty replacements integrate cleanly.
func replaceMarker(template, marker, replacement string) string {
	markerLine := marker + "\n"
	if strings.Contains(template, markerLine) {
		return strings.ReplaceAll(template, markerLine, replacement)
	}
	// Fallback: marker at end of file without trailing newline
	return strings.ReplaceAll(template, marker, replacement)
}

func buildThemePresetSection(preset *models.ThemePreset) string {
	if preset == nil || preset.ID == "" || strings.EqualFold(preset.ID, "default") {
		return ""
	}
	themeLine := fmt.Sprintf("The user has selected the \"%s\" theme.", preset.Name)
	if preset.Description != "" {
		themeLine = fmt.Sprintf("The user has selected the \"%s\" theme (%s).", preset.Name, preset.Description)
	}

	return fmt.Sprintf(`
## THEME PRESET - USER'S COLOR PREFERENCE

%s This theme is ALREADY applied to the workspace CSS.

**CRITICAL: Do NOT modify index.css — it is a protected system file.**
If the user explicitly asks to adjust colors, add CSS variable overrides in src/custom.css (imported after index.css, so overrides take effect).

When creating UI elements:
- Use semantic color classes: bg-primary, text-foreground, bg-card, bg-muted, bg-accent
- Do NOT hardcode colors like "blue-500" or "gray-900"
- The theme has both light and dark mode support built-in

The user chose this theme for a specific color feel. Do NOT suggest changing colors unless explicitly asked.
`, themeLine)
}

func buildThemePresetCompactSection(preset *models.ThemePreset) string {
	if preset == nil || preset.ID == "" || strings.EqualFold(preset.ID, "default") {
		return ""
	}
	desc := preset.Description
	if desc == "" {
		desc = "curated color system"
	}
	primary := ""
	if preset.Light != nil && preset.Light["primary"] != "" {
		primary = fmt.Sprintf(" (primary HSL: `%s`)", preset.Light["primary"])
	}
	return fmt.Sprintf("\n## THEME PRESET ACTIVE: %s (%s)%s\nThis theme is ALREADY applied to `src/index.css`. Keep its palette harmony by using semantic classes (`bg-primary`, `text-primary-foreground`, `bg-card`, `bg-muted`, `text-muted-foreground`, `border-border`). In `src/custom.css`, focus on typography (`--font-display`, `--font-sans` + Google Fonts) without overriding the user's chosen theme colors unless explicitly asked.\n", preset.Name, desc, primary)
}

func buildFileTreeSection(workspacePath string) string {
	fileTree := getFileTree(workspacePath)
	if fileTree != "" {
		return "\n```\n" + fileTree + "```\n"
	}
	return "\nNo files yet - this is a fresh project.\n"
}

func buildCapabilitiesSection(caps *models.ProjectCapabilities) string {
	if caps == nil {
		return ""
	}
	var sb strings.Builder
	if caps.Firebase != nil && caps.Firebase.Enabled {
		sb.WriteString("## FIREBASE: Enabled. Use getFirestoreCollections to discover collections.\n\n")
	}
	if caps.Storage != nil && caps.Storage.Enabled {
		fmt.Fprintf(&sb, "## STORAGE: Enabled. Max %dMB.\n\n", caps.Storage.MaxFileSizeMB)
	}
	return sb.String()
}

// buildTemplateMetadataSection is retained for compatibility and delegates to buildWorkspaceReferenceSection.
func buildTemplateMetadataSection(meta *models.TemplateMetadata) string {
	return buildWorkspaceReferenceSection(PromptConfig{TemplateMetadata: meta})
}

// buildWorkspaceReferenceSection renders a comprehensive, compact Workspace Reference
// directly into the system prompt so the AI never needs exploratory turns to scan
// boilerplate files (src/components/ui/*, package.json, src/index.css, src/routes.tsx,
// template.json, capabilities, starter Home.tsx, or initial domain stock photos).
func buildWorkspaceReferenceSection(cfg PromptConfig) string {
	workspaceExists := false
	if cfg.WorkspacePath != "" && cfg.WorkspacePath != "/nonexistent" {
		if info, err := os.Stat(cfg.WorkspacePath); err == nil && info.IsDir() {
			workspaceExists = true
		}
	}

	if !workspaceExists && cfg.TemplateMetadata == nil && cfg.Capabilities == nil && strings.TrimSpace(cfg.Goal) == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Workspace Reference (Pre-Loaded — DO NOT readFile Boilerplate)\n\n")

	// 1. Starter-placeholder detection
	if workspaceExists && isStarterPlaceholderHome(cfg.WorkspacePath) {
		sb.WriteString("**Workspace Status:** Fresh starter workspace: `src/pages/Home.tsx` is an empty placeholder — DO NOT read it; overwrite it directly in Turn 1.\n")
	}

	// 2. Inline Project Capabilities summary
	sb.WriteString("**Project Capabilities:** ")
	sb.WriteString(formatCapabilitiesSummary(cfg.Capabilities))
	sb.WriteString("\n")

	// 3. Template Metadata (pages, file structure, custom components, styling)
	meta := cfg.TemplateMetadata
	if meta != nil {
		if len(meta.AvailablePages) > 0 {
			sb.WriteString("**Pages:** ")
			names := make([]string, 0, len(meta.AvailablePages))
			for _, p := range meta.AvailablePages {
				names = append(names, p.Name)
			}
			sb.WriteString(strings.Join(names, ", "))
			sb.WriteString("\n")
		}

		fs := meta.FileStructure
		if fs.PagesDir != "" || fs.ComponentsDir != "" || fs.RoutesFile != "" {
			sb.WriteString("**File structure:** ")
			parts := []string{}
			if fs.PagesDir != "" {
				parts = append(parts, "pages="+fs.PagesDir)
			}
			if fs.ComponentsDir != "" {
				parts = append(parts, "components="+fs.ComponentsDir)
			}
			if fs.RoutesFile != "" {
				parts = append(parts, "routes="+fs.RoutesFile)
			}
			sb.WriteString(strings.Join(parts, ", "))
			sb.WriteString("\n")
		}

		if len(meta.CustomComponents) > 0 {
			names := make([]string, 0, len(meta.CustomComponents))
			for _, c := range meta.CustomComponents {
				if c.Path != "" {
					names = append(names, fmt.Sprintf("%s (`%s`)", c.Name, c.Path))
				} else {
					names = append(names, c.Name)
				}
			}
			sb.WriteString("**Template components:** ")
			sb.WriteString(strings.Join(names, ", "))
			sb.WriteString("\n")
		}

		if meta.Styling.IconSet != "" || meta.Styling.Framework != "" {
			sb.WriteString("**Styling:** ")
			parts := []string{}
			if meta.Styling.IconSet != "" {
				parts = append(parts, "icons="+meta.Styling.IconSet)
			}
			if meta.Styling.Framework != "" {
				parts = append(parts, "framework="+meta.Styling.Framework)
			}
			sb.WriteString(strings.Join(parts, ", "))
			sb.WriteString("\n")
		}
	}

	// 4. Installed npm dependencies from workspace package.json
	if workspaceExists {
		if deps := readInstalledNpmDeps(cfg.WorkspacePath); len(deps) > 0 {
			sb.WriteString("**Installed npm packages:** ")
			sb.WriteString(strings.Join(deps, ", "))
			sb.WriteString("\n")
		}
	}

	// 5. Core CSS tokens already defined in src/index.css
	if workspaceExists || meta != nil {
		sb.WriteString("**Core CSS tokens in `src/index.css` (already configured — DO NOT read or edit `src/index.css`):** `--background`, `--foreground`, `--card`, `--primary`, `--secondary`, `--muted`, `--accent`, `--destructive`, `--border`, `--input`, `--ring`, `--radius` (all color tokens are raw HSL triplets `H S% L%`; override in `src/custom.css` only).\n")
	}

	// 6. Compact shadcn/ui component export reference dynamically generated from registry.ComponentRegistry
	if exportLines := buildShadcnExportReference(meta, cfg.WorkspacePath); len(exportLines) > 0 {
		sb.WriteString("\n**shadcn/ui Component Exports (import via `@/components/ui/...` — NEVER `readFile` any file under `src/components/ui/`):**\n")
		for _, line := range exportLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	// 7. Exact src/routes.tsx content when it exists and is <= 2500 bytes
	if workspaceExists {
		routesRel := "src/routes.tsx"
		if meta != nil && meta.FileStructure.RoutesFile != "" {
			routesRel = meta.FileStructure.RoutesFile
		}
		routesPath := filepath.Join(cfg.WorkspacePath, filepath.FromSlash(routesRel))
		if data, err := os.ReadFile(routesPath); err == nil && len(data) > 0 && len(data) <= 2500 {
			fmt.Fprintf(&sb, "\n**Current `%s` (pre-loaded — do NOT `readFile` it):**\n```tsx\n%s\n```\n", routesRel, strings.TrimRight(string(data), "\r\n"))
		}
	}

	// 8. Pre-selected domain stock images (local registry lookup, 0ms, zero HTTP)
	if images := preselectDomainStockImages(cfg.Goal); len(images) > 0 {
		sb.WriteString("\n**Pre-Selected Domain Stock Images (verified & ready for Turn 1 — no need to call `listImages` unless you want more; NEVER reuse a URL across two slots):**\n")
		for _, img := range images {
			url := registry.FormatImageURL(img, "")
			fmt.Fprintf(&sb, "- `[%s | %s | %s/%s]`: %s\n", img.Type, img.Subject, img.Tone, img.Contrast, url)
		}
	}

	sb.WriteString("\n")
	return sb.String()
}

// isStarterPlaceholderHome detects whether src/pages/Home.tsx is still the untouched starter placeholder.
func isStarterPlaceholderHome(workspacePath string) bool {
	homePath := filepath.Join(workspacePath, "src", "pages", "Home.tsx")
	data, err := os.ReadFile(homePath)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "Welcome to Your Website") || strings.Contains(content, "Your website starts here")
}

// formatCapabilitiesSummary renders project capabilities in a single inline line.
func formatCapabilitiesSummary(caps *models.ProjectCapabilities) string {
	firebaseStatus := "disabled"
	if caps != nil && caps.Firebase != nil && caps.Firebase.Enabled {
		if caps.Firebase.CollectionPrefix != "" {
			firebaseStatus = fmt.Sprintf("enabled (prefix: %s)", caps.Firebase.CollectionPrefix)
		} else {
			firebaseStatus = "enabled"
		}
	}

	supabaseStatus := "disabled"

	storageStatus := "disabled"
	if caps != nil && caps.Storage != nil && caps.Storage.Enabled {
		if caps.Storage.MaxFileSizeMB > 0 {
			storageStatus = fmt.Sprintf("enabled (max %dMB)", caps.Storage.MaxFileSizeMB)
		} else {
			storageStatus = "enabled"
		}
	}

	return fmt.Sprintf("Firebase: %s | Supabase: %s | File Storage: %s", firebaseStatus, supabaseStatus, storageStatus)
}

// readInstalledNpmDeps reads sorted dependency package names from workspace package.json.
func readInstalledNpmDeps(workspacePath string) []string {
	data, err := os.ReadFile(filepath.Join(workspacePath, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || len(pkg.Dependencies) == 0 {
		return nil
	}
	deps := make([]string, 0, len(pkg.Dependencies))
	for name := range pkg.Dependencies {
		deps = append(deps, name)
	}
	sort.Strings(deps)
	return deps
}

// normalizeComponentKey lowercases and strips separators so "dropdown-menu", "DropdownMenu",
// and "sonner" match keys in registry.ComponentRegistry cleanly.
func normalizeComponentKey(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".tsx")
	n = strings.TrimSuffix(n, ".ts")
	n = strings.ReplaceAll(n, "-", "")
	n = strings.ReplaceAll(n, "_", "")
	n = strings.ReplaceAll(n, " ", "")
	if n == "sonner" {
		return "toast"
	}
	return n
}

// buildShadcnExportReference builds sorted `- \`@/components/ui/...\`: Exports` lines
// dynamically from registry.ComponentRegistry filtered to the template's installed components.
func buildShadcnExportReference(meta *models.TemplateMetadata, workspacePath string) []string {
	allowed := make(map[string]bool)

	if meta != nil && len(meta.ShadcnComponents) > 0 {
		for _, comp := range meta.ShadcnComponents {
			allowed[normalizeComponentKey(comp)] = true
		}
	} else if workspacePath != "" && workspacePath != "/nonexistent" {
		uiDir := filepath.Join(workspacePath, "src", "components", "ui")
		if entries, err := os.ReadDir(uiDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				if strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".ts") {
					allowed[normalizeComponentKey(name)] = true
				}
			}
		}
	}

	if len(allowed) == 0 {
		return nil
	}

	seenLines := make(map[string]bool)
	var lines []string
	for regKey, info := range registry.ComponentRegistry {
		if !allowed[normalizeComponentKey(regKey)] {
			continue
		}
		for _, parsed := range parseImportExports(info.Import) {
			if !seenLines[parsed] {
				seenLines[parsed] = true
				lines = append(lines, parsed)
			}
		}
	}
	sort.Strings(lines)
	return lines
}

// parseImportExports converts lines like:
//   import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
// into compact reference lines:
//   - `@/components/ui/tabs`: Tabs, TabsList, TabsTrigger, TabsContent
func parseImportExports(importBlock string) []string {
	var out []string
	for _, rawLine := range strings.Split(importBlock, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		openBrace := strings.Index(line, "{")
		closeBrace := strings.Index(line, "}")
		fromIdx := strings.Index(line, "from")
		if openBrace == -1 || closeBrace == -1 || fromIdx == -1 || closeBrace < openBrace {
			continue
		}
		exports := strings.TrimSpace(line[openBrace+1 : closeBrace])
		pathPart := strings.TrimSpace(line[fromIdx+len("from"):])
		pathPart = strings.TrimRight(pathPart, ";")
		pathPart = strings.Trim(pathPart, `"' `)
		if exports == "" || pathPart == "" {
			continue
		}
		out = append(out, fmt.Sprintf("- `%s`: %s", pathPart, exports))
	}
	return out
}

// goalStopWords filters generic website-building phrasing when extracting domain image keywords.
var goalStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "for": true, "with": true,
	"of": true, "to": true, "in": true, "on": true, "at": true, "by": true, "from": true,
	"my": true, "our": true, "your": true, "this": true, "that": true, "it": true, "is": true,
	"be": true, "as": true, "are": true, "was": true, "have": true, "has": true, "do": true,
	"build": true, "create": true, "make": true, "design": true, "develop": true, "generate": true,
	"website": true, "site": true, "webapp": true, "web": true, "page": true, "pages": true,
	"landing": true, "homepage": true, "home": true, "app": true, "application": true,
	"modern": true, "clean": true, "sleek": true, "beautiful": true, "stunning": true,
	"professional": true, "responsive": true, "minimal": true, "minimalist": true,
	"please": true, "want": true, "need": true, "like": true, "using": true, "include": true,
	"including": true, "features": true, "feature": true, "section": true, "sections": true,
	"add": true, "update": true, "change": true, "edit": true, "fix": true, "makeover": true,
	"full": true, "complete": true, "new": true, "best": true, "great": true, "good": true,
	"color": true, "colors": true, "theme": true, "style": true, "styling": true, "font": true,
	"button": true, "buttons": true, "hero": true, "footer": true, "navbar": true, "header": true,
	"about": true, "contact": true, "pricing": true, "testimonials": true, "faq": true,
}

// extractGoalKeywords extracts clean domain keywords from a user goal string.
func extractGoalKeywords(goal string) string {
	clean := goal
	// Strip system-appended notes or follow-up wrappers
	for _, marker := range []string{"\n\n(Note:", "[Follow-Up Edit Mode", "[Attached Images", "[Attached Files", "[Visual Design Blueprint"} {
		if idx := strings.Index(clean, marker); idx != -1 {
			clean = clean[:idx]
		}
	}
	clean = strings.ToLower(strings.TrimSpace(clean))
	if clean == "" {
		return ""
	}

	words := strings.FieldsFunc(clean, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	seen := make(map[string]bool)
	var keywords []string
	for _, w := range words {
		if len(w) <= 2 || goalStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		keywords = append(keywords, w)
		if len(keywords) >= 12 {
			break
		}
	}
	return strings.Join(keywords, " ")
}

// preselectDomainStockImages returns 8-10 verified stock image entries using local in-memory
// registry search (0ms, zero HTTP calls) so the AI has domain photography immediately in Turn 1.
func preselectDomainStockImages(goal string) []registry.ImageEntry {
	if strings.TrimSpace(goal) == "" {
		return nil
	}

	keywords := extractGoalKeywords(goal)
	var results []registry.ImageEntry
	seen := make(map[string]bool)

	appendUnique := func(entries []registry.ImageEntry, maxTotal int) {
		for _, img := range entries {
			if len(results) >= maxTotal {
				break
			}
			url := registry.FormatImageURL(img, "")
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			results = append(results, img)
		}
	}

	if keywords != "" {
		// Prioritize domain-matched photos (up to 8) + portraits for testimonials/team (up to 10 total)
		appendUnique(registry.SearchImages("", keywords, "", "", 8), 8)
		if len(results) < 10 {
			appendUnique(registry.SearchImages("gallery", "avatar portrait testimonial", "", "", 3), 10)
		}
	}

	// Ensure at least 8 verified distinct images even for very abstract or short prompts
	if len(results) < 8 {
		appendUnique(registry.SearchImages("gallery", "", "", "", 10), 8)
	}

	return results
}

// getFileTree returns a string representation of the file tree, collapsing
// src/components/ui/* into a single summary line to avoid boilerplate scanning.
func getFileTree(workspacePath string) string {
	var sb strings.Builder

	err := filepath.Walk(workspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden files, node_modules, and dist
		name := info.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(workspacePath, path)
		if relPath == "." {
			return nil
		}

		// Calculate indentation based on depth
		depth := strings.Count(relPath, string(os.PathSeparator))
		indent := strings.Repeat("  ", depth)

		// Collapse src/components/ui/* into a single line
		if info.IsDir() && filepath.ToSlash(relPath) == "src/components/ui" {
			fmt.Fprintf(&sb, "%sui/ (all standard shadcn/ui primitives — exports listed in Workspace Reference above; DO NOT readFile these)\n", indent)
			return filepath.SkipDir
		}

		if info.IsDir() {
			fmt.Fprintf(&sb, "%s%s/\n", indent, name)
		} else {
			fmt.Fprintf(&sb, "%s%s\n", indent, name)
		}

		return nil
	})

	if err != nil {
		return ""
	}

	return sb.String()
}

// buildFromCustomPrompt uses an admin-editable prompt as the base,
// processing all markers ({{PROJECT_NAME}}, {{INJECT:*}}, {{DYNAMIC:*}}).
func buildFromCustomPrompt(cfg PromptConfig, customPrompt string) string {
	return processTemplate(customPrompt, cfg)
}
