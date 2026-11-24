package registry

import (
	_ "embed"
	"encoding/json"
)

//go:embed extra_industry_images.json
var extraIndustryImagesJSON []byte

func init() {
	var extra []ImageEntry
	if err := json.Unmarshal(extraIndustryImagesJSON, &extra); err != nil {
		return
	}

	seen := make(map[string]bool, len(AllCuratedIndustryImages)+len(extra))
	for _, img := range AllCuratedIndustryImages {
		if img.Filename != "" {
			seen[img.Filename] = true
		}
	}

	for _, img := range extra {
		if img.Filename == "" || seen[img.Filename] {
			continue
		}
		seen[img.Filename] = true
		AllCuratedIndustryImages = append(AllCuratedIndustryImages, img)
	}
}
