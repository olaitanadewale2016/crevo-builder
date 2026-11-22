package registry

import (
	"fmt"
	"strings"
)

// ImageEntry represents a stock image with parsed metadata
type ImageEntry struct {
	Filename   string   `json:"filename"`
	Type       string   `json:"type"`       // "background" or "gallery"
	Subject    string   `json:"subject"`    // descriptive name e.g. "sand-dunes"
	Category   string   `json:"category"`   // primary category e.g. "texture" (bg) or "food-bakery" (gallery)
	Categories []string `json:"categories"` // split categories e.g. ["food", "bakery"]
	Mood       string   `json:"mood"`       // backgrounds only: warm, cool, earthy, moody, soft, bold, etc.
	Tone       string   `json:"tone"`       // light, dark, warm, cool, neutral
	Contrast   string   `json:"contrast"`   // dark-text or light-text
}

// ParseBackgroundFilename parses bg_{subject}_{type}_{mood}_{tone}_{contrast}.png
func ParseBackgroundFilename(filename string) ImageEntry {
	name := strings.TrimSuffix(filename, ".png")
	parts := strings.Split(name, "_")

	entry := ImageEntry{
		Filename: filename,
		Type:     "background",
	}

	// Expected: bg, subject, type, mood, tone, contrast
	if len(parts) >= 6 {
		entry.Subject = parts[1]
		entry.Category = parts[2]
		entry.Categories = []string{parts[2]}
		entry.Mood = parts[3]
		entry.Tone = parts[4]
		entry.Contrast = parts[5]
	} else if len(parts) >= 2 {
		entry.Subject = parts[1]
	}

	return entry
}

// ParseGalleryFilename parses gal_{subject}_{categories}_{tone}_{contrast}.png
func ParseGalleryFilename(filename string) ImageEntry {
	name := strings.TrimSuffix(filename, ".png")
	parts := strings.Split(name, "_")

	entry := ImageEntry{
		Filename: filename,
		Type:     "gallery",
	}

	// Expected: gal, subject, categories, tone, contrast
	if len(parts) >= 5 {
		entry.Subject = parts[1]
		entry.Category = parts[2]
		entry.Categories = strings.Split(parts[2], "-")
		entry.Tone = parts[3]
		entry.Contrast = parts[4]
	} else if len(parts) >= 2 {
		entry.Subject = parts[1]
	}

	return entry
}

// AllBackgroundImages holds all parsed background image entries
var AllBackgroundImages []ImageEntry

// AllGalleryImages holds all parsed gallery image entries
var AllGalleryImages []ImageEntry

func init() {
	for _, filename := range backgroundFilenames {
		AllBackgroundImages = append(AllBackgroundImages, ParseBackgroundFilename(filename))
	}
	for _, filename := range galleryFilenames {
		AllGalleryImages = append(AllGalleryImages, ParseGalleryFilename(filename))
	}
}

// SearchImages finds images matching the given filters. Empty string means "any".
// When a category/topic is specified, curated industry photography is prioritized first,
// followed by matching local gallery/background images.
func SearchImages(imageType, category, mood, tone string, limit int) []ImageEntry {
	if limit <= 0 {
		limit = 10
	}

	var results []ImageEntry
	seen := make(map[string]bool)

	if category != "" {
		curated := SearchCuratedIndustryImages(category, imageType, mood, tone, limit)
		for _, img := range curated {
			if !seen[img.Filename] {
				seen[img.Filename] = true
				results = append(results, img)
			}
		}
	}

	if len(results) >= limit {
		return results[:limit]
	}

	var pool []ImageEntry
	switch imageType {
	case "background":
		pool = AllBackgroundImages
	case "gallery":
		pool = AllGalleryImages
	default:
		pool = append(append([]ImageEntry{}, AllBackgroundImages...), AllGalleryImages...)
	}

	for _, img := range pool {
		if seen[img.Filename] {
			continue
		}
		if category != "" && !matchesCategory(img, category) {
			continue
		}
		if mood != "" && img.Mood != mood {
			continue
		}
		if tone != "" && img.Tone != tone {
			continue
		}
		seen[img.Filename] = true
		results = append(results, img)
		if len(results) >= limit {
			break
		}
	}

	// If no category filter was provided and slots remain, include curated photography too
	if len(results) < limit && category == "" {
		curated := SearchCuratedIndustryImages("", imageType, mood, tone, limit-len(results))
		for _, img := range curated {
			if !seen[img.Filename] {
				seen[img.Filename] = true
				results = append(results, img)
				if len(results) >= limit {
					break
				}
			}
		}
	}

	return results
}

