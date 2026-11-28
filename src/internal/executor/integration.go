package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// placeholderPatterns matches file names that are likely garbage/placeholder files
// created when AI gets confused and can't properly delete files
var placeholderPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)DELETED`),      // DELETED.txt, Deleted.tsx
	regexp.MustCompile(`(?i)Fixed\.tsx$`),  // HomeFixed.tsx, AboutFixed.tsx
	regexp.MustCompile(`(?i)New\.tsx$`),    // HomeNew.tsx, NewHome.tsx (but not just New.tsx)
	regexp.MustCompile(`(?i)Old\.tsx$`),    // HomeOld.tsx, OldHome.tsx
	regexp.MustCompile(`(?i)Copy\.tsx$`),   // HomeCopy.tsx
	regexp.MustCompile(`(?i)Backup\.tsx$`), // HomeBackup.tsx
	regexp.MustCompile(`(?i)\.bak$`),       // Home.tsx.bak
	regexp.MustCompile(`(?i)\.tmp$`),       // Home.tsx.tmp
	regexp.MustCompile(`(?i)~$`),           // Home.tsx~
}

// isPlaceholderFile returns true if the filename matches placeholder patterns
func isPlaceholderFile(path string) bool {
	for _, pattern := range placeholderPatterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

// isPlaceholderContent returns true if the file content indicates it's a placeholder
func isPlaceholderContent(content string) bool {
	// Very short files (less than 50 chars) are likely placeholders
	if len(strings.TrimSpace(content)) < 50 {
		return true
	}

	// Check for common placeholder markers
	lowerContent := strings.ToLower(content)
	placeholderMarkers := []string{
		"// deleted",
		"// removed",
		"// this file has been removed",
		"// this file has been deleted",
		"export default null",
		"export default undefined",
		"// placeholder",
	}
	for _, marker := range placeholderMarkers {
		if strings.Contains(lowerContent, marker) {
			return true
		}
	}

	return false
}

// IntegrationExecutor verifies component integration
type IntegrationExecutor struct {
	workspacePath string
}

// NewIntegrationExecutor creates a new integration executor
func NewIntegrationExecutor(workspacePath string) *IntegrationExecutor {
	return &IntegrationExecutor{workspacePath: workspacePath}
}

// IntegrationIssue represents a single integration problem
type IntegrationIssue struct {
	File    string `json:"file"`
	Issue   string `json:"issue"`
	Details string `json:"details"`
}

// VerifyIntegration checks if files are properly integrated in App.tsx or routes.tsx
func (e *IntegrationExecutor) VerifyIntegration(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	filesArg, ok := args["files"]
	if !ok {
		return &ToolResult{
			Success: false,
			Content: "No files provided to verify",
		}, nil
	}

	// Handle both []interface{} and []string
	var files []string
	switch v := filesArg.(type) {
	case []interface{}:
		for _, f := range v {
			if s, ok := f.(string); ok {
				files = append(files, s)
			}
		}
	case []string:
		files = v
	default:
		return &ToolResult{
			Success: false,
			Content: "Invalid files parameter type",
		}, nil
	}

	if len(files) == 0 {
		return &ToolResult{
			Success: false,
			Content: "No files provided to verify",
		}, nil
	}

	// Load template.json for routing context (graceful fallback if missing)
	tmplConfig := loadTemplateRouteConfig(e.workspacePath)

	// Determine routes file path from template.json or use default
	routesFilePath := "src/routes.tsx"
	if tmplConfig != nil && tmplConfig.RoutesFile != "" {
		routesFilePath = tmplConfig.RoutesFile
	}

	// Try to read routes file first (preferred), then App.tsx
	var mainContent string
	var mainFile string

	routesPath := filepath.Join(e.workspacePath, routesFilePath)
	if content, err := os.ReadFile(routesPath); err == nil {
		mainContent = string(content)
		mainFile = filepath.Base(routesFilePath)
	} else {
		// Fall back to App.tsx
		appPath := filepath.Join(e.workspacePath, "src/App.tsx")
		content, err := os.ReadFile(appPath)
		if err != nil {
			return &ToolResult{
				Success: false,
				Content: fmt.Sprintf("Cannot read App.tsx or %s: %s", routesFilePath, err.Error()),
			}, nil
		}
		mainContent = string(content)
		mainFile = "App.tsx"
	}

	var issues []IntegrationIssue
	var skippedPlaceholders []string

	for _, filePath := range files {
		// Only check page files
		if !strings.Contains(filePath, "src/pages/") {
			continue
		}

		// Skip placeholder files by name pattern
		if isPlaceholderFile(filePath) {
			skippedPlaceholders = append(skippedPlaceholders, filePath)
			continue
		}

		// Check if file exists
		fullPath := filepath.Join(e.workspacePath, filePath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			issues = append(issues, IntegrationIssue{
				File:    filePath,
				Issue:   "file_not_found",
				Details: "File does not exist",
			})
			continue
		}

		// Check if file content indicates it's a placeholder
		content, err := os.ReadFile(fullPath)
		if err == nil && isPlaceholderContent(string(content)) {
			skippedPlaceholders = append(skippedPlaceholders, filePath)
			continue
		}

		// Extract component name from file path
		componentName := extractComponentName(filePath)
		if componentName == "" {
			continue
		}

		// Check if imported
		// Match patterns like: import Home from './pages/Home'
		// or: import { Home } from './pages/Home'
		importPattern := regexp.MustCompile(fmt.Sprintf(`import\s+(?:%s|\{[^}]*%s[^}]*\})\s+from\s+['"]\.?/?(?:\.\.\/)*(?:src\/)?pages\/%s['"]`, componentName, componentName, componentName))
		if !importPattern.MatchString(mainContent) {
			// Also check for simpler import patterns
			simpleImport := regexp.MustCompile(fmt.Sprintf(`import\s+%s\s+from`, componentName))
			if !simpleImport.MatchString(mainContent) {
				issues = append(issues, IntegrationIssue{
					File:    filePath,
					Issue:   "unimported",
					Details: fmt.Sprintf("Component '%s' is not imported in %s. Add: import %s from './pages/%s'", componentName, mainFile, componentName, componentName),
				})
				continue
			}
		}

		// Check if used/rendered (in JSX or routes array)
		// Matches: <Home />, <Home/>, <Home>, element: <Home, Component: Home, component={Home}, lazy imports
		usagePatterns := []string{
			fmt.Sprintf(`<%s\s*/?>`, componentName),            // <Home /> or <Home/> or <Home>
			fmt.Sprintf(`<%s\s+[^>]*>`, componentName),         // <Home prop="value">
			fmt.Sprintf(`element:\s*<%s`, componentName),       // element: <Home
			fmt.Sprintf(`[Cc]omponent:\s*%s\b`, componentName), // Component: Home or component: Home
			fmt.Sprintf(`component=\{%s\}`, componentName),     // component={Home}
			fmt.Sprintf(`lazy\([^)]*%s`, componentName),        // lazy(() => import('./pages/Home'))
			fmt.Sprintf(`['"]/%s['"]`, strings.ToLower(componentName)), // path: '/home' in RouteConfig
			fmt.Sprintf(`(?i)label:\s*['"][^'"]*%s[^'"]*['"]`, componentName), // label: 'Home' or 'home' in RouteConfig
		}

		used := false
		for _, pattern := range usagePatterns {
			if regexp.MustCompile(pattern).MatchString(mainContent) {
				used = true
				break
			}
		}

		if !used {
			issues = append(issues, IntegrationIssue{
				File:    filePath,
				Issue:   "unused",
				Details: fmt.Sprintf("Component '%s' is imported but not rendered. Add it to the routes array or JSX.", componentName),
			})
		}
	}

	// Check layout component usage in page files
	templatePath := filepath.Join(e.workspacePath, "template.json")
	if tmplData, err := os.ReadFile(templatePath); err == nil {
		var tmpl struct {
			CustomComponents []struct {
				Name string `json:"name"`
			} `json:"custom_components"`
		}
		if jsonErr := json.Unmarshal(tmplData, &tmpl); jsonErr == nil {
			var layoutName string
			for _, comp := range tmpl.CustomComponents {
				if strings.Contains(strings.ToLower(comp.Name), "layout") {
					layoutName = comp.Name
					break
				}
			}
			if layoutName != "" {
				// Check if Layout is applied centrally in App.tsx.
				// Templates wrap routes like: <Layout>{route.element}</Layout>
				// When detected, pages don't need to import Layout themselves.
				// Detection: if App.tsx has both <Layout and </Layout>, it wraps children centrally.
				centralContent := mainContent
				appTsxPath := filepath.Join(e.workspacePath, "src/App.tsx")
				if appData, readErr := os.ReadFile(appTsxPath); readErr == nil {
					centralContent = string(appData)
				}
				hasCentralLayout := strings.Contains(centralContent, "<"+layoutName) &&
					strings.Contains(centralContent, "</"+layoutName+">")

				if !hasCentralLayout {
					// No centralized layout — check individual pages for Layout import
					// Find pages with layout: 'bare' in routes file -- they intentionally skip Layout
					// Handles both single-line and multi-line route objects by matching
					// element and layout: 'bare' within the same route object ({ ... })
					bareLayoutPages := make(map[string]bool)
					// element before layout
					barePattern := regexp.MustCompile(`(?s)\{[^}]*?element:\s*<(\w+)[^}]*?layout:\s*['"]bare['"][^}]*?\}`)
					// layout before element
					barePatternReverse := regexp.MustCompile(`(?s)\{[^}]*?layout:\s*['"]bare['"][^}]*?element:\s*<(\w+)[^}]*?\}`)
					for _, m := range barePattern.FindAllStringSubmatch(mainContent, -1) {
						bareLayoutPages[m[1]] = true
					}
					for _, m := range barePatternReverse.FindAllStringSubmatch(mainContent, -1) {
						bareLayoutPages[m[1]] = true
					}

					for _, filePath := range files {
						if isPlaceholderFile(filePath) {
							continue
						}
						if !strings.Contains(filePath, "src/pages/") {
							continue
						}

						// Skip pages with layout: 'bare' -- they intentionally don't use Layout
						componentName := extractComponentName(filePath)
						if bareLayoutPages[componentName] {
							continue
						}

						fullPath := filepath.Join(e.workspacePath, filePath)
						pageContent, err := os.ReadFile(fullPath)
						if err != nil {
							continue
						}
						pageStr := string(pageContent)
						layoutImportPattern := regexp.MustCompile(fmt.Sprintf(`import\s+.*%s`, layoutName))
						if !layoutImportPattern.MatchString(pageStr) {
							issues = append(issues, IntegrationIssue{
								File:    filePath,
								Issue:   "missing_layout",
								Details: fmt.Sprintf("Page does not import the '%s' component. All pages should use the shared layout for consistent navigation.", layoutName),
							})
						}
					}
				}
			}
		}
	}

	// Verify interactive navigation, route links, and functional handlers across pages and custom components
	if navIssues := e.verifyNavigationAndHandlers(mainContent); len(navIssues) > 0 {
		issues = append(issues, navIssues...)
	}

	// Format output
	var sb strings.Builder

	// Report skipped placeholder files first
	if len(skippedPlaceholders) > 0 {
		sb.WriteString("PLACEHOLDER FILES DETECTED (skipped verification):\n")
		for _, file := range skippedPlaceholders {
			fmt.Fprintf(&sb, "  - %s\n", file)
		}
		sb.WriteString("\nThese files appear to be placeholders or garbage files. Use the deleteFile tool to remove them.\n\n")
	}

	if len(issues) == 0 {
		if len(skippedPlaceholders) > 0 {
			// Has placeholders but no real issues - still success but recommend cleanup
			return &ToolResult{
				Success: true,
				Content: sb.String() + "All valid pages are properly integrated. Consider deleting the placeholder files above.",
			}, nil
		}
		return &ToolResult{
			Success: true,
			Content: "All pages are properly integrated! Every file is imported and rendered.",
		}, nil
	}

	// Format issues for AI
	sb.WriteString("INTEGRATION ISSUES FOUND:\n\n")
	for _, issue := range issues {
		fmt.Fprintf(&sb, "- %s [%s]\n  %s\n\n", issue.File, issue.Issue, issue.Details)
	}
	sb.WriteString("Fix these issues before telling the user they can access these pages.")

	return &ToolResult{
		Success: false,
		Content: sb.String(),
	}, nil
}

