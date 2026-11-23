package executor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// shadcnColorVars lists all CSS custom properties wrapped in hsl(var(--...)) by src/index.css @theme inline.
// Writing hex (#...), rgb(...), or hsl(...) directly into these variables produces invalid CSS like hsl(#4f46e5).
var shadcnColorVars = map[string]bool{
	"background":                 true,
	"foreground":                 true,
	"card":                       true,
	"card-foreground":            true,
	"popover":                    true,
	"popover-foreground":         true,
	"primary":                    true,
	"primary-foreground":         true,
	"secondary":                  true,
	"secondary-foreground":       true,
	"muted":                      true,
	"muted-foreground":           true,
	"accent":                     true,
	"accent-foreground":          true,
	"destructive":                true,
	"destructive-foreground":     true,
	"border":                     true,
	"input":                      true,
	"ring":                       true,
	"chart-1":                    true,
	"chart-2":                    true,
	"chart-3":                    true,
	"chart-4":                    true,
	"chart-5":                    true,
	"sidebar":                    true,
	"sidebar-foreground":         true,
	"sidebar-primary":            true,
	"sidebar-primary-foreground": true,
	"sidebar-accent":             true,
	"sidebar-accent-foreground":  true,
	"sidebar-border":             true,
	"sidebar-ring":               true,
}

var (
	googleFontImportRe = regexp.MustCompile(`(?im)^\s*@import\s+(?:url\(\s*)?["']?(https://fonts\.googleapis\.com/[^"'\)\s;]+)["']?\s*\)?\s*;\s*$`)
	cssVarDeclRe       = regexp.MustCompile(`(?i)(--([a-z0-9-]+)\s*:\s*)([^;{}]+)(;)`)
	hslFuncRe          = regexp.MustCompile(`(?i)^hsla?\(\s*([^)]+)\s*\)$`)
	rgbFuncRe          = regexp.MustCompile(`(?i)^rgba?\(\s*([^)]+)\s*\)$`)
	rootOrDarkBlockRe  = regexp.MustCompile(`(?s)((?::root|\.dark)\s*\{)([^}]*)(\})`)
)

func isCustomCSSPath(path string) bool {
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	return clean == "src/custom.css" || strings.HasSuffix(clean, "/custom.css")
}

// sanitizeCustomCSS ensures AI-written src/custom.css works seamlessly with Tailwind v4 @theme inline:
// 1. Hoists Google Fonts @import URLs into workspace index.html as <link rel="stylesheet"> so Vite bundling never drops them.
// 2. Converts hex (#...), rgb(...), and hsl(...) values on shadcn color variables into raw HSL triplets (e.g., "239 84% 67%").
func sanitizeCustomCSS(content string, workspacePath string) (string, int) {
	fixes := 0

	// 1. Hoist Google Fonts @import lines into index.html <head>
	matches := googleFontImportRe.FindAllStringSubmatch(content, -1)
	if len(matches) > 0 {
		var urls []string
		for _, m := range matches {
			if len(m) >= 2 && strings.HasPrefix(m[1], "https://fonts.googleapis.com/") {
				urls = append(urls, m[1])
			}
		}
		if len(urls) > 0 {
			injectGoogleFontsIntoIndexHTML(workspacePath, urls)
			content = googleFontImportRe.ReplaceAllString(content, "")
			content = strings.TrimLeft(content, "\r\n")
			fixes += len(urls)
		}
	}

	// 2. Normalize invalid color formats on shadcn HSL-triplet custom properties
	updated := cssVarDeclRe.ReplaceAllStringFunc(content, func(decl string) string {
		parts := cssVarDeclRe.FindStringSubmatch(decl)
		if len(parts) < 5 {
			return decl
		}
		prefix, varName, rawVal, suffix := parts[1], strings.ToLower(parts[2]), parts[3], parts[4]
		if !shadcnColorVars[varName] {
			return decl
		}
		normalized, changed := normalizeCSSColorToHSLTriplet(rawVal)
		if !changed {
			return decl
		}
		fixes++
		return prefix + normalized + suffix
	})

	return updated, fixes
}

