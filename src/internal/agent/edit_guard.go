package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"crevo-builder/internal/executor"
)

// EditIntent captures what scope of modifications the user explicitly requested
// when editing an already-built application.
type EditIntent struct {
	WantsFullRedesign bool
	WantsLogoChange   bool
	WantsThemeChange  bool
	WantsImageChanges bool
}

// DetectedLogoAsset summarizes an existing brand logo found in the workspace.
type DetectedLogoAsset struct {
	FilePath     string
	UploadedURLs []string
	HasLogoMark  bool
}

var (
	stripGoalMarkers = []string{
		"\n\n[Follow-Up Edit Mode",
		"\n\n[Attached Images",
		"\n\n[Attached Files",
		"\n\n[Visual Design Blueprint",
		"\n\n[Firebase Database",
		"\n\n[Project File Storage",
		"\n\n[Supabase Database",
		"\n\n[Admin Internal AI",
		"\n\n[Internal AI Provider",
		"\n\nNOTE: The user has asked",
	}

	keepLogoNegationRe = regexp.MustCompile(`(?i)\b(?:don'?t|do\s+not|never|avoid|without)\s+(?:change|touch|alter|replace|modify|edit|remove|delete|update|mess\s+with)\b[^.!?\n]{0,40}\b(?:logo|brand\s*mark|wordmark)\b|\b(?:keep|preserve|retain|leave)\b[^.!?\n]{0,30}\b(?:the\s+|current\s+|existing\s+)?(?:logo|brand\s*mark|wordmark)\b`)
	logoChangeRe       = regexp.MustCompile(`(?i)\b(?:change|replace|update|swap|redesign|remove|delete|edit|modify|fix|resize|make|add|upload|use|set|put|insert)\b[^.!?\n]{0,45}\b(?:logo|brand\s*mark|wordmark|favicon)\b|\b(?:new|different|custom|updated)\s+(?:logo|brand\s*mark|wordmark|favicon)\b|\blogo\b[^.!?\n]{0,30}\b(?:to|with|into|should\s+be|needs\s+to\s+be|change|replace|update|bigger|smaller|removed)\b`)

	keepUiNegationRe       = regexp.MustCompile(`(?i)\b(?:don'?t|do\s+not|never)\s+(?:change|redesign|alter|touch|rebuild|rewrite)\b[^.!?\n]{0,40}\b(?:the\s+)?(?:entire|whole|rest\s+of\s+the|existing|current)\s+(?:ui|design|layout|app|site|website|page)\b|\bkeep\s+(?:the\s+)?(?:same|existing|current)\s+(?:ui|design|layout)\b`)
	fullRedesignRe         = regexp.MustCompile(`(?i)\b(?:redesign|rebuild|remake|recreate|overhaul|revamp|start\s+over|start\s+from\s+scratch|build\s+from\s+scratch|completely\s+new\s+(?:design|ui|layout|website|app)|change\s+(?:the\s+)?(?:entire|whole)\s+(?:ui|design|layout|app|site|website))\b`)
	fullScopeRe            = regexp.MustCompile(`(?i)\b(?:entire|whole|full|complete|everything|all\s+pages|from\s+scratch|start\s+over|completely\s+new|the\s+app|the\s+site|the\s+website|the\s+ui)\b`)
	scopedRedesignTargetRe = regexp.MustCompile(`(?i)\b(?:redesign|revamp|overhaul|rebuild)\s+(?:only\s+)?(?:the\s+|this\s+)?(?:hero|header|navbar|nav|footer|sidebar|card|cards|button|buttons|form|modal|dialog|drawer|pricing|testimonial|testimonials|faq|contact|about|login|signup|profile|settings|calendar|chart|table|banner|section)\b`)

	themeChangeRe  = regexp.MustCompile(`(?i)\b(?:change|switch|update|make|set|use)\b[^.!?\n]{0,40}\b(?:color\s*(?:scheme|palette|theme)|theme\s*colors?|brand\s*colors?|primary\s*color|background\s*color|dark\s*theme|light\s*theme|color\s*palette|global\s*(?:colors?|fonts?|typography|theme)|font\s*family|typography)\b|\b(?:retheme|re-theme|new\s+color\s+(?:scheme|palette)|different\s+color\s+(?:scheme|palette))\b`)
	imageRequestRe = regexp.MustCompile(`(?i)\b(?:image|images|photo|photos|picture|pictures|gallery|banner|hero|avatar|avatars|thumbnail|thumbnails|background\s+image|stock\s+photo|add\s+(?:a\s+)?(?:new\s+)?(?:section|page|testimonials|team|portfolio|menu|products|services|blog|gallery))\b`)

	uploadedFileURLRe = regexp.MustCompile(`(?:https?://[^\s"'<>)]+)?/api/files/[^\s"'<>)]+`)
	logoLineHintRe    = regexp.MustCompile(`(?i)\b(?:logo|brand|wordmark|navbar|header|topbar|appbar|favicon)\b`)
)