func matchesCategory(img ImageEntry, category string) bool {
	if img.Category == category {
		return true
	}
	for _, cat := range img.Categories {
		if cat == category {
			return true
		}
	}
	return false
}

// FormatImageURL constructs a root-relative or CDN URL so images load across /preview, /app, and published tabs
func FormatImageURL(img ImageEntry, laravelURL string) string {
	if strings.HasPrefix(img.Filename, "http://") || strings.HasPrefix(img.Filename, "https://") {
		return img.Filename
	}
	if strings.HasPrefix(img.Filename, "/") {
		return img.Filename
	}
	subdir := "backgrounds"
	if img.Type == "gallery" {
		subdir = "gallery"
	}
	return fmt.Sprintf("/storage/image-library/%s/%s", subdir, img.Filename)
}

// FormatImageList formats a slice of images for display to the AI
func FormatImageList(images []ImageEntry, laravelURL string) string {
	if len(images) == 0 {
		return "No matching images found."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d images:\n\n", len(images))

	for i, img := range images {
		fmt.Fprintf(&sb, "%d. **%s** (%s)\n", i+1, img.Subject, img.Type)
		fmt.Fprintf(&sb, "   Category: %s | Tone: %s | Contrast: %s",
			img.Category, img.Tone, img.Contrast)
		if img.Mood != "" {
			fmt.Fprintf(&sb, " | Mood: %s", img.Mood)
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "   URL: %s\n\n", FormatImageURL(img, laravelURL))
	}

	return sb.String()
}

// FormatImageUsage returns usage guidelines for a specific image
func FormatImageUsage(img ImageEntry, laravelURL string) string {
	url := FormatImageURL(img, laravelURL)
	var sb strings.Builder

	fmt.Fprintf(&sb, "## %s (%s)\n\n", img.Subject, img.Type)
	fmt.Fprintf(&sb, "URL: `%s`\n\n", url)

	if img.Type == "background" {
		sb.WriteString("### Recommended Usage\n")
		sb.WriteString("Use as a **section background** with gradient overlay for text readability:\n\n")
		sb.WriteString("```tsx\n")
		fmt.Fprintf(&sb, `<section className="relative min-h-[60vh] bg-cover bg-center" style={{ backgroundImage: 'url(%s)' }}>`, url)
		sb.WriteString("\n")
		sb.WriteString(`  <div className="absolute inset-0 bg-black/40" />`)
		sb.WriteString("\n")
		sb.WriteString(`  <div className="relative z-10 container mx-auto px-6 py-20">`)
		sb.WriteString("\n")
		fmt.Fprintf(&sb, `    <h1 className="%s">Your Heading</h1>`, textColorForContrast(img.Contrast))
		sb.WriteString("\n")
		sb.WriteString("  </div>\n</section>\n```\n\n")
	} else {
		sb.WriteString("### Recommended Usage\n")
		sb.WriteString("Use as a **card image** or **section image**:\n\n")
		sb.WriteString("```tsx\n")
		fmt.Fprintf(&sb, `<img src="%s" alt="%s" className="w-full h-64 object-cover rounded-lg" />`, url, humanizeSubject(img.Subject))
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("### Metadata\n")
	fmt.Fprintf(&sb, "- **Alt text suggestion**: \"%s\"\n", humanizeSubject(img.Subject))
	fmt.Fprintf(&sb, "- **Tone**: %s (pair with %s backgrounds)\n", img.Tone, img.Tone)
	fmt.Fprintf(&sb, "- **Text overlay**: Use %s text color\n", textColorForContrast(img.Contrast))
	if img.Mood != "" {
		fmt.Fprintf(&sb, "- **Mood**: %s\n", img.Mood)
	}

	return sb.String()
}

func textColorForContrast(contrast string) string {
	if contrast == "light-text" {
		return "text-white"
	}
	return "text-gray-900"
}

func humanizeSubject(subject string) string {
	return strings.ReplaceAll(subject, "-", " ")
}

// FindImageByFilename looks up an image by exact filename
func FindImageByFilename(filename string) *ImageEntry {
	for _, img := range AllBackgroundImages {
		if img.Filename == filename {
			return &img
		}
	}
	for _, img := range AllGalleryImages {
		if img.Filename == filename {
			return &img
		}
	}
	for _, img := range AllCuratedIndustryImages {
		if img.Filename == filename {
			return &img
		}
	}
	if strings.HasPrefix(filename, "http://") || strings.HasPrefix(filename, "https://") {
		entry := ImageEntry{
			Filename: filename,
			Type:     "gallery",
			Subject:  "custom-photography",
			Category: "photography",
			Tone:     "light",
			Contrast: "dark-text",
		}
		return &entry
	}
	return nil
}