// StripThemeColorOverridesFromCustomCSS removes conflicting shadcn color variable overrides
// inside :root and .dark blocks of src/custom.css when a user explicitly applies a Theme Preset
// via the Design tab, while preserving typography (--font-*), animations, and custom classes.
func StripThemeColorOverridesFromCustomCSS(workspacePath string) {
	customPath := filepath.Join(workspacePath, "src", "custom.css")
	data, err := os.ReadFile(customPath)
	if err != nil {
		return
	}
	original := string(data)
	updated := rootOrDarkBlockRe.ReplaceAllStringFunc(original, func(block string) string {
		parts := rootOrDarkBlockRe.FindStringSubmatch(block)
		if len(parts) < 4 {
			return block
		}
		open, body, closeBrace := parts[1], parts[2], parts[3]
		cleanedBody := cssVarDeclRe.ReplaceAllStringFunc(body, func(decl string) string {
			d := cssVarDeclRe.FindStringSubmatch(decl)
			if len(d) >= 3 && shadcnColorVars[strings.ToLower(d[2])] {
				return ""
			}
			return decl
		})
		return open + cleanedBody + closeBrace
	})
	if updated != original {
		_ = os.WriteFile(customPath, []byte(updated), 0644)
	}
}

func injectGoogleFontsIntoIndexHTML(workspacePath string, fontURLs []string) {
	indexPath := filepath.Join(workspacePath, "index.html")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return
	}
	html := string(data)
	var tags []string

	if !strings.Contains(html, "https://fonts.googleapis.com") {
		tags = append(tags, `    <link rel="preconnect" href="https://fonts.googleapis.com" />`)
	}
	if !strings.Contains(html, "https://fonts.gstatic.com") {
		tags = append(tags, `    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin />`)
	}
	for _, u := range fontURLs {
		if strings.Contains(html, u) {
			continue
		}
		safeURL := strings.ReplaceAll(u, `"`, "")
		tags = append(tags, fmt.Sprintf(`    <link href="%s" rel="stylesheet" />`, safeURL))
	}
	if len(tags) == 0 {
		return
	}
	inject := strings.Join(tags, "\n") + "\n  </head>"
	if idx := strings.Index(strings.ToLower(html), "</head>"); idx != -1 {
		newHTML := html[:idx] + inject + html[idx+len("</head>"):]
		_ = os.WriteFile(indexPath, []byte(newHTML), 0644)
	}
}

func normalizeCSSColorToHSLTriplet(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw, false
	}

	important := ""
	lower := strings.ToLower(trimmed)
	if strings.HasSuffix(lower, "!important") {
		important = " !important"
		trimmed = strings.TrimSpace(trimmed[:len(trimmed)-len("!important")])
	}

	// Hex color (#RGB, #RGBA, #RRGGBB, #RRGGBBAA)
	if strings.HasPrefix(trimmed, "#") {
		if triplet, ok := hexToHSLTriplet(trimmed); ok {
			return triplet + important, true
		}
		return raw, false
	}

	// hsl(...) or hsla(...) wrapper -> unwrap to raw HSL triplet
	if m := hslFuncRe.FindStringSubmatch(trimmed); len(m) == 2 {
		if triplet, ok := unwrapHSLFunc(m[1]); ok {
			return triplet + important, true
		}
		return raw, false
	}

	// rgb(...) or rgba(...) -> convert to raw HSL triplet
	if m := rgbFuncRe.FindStringSubmatch(trimmed); len(m) == 2 {
		if triplet, ok := rgbFuncToHSLTriplet(m[1]); ok {
			return triplet + important, true
		}
		return raw, false
	}

	return raw, false
}

func hexToHSLTriplet(hexStr string) (string, bool) {
	h := strings.TrimPrefix(strings.TrimSpace(hexStr), "#")
	var r, g, b uint64
	var alpha float64 = 1.0
	var err error

	switch len(h) {
	case 3, 4:
		r, err = strconv.ParseUint(strings.Repeat(string(h[0]), 2), 16, 8)
		if err != nil {
			return "", false
		}
		g, err = strconv.ParseUint(strings.Repeat(string(h[1]), 2), 16, 8)
		if err != nil {
			return "", false
		}
		b, err = strconv.ParseUint(strings.Repeat(string(h[2]), 2), 16, 8)
		if err != nil {
			return "", false
		}
		if len(h) == 4 {
			a, aErr := strconv.ParseUint(strings.Repeat(string(h[3]), 2), 16, 8)
			if aErr != nil {
				return "", false
			}
			alpha = float64(a) / 255.0
		}
	case 6, 8:
		r, err = strconv.ParseUint(h[0:2], 16, 8)
		if err != nil {
			return "", false
		}
		g, err = strconv.ParseUint(h[2:4], 16, 8)
		if err != nil {
			return "", false
		}
		b, err = strconv.ParseUint(h[4:6], 16, 8)
		if err != nil {
			return "", false
		}
		if len(h) == 8 {
			a, aErr := strconv.ParseUint(h[6:8], 16, 8)
			if aErr != nil {
				return "", false
			}
			alpha = float64(a) / 255.0
		}
	default:
		return "", false
	}

	return rgbToHSLTriplet(float64(r), float64(g), float64(b), alpha), true
}