func normalizeWorkspaceRelPath(p string) string {
	cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	return strings.TrimPrefix(cleaned, "./")
}

func stripSystemNoteMarkers(goal string) string {
	clean := strings.TrimSpace(goal)
	for _, marker := range stripGoalMarkers {
		if idx := strings.Index(clean, marker); idx != -1 {
			clean = clean[:idx]
		}
	}
	return strings.TrimSpace(clean)
}

// ClassifyEditIntent analyzes ONLY the user's raw prompt text (excluding injected
// system notes) to determine whether they explicitly asked for a logo change,
// a whole-app UI redesign, or a global color/theme update.
func ClassifyEditIntent(goal string) EditIntent {
	prompt := stripSystemNoteMarkers(goal)
	if prompt == "" {
		return EditIntent{}
	}

	keepLogo := keepLogoNegationRe.MatchString(prompt)
	wantsLogoChange := !keepLogo && logoChangeRe.MatchString(prompt)

	keepUI := keepUiNegationRe.MatchString(prompt)
	mentionsFullScope := fullScopeRe.MatchString(prompt)
	isScopedOnly := scopedRedesignTargetRe.MatchString(prompt) && !mentionsFullScope
	wantsFullRedesign := !keepUI && !isScopedOnly && fullRedesignRe.MatchString(prompt) && mentionsFullScope

	wantsThemeChange := wantsFullRedesign || themeChangeRe.MatchString(prompt)
	wantsImageChanges := wantsFullRedesign || imageRequestRe.MatchString(prompt)

	return EditIntent{
		WantsFullRedesign: wantsFullRedesign,
		WantsLogoChange:   wantsLogoChange,
		WantsThemeChange:  wantsThemeChange,
		WantsImageChanges: wantsImageChanges,
	}
}

func isLogoOrShellFile(relPath string) bool {
	lower := strings.ToLower(normalizeWorkspaceRelPath(relPath))
	base := filepath.Base(lower)
	for _, token := range []string{"logo", "brand", "nav", "header", "topbar", "appshell", "layout", "sidebar", "footer"} {
		if strings.Contains(base, token) {
			return true
		}
	}
	return false
}

func extractLogoURLsFromContent(relPath, content string) []string {
	shellFile := isLogoOrShellFile(relPath)
	seen := make(map[string]bool)
	var urls []string

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		matches := uploadedFileURLRe.FindAllString(line, -1)
		if len(matches) == 0 {
			continue
		}
		// Inspect surrounding window (+/-2 lines) for logo/brand/header cues
		ctxStart := i - 2
		if ctxStart < 0 {
			ctxStart = 0
		}
		ctxEnd := i + 2
		if ctxEnd >= len(lines) {
			ctxEnd = len(lines) - 1
		}
		window := strings.Join(lines[ctxStart:ctxEnd+1], "\n")
		isLogoLine := shellFile || logoLineHintRe.MatchString(window)
		if !isLogoLine {
			continue
		}
		for _, m := range matches {
			u := strings.TrimRight(m, `.,;:'")`)
			if u != "" && !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	sort.Strings(urls)
	return urls
}

// ScanWorkspaceLogos inspects existing custom source files in the workspace to identify
// where the brand logo is rendered and what uploaded logo URLs exist.
func ScanWorkspaceLogos(workspacePath string) []DetectedLogoAsset {
	if workspacePath == "" || workspacePath == "/nonexistent" {
		return nil
	}

	candidates := []string{
		"src/App.tsx",
		"src/pages/Home.tsx",
	}

	compDir := filepath.Join(workspacePath, "src", "components")
	if entries, err := os.ReadDir(compDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".jsx") {
				candidates = append(candidates, "src/components/"+name)
			}
		}
	}

	seenFiles := make(map[string]bool)
	var assets []DetectedLogoAsset
	for _, rel := range candidates {
		norm := normalizeWorkspaceRelPath(rel)
		if seenFiles[norm] {
			continue
		}
		seenFiles[norm] = true

		data, err := os.ReadFile(filepath.Join(workspacePath, filepath.FromSlash(norm)))
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, "Welcome to Your Website") {
			continue
		}

		urls := extractLogoURLsFromContent(norm, content)
		hasMark := false
		if isLogoOrShellFile(norm) {
			lower := strings.ToLower(content)
			if strings.Contains(lower, "<svg") || strings.Contains(lower, "<img") || strings.Contains(lower, "logo") {
				hasMark = true
			}
		}

		if len(urls) > 0 || hasMark {
			assets = append(assets, DetectedLogoAsset{
				FilePath:     norm,
				UploadedURLs: urls,
				HasLogoMark:  hasMark,
			})
		}
	}

	return assets
}