// templateRouteInfo holds routing config extracted from template.json
type templateRouteInfo struct {
	RoutesFile     string
	AvailablePages []string
}

// loadTemplateRouteConfig reads template.json and extracts routing configuration.
// Returns nil if template.json doesn't exist (graceful fallback).
func loadTemplateRouteConfig(workspacePath string) *templateRouteInfo {
	templatePath := filepath.Join(workspacePath, "template.json")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return nil
	}

	var tmpl struct {
		FileStructure struct {
			RoutesFile string `json:"routes_file"`
		} `json:"file_structure"`
		AvailablePages []struct {
			Name string `json:"name"`
		} `json:"available_pages"`
	}
	if err := json.Unmarshal(data, &tmpl); err != nil {
		return nil
	}

	info := &templateRouteInfo{
		RoutesFile: tmpl.FileStructure.RoutesFile,
	}
	for _, p := range tmpl.AvailablePages {
		info.AvailablePages = append(info.AvailablePages, p.Name)
	}
	return info
}

// extractComponentName gets the component name from a file path
// e.g., "src/pages/About.tsx" -> "About"
func extractComponentName(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

var (
	hrefAttrRe            = regexp.MustCompile(`\bhref\s*=\s*["']([^"']*)["']`)
	toAttrRe              = regexp.MustCompile(`\bto\s*=\s*["']([^"']*)["']`)
	noopOnClickRe         = regexp.MustCompile(`\bon(?:Click|Submit)\s*=\s*\{\s*(?:async\s*)?\([^)]*\)\s*=>\s*(?:\{\s*(?:console\.(?:log|warn|info)\([^)]*\);?\s*)?\}|null|undefined|console\.(?:log|warn|info)\([^)]*\))\s*\}`)
	linkToRe              = regexp.MustCompile(`<(?:Link|NavLink)\b[^>]*\bto\s*=\s*["'](/[^"'?#]*)[^"']*["']`)
	navigateCallRe        = regexp.MustCompile(`\bnavigate\(\s*["'](/[^"'?#]*)[^"']*["']`)
	windowLocNavRe        = regexp.MustCompile(`\bwindow\.location(?:\.href\s*=|\.assign\(|\.replace\()\s*["'](/[^"'?#]*)[^"']*["']`)
	routePathDeclRe       = regexp.MustCompile(`path\s*:\s*["']([^"']+)["']`)
	jsxIdAttrRe           = regexp.MustCompile(`\bid\s*=\s*(?:["']([A-Za-z][A-Za-z0-9_-]*)["']|\{\s*["']([A-Za-z][A-Za-z0-9_-]*)["']\s*\})`)
	hashHrefTargetRe      = regexp.MustCompile(`\bhref\s*=\s*["']#([A-Za-z][A-Za-z0-9_-]*)["']`)
	getElemByIdCallRe     = regexp.MustCompile(`document\.(?:getElementById\(\s*["']([A-Za-z][A-Za-z0-9_-]*)["']|querySelector\(\s*["']#([A-Za-z][A-Za-z0-9_-]*)["'])`)
	triggerWrapperRe      = regexp.MustCompile(`<(?:DialogTrigger|SheetTrigger|DropdownMenuTrigger|PopoverTrigger|TooltipTrigger|AlertDialogTrigger|AlertDialogAction|AlertDialogCancel|CollapsibleTrigger|AccordionTrigger|TabsTrigger|Link|NavLink|a)\b[^>]*>\s*$`)
	spreadPropsRe         = regexp.MustCompile(`\{\s*\.\.\.[A-Za-z_$]`)
	buttonInteractiveRe   = regexp.MustCompile(`\b(?:onClick|onMouseDown|onPointerDown|onTouchStart|asChild)\b`)
	buttonSubmitResetRe   = regexp.MustCompile(`\btype\s*=\s*(?:["'](?:submit|reset)["']|\{\s*["'](?:submit|reset)["']\s*\})`)
	buttonExplicitTypeBtn = regexp.MustCompile(`\btype\s*=\s*(?:["']button["']|\{\s*["']button["']\s*\})`)
	staticDisabledRe      = regexp.MustCompile(`\bdisabled(?:\s*=\s*\{\s*true\s*\}|\s|/|>)`)
	htmlTagStripRe        = regexp.MustCompile(`<[^>]+>`)
)

type jsxOpeningTag struct {
	name       string
	start      int
	end        int
	raw        string
	selfClosed bool
}

// scanJSXOpeningTags scans JSX opening tags for the specified tag names while properly
// skipping string literals and balanced `{ ... }` expressions (so `=>` inside handlers
// never prematurely terminates tag parsing).
func scanJSXOpeningTags(content string, targets map[string]bool) []jsxOpeningTag {
	var tags []jsxOpeningTag
	n := len(content)
	for i := 0; i < n; i++ {
		if content[i] != '<' {
			continue
		}
		if i+1 >= n {
			break
		}
		// Skip closing tags, comments, fragments
		next := content[i+1]
		if next == '/' || next == '!' || next == '>' {
			continue
		}
		// Read tag identifier
		j := i + 1
		for j < n && ((content[j] >= 'a' && content[j] <= 'z') || (content[j] >= 'A' && content[j] <= 'Z') || (content[j] >= '0' && content[j] <= '9') || content[j] == '_' || content[j] == '.') {
			j++
		}
		tagName := content[i+1 : j]
		if !targets[tagName] {
			continue
		}
		// Ensure boundary after tag name
		if j < n && content[j] != ' ' && content[j] != '\t' && content[j] != '\n' && content[j] != '\r' && content[j] != '>' && content[j] != '/' {
			continue
		}

		// Scan forward until matching '>' outside quotes and `{ ... }`
		braceDepth := 0
		inQuote := byte(0)
		k := j
		foundEnd := -1
		for k < n {
			ch := content[k]
			if inQuote != 0 {
				if ch == '\\' {
					k += 2
					continue
				}
				if ch == inQuote {
					inQuote = 0
				}
				k++
				continue
			}
			if ch == '"' || ch == '\'' || ch == '`' {
				inQuote = ch
				k++
				continue
			}
			if ch == '{' {
				braceDepth++
				k++
				continue
			}
			if ch == '}' {
				if braceDepth > 0 {
					braceDepth--
				}
				k++
				continue
			}
			if ch == '>' && braceDepth == 0 {
				foundEnd = k
				break
			}
			k++
		}
		if foundEnd == -1 {
			continue
		}
		raw := content[i : foundEnd+1]
		selfClosed := foundEnd > i && content[foundEnd-1] == '/'
		tags = append(tags, jsxOpeningTag{
			name:       tagName,
			start:      i,
			end:        foundEnd + 1,
			raw:        raw,
			selfClosed: selfClosed,
		})
		i = foundEnd
	}
	return tags
}

// isInsideOpenForm returns true if pos sits inside a <form ...> ... </form> block.
func isInsideOpenForm(content string, pos int) bool {
	prefix := content[:pos]
	lastOpen := strings.LastIndex(strings.ToLower(prefix), "<form")
	if lastOpen == -1 {
		return false
	}
	lastClose := strings.LastIndex(strings.ToLower(prefix), "</form>")
	return lastOpen > lastClose
}

// extractJSXTagLabel grabs a concise human-readable label inside <Button>Label</Button>.
func extractJSXTagLabel(content string, tag jsxOpeningTag) string {
	if tag.selfClosed || tag.end >= len(content) {
		return ""
	}
	closing := "</" + tag.name + ">"
	rest := content[tag.end:]
	if len(rest) > 300 {
		rest = rest[:300]
	}
	closeIdx := strings.Index(rest, closing)
	if closeIdx == -1 {
		return ""
	}
	inner := htmlTagStripRe.ReplaceAllString(rest[:closeIdx], " ")
	inner = strings.Join(strings.Fields(inner), " ")
	if len(inner) > 40 {
		inner = inner[:40] + "..."
	}
	return inner
}

// findDeadButtons returns labels/descriptions of <button> or <Button> elements that
// have no interactive handler (onClick, asChild, submit/reset, trigger wrapper, or spread props).
func findDeadButtons(content string) []string {
	tags := scanJSXOpeningTags(content, map[string]bool{"button": true, "Button": true})
	var dead []string
	seen := make(map[string]bool)

	for _, tag := range tags {
		// Skip if it already has an interactive handler, asChild, submit/reset type, spread props, or static disabled
		if buttonInteractiveRe.MatchString(tag.raw) ||
			buttonSubmitResetRe.MatchString(tag.raw) ||
			spreadPropsRe.MatchString(tag.raw) ||
			staticDisabledRe.MatchString(tag.raw) {
			continue
		}

		// Skip if wrapped immediately inside a Radix/shadcn trigger or router Link
		winStart := tag.start - 260
		if winStart < 0 {
			winStart = 0
		}
		before := strings.TrimSpace(content[winStart:tag.start])
		if triggerWrapperRe.MatchString(before) {
			continue
		}

		// Skip native submit buttons inside <form onSubmit={...}> unless explicitly type="button"
		if isInsideOpenForm(content, tag.start) && !buttonExplicitTypeBtn.MatchString(tag.raw) {
			continue
		}

		label := extractJSXTagLabel(content, tag)
		desc := fmt.Sprintf("<%s>", tag.name)
		if label != "" {
			desc = fmt.Sprintf(`<%s>"%s"</%s>`, tag.name, label, tag.name)
		}
		if !seen[desc] {
			seen[desc] = true
			dead = append(dead, desc)
			if len(dead) >= 5 {
				break
			}
		}
	}
	return dead
}

// findDeadForms returns true if any <form> tag lacks both onSubmit and action attributes.
func findDeadForms(content string) bool {
	tags := scanJSXOpeningTags(content, map[string]bool{"form": true})
	for _, tag := range tags {
		if !strings.Contains(tag.raw, "onSubmit") && !strings.Contains(tag.raw, "action=") && !spreadPropsRe.MatchString(tag.raw) {
			return true
		}
	}
	return false
}

func findLeadingSlashAnchorHrefs(content string) []string {
	var bad []string
	seen := make(map[string]bool)
	for _, tag := range scanJSXOpeningTags(content, map[string]bool{"a": true}) {
		m := hrefAttrRe.FindStringSubmatch(tag.raw)
		if len(m) < 2 {
			continue
		}
		href := strings.TrimSpace(m[1])
		if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
			continue
		}
		if strings.HasPrefix(href, "/api/") ||
			strings.HasPrefix(href, "/storage/") ||
			strings.HasPrefix(href, "/project/") ||
			strings.HasPrefix(href, "/files/") {
			continue
		}
		if !seen[href] {
			seen[href] = true
			bad = append(bad, href)
		}
	}
	for _, m := range windowLocNavRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		href := strings.TrimSpace(m[1])
		if strings.HasPrefix(href, "/api/") || strings.HasPrefix(href, "/storage/") || strings.HasPrefix(href, "/files/") {
			continue
		}
		if !seen[href] {
			seen[href] = true
			bad = append(bad, href)
		}
	}
	return bad
}