func unwrapHSLFunc(inner string) (string, bool) {
	// Support both comma syntax "240, 80%, 60%, 0.5" and space/slash syntax "240 80% 60% / 50%"
	cleaned := strings.ReplaceAll(inner, ",", " ")
	var alphaPart string
	if slashIdx := strings.Index(cleaned, "/"); slashIdx != -1 {
		alphaPart = strings.TrimSpace(cleaned[slashIdx+1:])
		cleaned = cleaned[:slashIdx]
	}
	fields := strings.Fields(cleaned)
	if len(fields) < 3 {
		return "", false
	}
	hStr := strings.TrimSuffix(strings.ToLower(fields[0]), "deg")
	sStr := fields[1]
	lStr := fields[2]
	if !strings.HasSuffix(sStr, "%") {
		sStr += "%"
	}
	if !strings.HasSuffix(lStr, "%") {
		lStr += "%"
	}
	if alphaPart == "" && len(fields) >= 4 {
		alphaPart = fields[3]
	}
	base := fmt.Sprintf("%s %s %s", hStr, sStr, lStr)
	if alphaPart != "" && alphaPart != "1" && alphaPart != "1.0" && alphaPart != "100%" {
		return base + " / " + alphaPart, true
	}
	return base, true
}

func rgbFuncToHSLTriplet(inner string) (string, bool) {
	cleaned := strings.ReplaceAll(inner, ",", " ")
	var alphaStr string
	if slashIdx := strings.Index(cleaned, "/"); slashIdx != -1 {
		alphaStr = strings.TrimSpace(cleaned[slashIdx+1:])
		cleaned = cleaned[:slashIdx]
	}
	fields := strings.Fields(cleaned)
	if len(fields) < 3 {
		return "", false
	}
	r, ok1 := parseRGBChannel(fields[0])
	g, ok2 := parseRGBChannel(fields[1])
	b, ok3 := parseRGBChannel(fields[2])
	if !ok1 || !ok2 || !ok3 {
		return "", false
	}
	if alphaStr == "" && len(fields) >= 4 {
		alphaStr = fields[3]
	}
	alpha := 1.0
	if alphaStr != "" {
		if strings.HasSuffix(alphaStr, "%") {
			if p, err := strconv.ParseFloat(strings.TrimSuffix(alphaStr, "%"), 64); err == nil {
				alpha = p / 100.0
			}
		} else if a, err := strconv.ParseFloat(alphaStr, 64); err == nil {
			alpha = a
		}
	}
	return rgbToHSLTriplet(r, g, b, alpha), true
}

func parseRGBChannel(tok string) (float64, bool) {
	if strings.HasSuffix(tok, "%") {
		p, err := strconv.ParseFloat(strings.TrimSuffix(tok, "%"), 64)
		if err != nil {
			return 0, false
		}
		return (p / 100.0) * 255.0, true
	}
	v, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func rgbToHSLTriplet(r255, g255, b255, alpha float64) string {
	r := math.Max(0, math.Min(1, r255/255.0))
	g := math.Max(0, math.Min(1, g255/255.0))
	b := math.Max(0, math.Min(1, b255/255.0))

	maxC := math.Max(r, math.Max(g, b))
	minC := math.Min(r, math.Min(g, b))
	delta := maxC - minC

	l := (maxC + minC) / 2.0
	var h, s float64

	if delta != 0 {
		if l < 0.5 {
			s = delta / (maxC + minC)
		} else {
			s = delta / (2.0 - maxC - minC)
		}
		switch maxC {
		case r:
			h = (g - b) / delta
			if g < b {
				h += 6
			}
		case g:
			h = (b-r)/delta + 2
		case b:
			h = (r-g)/delta + 4
		}
		h *= 60.0
	}

	hOut := math.Round(h*10) / 10
	sOut := math.Round(s*1000) / 10
	lOut := math.Round(l*1000) / 10

	base := fmt.Sprintf("%g %g%% %g%%", hOut, sOut, lOut)
	if alpha >= 0 && alpha < 0.999 {
		aPct := math.Round(alpha*1000) / 10
		return fmt.Sprintf("%s / %g%%", base, aPct)
	}
	return base
}