// BuildExistingAppPreservationSection renders a high-priority preservation contract
// into the Workspace Reference whenever V1 of the app has already been built.
func BuildExistingAppPreservationSection(workspacePath, goal string) string {
	if !executor.WorkspaceHasCustomizedApp(workspacePath) {
		return ""
	}

	intent := ClassifyEditIntent(goal)
	logos := ScanWorkspaceLogos(workspacePath)

	var sb strings.Builder
	sb.WriteString("**Workspace Status:** EXISTING CUSTOMIZED APP (V1 ALREADY BUILT) — **STRICT SURGICAL EDIT MODE**.\n")
	sb.WriteString("- **UNDERSTAND THE EXACT USER NEED FIRST:** Carefully identify the exact feature, bug fix, section, copy, or component the user asked for. Do NOT redesign, refactor, or rewrite anything the user did NOT ask to change.\n")

	if !intent.WantsLogoChange {
		sb.WriteString("- **LOCKED BRAND LOGO (DO NOT CHANGE):** The user did NOT ask to change the logo. NEVER replace, redraw, swap icons on, resize, or remove the existing logo or brand mark anywhere in the app.\n")
		if len(logos) > 0 {
			var parts []string
			for _, l := range logos {
				if len(l.UploadedURLs) > 0 {
					parts = append(parts, fmt.Sprintf("`%s` (locked asset URL: `%s`)", l.FilePath, strings.Join(l.UploadedURLs, "`, `")))
				} else {
					parts = append(parts, fmt.Sprintf("`%s`", l.FilePath))
				}
			}
			sb.WriteString("  * Existing logo / brand shell files: " + strings.Join(parts, ", ") + "\n")
		}
	} else {
		sb.WriteString("- **LOGO UPDATE REQUESTED:** Apply the exact logo change the user requested while keeping the rest of the header, navigation, and app UI intact.\n")
	}

	if !intent.WantsFullRedesign {
		sb.WriteString("- **LOCKED OVERALL UI & LAYOUT (SURGICAL EDITS ONLY):** The user did NOT ask to redesign the entire app. NEVER rewrite untouched pages, navigation/header/footer chrome, or layout structure. Call `readFiles` ONLY on the specific target file(s) you need to change, then apply minimal surgical `editFile` patches.\n")
	} else {
		sb.WriteString("- **FULL REDESIGN REQUESTED:** Redesign the app UI as explicitly requested while keeping existing data wiring and working routes (and keeping the existing logo unless the user also asked to change it).\n")
	}

	if !intent.WantsThemeChange {
		sb.WriteString("- **LOCKED THEME & FONTS (`src/custom.css`):** Do NOT alter existing global CSS color variables or font families in `src/custom.css`.\n")
	}

	return sb.String()
}