func findUnhandledDeadAnchors(content string) []string {
	var bad []string
	seen := make(map[string]bool)
	for _, tag := range scanJSXOpeningTags(content, map[string]bool{"a": true}) {
		m := hrefAttrRe.FindStringSubmatch(tag.raw)
		if len(m) < 2 {
			continue
		}
		href := strings.TrimSpace(m[1])
		lowerHref := strings.ToLower(href)
		isDead := href == "#" || href == "" || strings.HasPrefix(lowerHref, "javascript:")
		if !isDead {
			continue
		}
		if strings.Contains(tag.raw, "onClick") || spreadPropsRe.MatchString(tag.raw) {
			continue
		}
		if !seen[href] {
			seen[href] = true
			bad = append(bad, href)
		}
	}
	for _, tag := range scanJSXOpeningTags(content, map[string]bool{"Link": true, "NavLink": true}) {
		m := toAttrRe.FindStringSubmatch(tag.raw)
		if len(m) < 2 {
			continue
		}
		toVal := strings.TrimSpace(m[1])
		if toVal != "#" && toVal != "" {
			continue
		}
		if strings.Contains(tag.raw, "onClick") || spreadPropsRe.MatchString(tag.raw) {
			continue
		}
		if !seen["Link:empty"] {
			seen["Link:empty"] = true
			bad = append(bad, "#")
		}
	}
	return bad
}

func collectDeclaredSectionIDs(fileContents map[string]string) map[string]bool {
	ids := map[string]bool{
		"main-content": true,
		"top":          true,
		"root":         true,
	}
	for _, content := range fileContents {
		for _, m := range jsxIdAttrRe.FindAllStringSubmatch(content, -1) {
			if len(m) >= 2 && m[1] != "" {
				ids[m[1]] = true
			} else if len(m) >= 3 && m[2] != "" {
				ids[m[2]] = true
			}
		}
	}
	return ids
}

func findMissingSectionAnchors(content string, declaredIDs map[string]bool) []string {
	var missing []string
	seen := make(map[string]bool)
	checkID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || declaredIDs[id] || seen[id] {
			return
		}
		seen[id] = true
		missing = append(missing, "#"+id)
	}
	for _, tag := range scanJSXOpeningTags(content, map[string]bool{"a": true}) {
		if strings.Contains(tag.raw, "onClick") {
			continue
		}
		if m := hashHrefTargetRe.FindStringSubmatch(tag.raw); len(m) >= 2 {
			checkID(m[1])
		}
	}
	for _, m := range getElemByIdCallRe.FindAllStringSubmatch(content, -1) {
		if len(m) >= 2 && m[1] != "" {
			checkID(m[1])
		} else if len(m) >= 3 && m[2] != "" {
			checkID(m[2])
		}
	}
	return missing
}