// ValidateFollowUpWrite enforces deterministic runtime protection against accidental
// logo deletion, global CSS theme wiping, or blind full-file rewrites on an existing built app.
// Returns an empty string if the write is allowed, or a clear corrective error message if blocked.
func ValidateFollowUpWrite(workspacePath, relPath, newContent string, intent EditIntent, readFiles map[string]bool) string {
	norm := normalizeWorkspaceRelPath(relPath)
	if norm == "" {
		return ""
	}

	fullPath := filepath.Join(workspacePath, filepath.FromSlash(norm))
	existingBytes, err := os.ReadFile(fullPath)
	if err != nil {
		// New file (does not exist yet) — always allowed
		return ""
	}
	existingStr := string(existingBytes)
	if strings.TrimSpace(existingStr) == "" || strings.Contains(existingStr, "Welcome to Your Website") {
		return ""
	}

	// 1. Protect global theme tokens in src/custom.css unless theme/redesign was requested
	if strings.EqualFold(norm, "src/custom.css") && !intent.WantsFullRedesign && !intent.WantsThemeChange {
		return "SURGICAL EDIT GUARD: 'src/custom.css' defines the existing app's theme colors and typography, and the user did NOT ask to change the global theme or redesign the UI. Do NOT overwrite src/custom.css. If you truly need a small helper rule, read src/custom.css first and use editFile without modifying existing :root or .dark variables."
	}

	// 2. Protect uploaded logo URLs when the user did NOT ask to change the logo
	if !intent.WantsLogoChange {
		existingLogoURLs := extractLogoURLsFromContent(norm, existingStr)
		for _, url := range existingLogoURLs {
			if !strings.Contains(newContent, url) {
				return fmt.Sprintf("PROTECTED LOGO GUARD: Your change to '%s' removed or altered the existing logo asset (%s), but the user did NOT ask to change the logo. Preserve the exact existing logo markup and URL, and use editFile to apply only the requested change.", norm, url)
			}
		}
	}

	// 3. Block blind full-file overwrites of existing customized files that were not read first in this turn
	if !intent.WantsFullRedesign && len(existingStr) > 600 && !readFiles[norm] {
		return fmt.Sprintf("SURGICAL EDIT GUARD: '%s' already exists in this built app and you have NOT read it in this turn. Call readFiles(['%s']) first to inspect the existing UI and logo, then apply minimal surgical editFile patches instead of overwriting the whole file.", norm, norm)
	}

	// 4. Protect logo/header/shell brand markup from being silently dropped on full rewrites
	if !intent.WantsFullRedesign && !intent.WantsLogoChange && isLogoOrShellFile(norm) && len(existingStr) > 400 {
		existingLower := strings.ToLower(existingStr)
		newLower := strings.ToLower(newContent)
		if strings.Contains(existingLower, "<img") && !strings.Contains(newLower, "<img") {
			return fmt.Sprintf("PROTECTED LOGO GUARD: Your overwrite of '%s' removed an existing <img> brand/logo element, but the user did NOT ask to change the logo. Preserve the existing logo element and use editFile for targeted changes.", norm)
		}
	}

	return ""
}

// ValidateFollowUpEdit verifies that a targeted editFile call on an existing built app
// does not accidentally remove a locked uploaded logo URL when the user did not ask to change the logo.
func ValidateFollowUpEdit(workspacePath, relPath string, args map[string]interface{}, intent EditIntent) string {
	if intent.WantsLogoChange {
		return ""
	}
	norm := normalizeWorkspaceRelPath(relPath)
	if norm == "" {
		return ""
	}
	fullPath := filepath.Join(workspacePath, filepath.FromSlash(norm))
	existingBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return ""
	}
	existingLogoURLs := extractLogoURLsFromContent(norm, string(existingBytes))
	if len(existingLogoURLs) == 0 {
		return ""
	}

	checkPair := func(search, replace string) string {
		for _, url := range existingLogoURLs {
			if strings.Contains(search, url) && !strings.Contains(replace, url) {
				return fmt.Sprintf("PROTECTED LOGO GUARD: Your editFile patch on '%s' removes or alters the existing logo asset (%s), but the user did NOT ask to change the logo. Keep the existing logo URL unchanged.", norm, url)
			}
		}
		return ""
	}

	if editsRaw, ok := args["edits"].([]interface{}); ok && len(editsRaw) > 0 {
		for _, item := range editsRaw {
			if m, ok := item.(map[string]interface{}); ok {
				s, _ := m["search"].(string)
				r, _ := m["replace"].(string)
				if msg := checkPair(s, r); msg != "" {
					return msg
				}
			}
		}
		return ""
	}

	s, _ := args["search"].(string)
	r, _ := args["replace"].(string)
	return checkPair(s, r)
}