func collectDeclaredRoutes(routesContent string) ([]string, bool) {
	matches := routePathDeclRe.FindAllStringSubmatch(routesContent, -1)
	var declared []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		p := strings.TrimSpace(m[1])
		if p == "*" {
			continue
		}
		declared = append(declared, p)
	}
	return declared, len(declared) > 0
}

func matchesDeclaredRoute(target string, declared []string) bool {
	if target == "" || target == "/" {
		return true
	}
	normTarget := strings.TrimSuffix(target, "/")
	for _, r := range declared {
		normRoute := strings.TrimSuffix(r, "/")
		if normRoute == "" {
			normRoute = "/"
		}
		if normRoute == normTarget {
			return true
		}
		routeSegs := strings.Split(strings.Trim(normRoute, "/"), "/")
		targetSegs := strings.Split(strings.Trim(normTarget, "/"), "/")
		if len(routeSegs) == len(targetSegs) {
			matched := true
			for i := range routeSegs {
				if strings.HasPrefix(routeSegs[i], ":") || routeSegs[i] == "*" {
					continue
				}
				if routeSegs[i] != targetSegs[i] {
					matched = false
					break
				}
			}
			if matched {
				return true
			}
		}
	}
	return false
}

// verifyNavigationAndHandlers scans customized pages, components, and App.tsx for dead buttons/CTAs,
// unwired forms, broken preview links, missing #sectionId targets, empty handlers, and
// <Link to="..."> / <NavLink to="..."> / navigate(...) targets missing from routes.tsx.
func (e *IntegrationExecutor) verifyNavigationAndHandlers(routesContent string) []IntegrationIssue {
	declaredRoutes, hasRoutes := collectDeclaredRoutes(routesContent)
	var issues []IntegrationIssue

	// Pass 1: Read all relevant TSX/JSX files so we can collect every declared section `id="..."`
	fileContents := make(map[string]string)
	var orderedFiles []string

	appTsxPath := filepath.Join(e.workspacePath, "src", "App.tsx")
	if raw, err := os.ReadFile(appTsxPath); err == nil {
		fileContents["src/App.tsx"] = string(raw)
		orderedFiles = append(orderedFiles, "src/App.tsx")
	}

	scanDirs := []string{"src/pages", "src/components"}
	for _, relDir := range scanDirs {
		absDir := filepath.Join(e.workspacePath, relDir)
		_ = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			relPath, relErr := filepath.Rel(e.workspacePath, path)
			if relErr != nil {
				return nil
			}
			normRel := filepath.ToSlash(relPath)
			if info.IsDir() {
				if normRel == "src/components/ui" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(normRel, ".tsx") && !strings.HasSuffix(normRel, ".jsx") {
				return nil
			}
			base := filepath.Base(normRel)
			if base == "NotFound.tsx" || base == "ProtectedRoute.tsx" || base == "EmptyState.tsx" || base == "ErrorBoundary.tsx" || base == "LoadingState.tsx" {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			fileContents[normRel] = string(raw)
			orderedFiles = append(orderedFiles, normRel)
			return nil
		})
	}

	declaredIDs := collectDeclaredSectionIDs(fileContents)

	// Pass 2: Audit every file for dead buttons/CTAs, dead forms, broken links, and missing routes/anchors
	for _, normRel := range orderedFiles {
		content := fileContents[normRel]
		if strings.Contains(content, "Welcome to Your Website") {
			continue
		}

		if badAnchors := findLeadingSlashAnchorHrefs(content); len(badAnchors) > 0 {
			issues = append(issues, IntegrationIssue{
				File:    normRel,
				Issue:   "broken_preview_link",
				Details: fmt.Sprintf("Found leading-slash link/redirect (`%s`), which escapes the app preview base path (`/preview/:id/`). Replace with React Router `<Link to=\"...\">` / `navigate('...')` for routes or `#sectionId` smooth scroll for in-page links.", strings.Join(badAnchors, ", ")),
			})
		}

		if deadAnchors := findUnhandledDeadAnchors(content); len(deadAnchors) > 0 {
			issues = append(issues, IntegrationIssue{
				File:    normRel,
				Issue:   "dead_link",
				Details: "Contains placeholder `<a href=\"#\">`, `javascript:...`, or empty `<Link to=\"\">` without an interactive handler. Wire every menu/footer link to a registered route via `<Link to=\"...\">`, an existing `#sectionId`, or a working `onClick` handler.",
			})
		}

		if missingAnchors := findMissingSectionAnchors(content, declaredIDs); len(missingAnchors) > 0 {
			issues = append(issues, IntegrationIssue{
				File:    normRel,
				Issue:   "missing_section_id",
				Details: fmt.Sprintf("In-page anchor/scroll target(s) %s do not match any element `id=\"...\"` in the app. Add the matching `id=\"...\"` attribute to the target section or point the menu link/CTA to an existing section or `<Link to=\"...\">` route.", strings.Join(missingAnchors, ", ")),
			})
		}

		if noopOnClickRe.MatchString(content) {
			issues = append(issues, IntegrationIssue{
				File:    normRel,
				Issue:   "noop_handler",
				Details: "Contains empty or console-only `onClick`/`onSubmit` handler. Wire every button, CTA, and form to real state updates, navigation, modals/dialogs, or `toast.success(...)` feedback.",
			})
		}

		if deadButtons := findDeadButtons(content); len(deadButtons) > 0 {
			issues = append(issues, IntegrationIssue{
				File:    normRel,
				Issue:   "dead_button",
				Details: fmt.Sprintf("Found non-functional button/CTA(s) without `onClick`, `type=\"submit\"`, `asChild`, or trigger wrapper: %s. Wire every button and CTA to perform a real action (route `<Link to=\"...\">` via `asChild`, `navigate(...)`, open a dialog/modal/drawer, scroll to an existing `#sectionId`, or update interactive state).", strings.Join(deadButtons, ", ")),
			})
		}

		if findDeadForms(content) {
			issues = append(issues, IntegrationIssue{
				File:    normRel,
				Issue:   "dead_form",
				Details: "Found `<form>` without an `onSubmit` handler. Add an `onSubmit={(e) => { e.preventDefault(); ... }}` handler that validates input, updates state/database, and shows `toast.success(...)` confirmation.",
			})
		}

		if hasRoutes {
			seenMissing := make(map[string]bool)
			for _, re := range []*regexp.Regexp{linkToRe, navigateCallRe} {
				for _, m := range re.FindAllStringSubmatch(content, -1) {
					if len(m) < 2 {
						continue
					}
					target := strings.TrimSpace(m[1])
					if target == "" || strings.HasPrefix(target, "/api/") || strings.HasPrefix(target, "/storage/") || strings.HasPrefix(target, "/files/") {
						continue
					}
					if !matchesDeclaredRoute(target, declaredRoutes) && !seenMissing[target] {
						seenMissing[target] = true
						issues = append(issues, IntegrationIssue{
							File:    normRel,
							Issue:   "missing_route",
							Details: fmt.Sprintf("Navigation target `%s` is not declared in `src/routes.tsx`. Either add `{ path: '%s', element: <... />, ... }` to `src/routes.tsx` or point the menu link/CTA to a valid registered route.", target, target),
						})
					}
				}
			}
		}

		if len(issues) >= 15 {
			break
		}
	}

	return issues
}
