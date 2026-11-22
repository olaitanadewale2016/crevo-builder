package registry

import "strings"

// AllCuratedIndustryImages contains verified high-resolution multi-industry photography
var AllCuratedIndustryImages = []ImageEntry{
	// DENTISTRY & HEALTHCARE
	{
		Filename:   "https://images.unsplash.com/photo-1588776814546-1ffcf47267a5?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "modern-dental-clinic-room",
		Category:   "dentist",
		Categories: []string{"dentist", "dental", "teeth", "healthcare", "clinic", "medical", "doctor"},
		Tone:       "light",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1629909613654-28e377c37b09?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "dentist-patient-consultation",
		Category:   "dentist",
		Categories: []string{"dentist", "dental", "smile", "healthcare", "doctor", "clinic"},
		Tone:       "cool",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1519494026892-80bbd2d6fd0d?auto=format&fit=crop&w=1600&q=80",
		Type:       "background",
		Subject:    "modern-medical-dental-reception",
		Category:   "dentist",
		Categories: []string{"dentist", "dental", "clinic", "hospital", "healthcare"},
		Mood:       "soft",
		Tone:       "light",
		Contrast:   "dark-text",
	},

	// LAW, LEGAL & ACCOUNTING
	{
		Filename:   "https://images.unsplash.com/photo-1589829545856-d10d557cf95f?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "justice-scales-law-books",
		Category:   "law",
		Categories: []string{"law", "legal", "lawyer", "attorney", "justice", "court"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1507679799987-c73779587ccf?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "executive-lawyer-consultation",
		Category:   "law",
		Categories: []string{"law", "lawyer", "corporate", "finance", "consulting", "advisory"},
		Tone:       "dark",
		Contrast:   "light-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1486406146926-c627a92ad1ab?auto=format&fit=crop&w=1600&q=80",
		Type:       "background",
		Subject:    "glass-corporate-skyscraper",
		Category:   "law",
		Categories: []string{"law", "legal", "corporate", "finance", "business"},
		Mood:       "moody",
		Tone:       "cool",
		Contrast:   "light-text",
	},

	// FITNESS, GYM & YOGA
	{
		Filename:   "https://images.unsplash.com/photo-1534438327276-14e5300c3a48?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "gym-weights-training-floor",
		Category:   "fitness",
		Categories: []string{"fitness", "gym", "workout", "weights", "training", "crossfit", "sports"},
		Tone:       "dark",
		Contrast:   "light-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1517838277536-f5f99be501cd?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "personal-trainer-workout",
		Category:   "fitness",
		Categories: []string{"fitness", "gym", "trainer", "exercise", "wellness"},
		Tone:       "cool",
		Contrast:   "light-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1545205597-3d9d02c29597?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "yoga-studio-meditation",
		Category:   "fitness",
		Categories: []string{"fitness", "yoga", "meditation", "wellness", "pilates", "zen"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1571902943202-507ec2618e8f?auto=format&fit=crop&w=1600&q=80",
		Type:       "background",
		Subject:    "gym-studio-lighting-atmosphere",
		Category:   "fitness",
		Categories: []string{"fitness", "gym", "workout", "sports"},
		Mood:       "bold",
		Tone:       "dark",
		Contrast:   "light-text",
	},

	// AUTOMOTIVE & MECHANICS
	{
		Filename:   "https://images.unsplash.com/photo-1486006920555-c77dce18193b?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "mechanic-engine-repair",
		Category:   "automotive",
		Categories: []string{"automotive", "mechanic", "car", "repair", "garage", "service", "auto"},
		Tone:       "dark",
		Contrast:   "light-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1617814076367-b759c7d7e738?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "car-detailing-polishing",
		Category:   "automotive",
		Categories: []string{"automotive", "car", "detailing", "dealership", "wash", "luxury"},
		Tone:       "cool",
		Contrast:   "light-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1503376780353-7e6692767b70?auto=format&fit=crop&w=1600&q=80",
		Type:       "background",
		Subject:    "sleek-supercar-road",
		Category:   "automotive",
		Categories: []string{"automotive", "car", "speed", "dealership"},
		Mood:       "bold",
		Tone:       "dark",
		Contrast:   "light-text",
	},

	// TECH, SAAS & AI
	{
		Filename:   "https://images.unsplash.com/photo-1551288049-bebda4e38f71?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "analytics-dashboard-display",
		Category:   "technology",
		Categories: []string{"technology", "tech", "saas", "software", "analytics", "dashboard", "ai"},
		Tone:       "light",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1498050108023-c5249f4df085?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "software-developer-workspace",
		Category:   "technology",
		Categories: []string{"technology", "code", "software", "programming", "startup", "developer"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1518770660439-4636190af475?auto=format&fit=crop&w=1600&q=80",
		Type:       "background",
		Subject:    "glowing-circuit-board-microchip",
		Category:   "technology",
		Categories: []string{"technology", "ai", "cloud", "cybersecurity", "future"},
		Mood:       "moody",
		Tone:       "cool",
		Contrast:   "light-text",
	},

	// PETS & VETERINARY
	{
		Filename:   "https://images.unsplash.com/photo-1583511655857-d19b40a7a54e?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "friendly-golden-retriever",
		Category:   "pets",
		Categories: []string{"pets", "pet", "dog", "veterinary", "vet", "animals", "grooming"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1628009368231-7bb7cfcb0def?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "veterinarian-puppy-exam",
		Category:   "pets",
		Categories: []string{"pets", "vet", "clinic", "puppy", "veterinary", "care"},
		Tone:       "light",
		Contrast:   "dark-text",
	},

	// REAL ESTATE & ARCHITECTURE
	{
		Filename:   "https://images.unsplash.com/photo-1600585154340-be6161a56a0c?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "luxury-modern-villa-architecture",
		Category:   "real-estate",
		Categories: []string{"real-estate", "house", "villa", "property", "architecture", "realtor", "home"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1618221195710-dd6b41faaea6?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "minimalist-living-room-interior",
		Category:   "real-estate",
		Categories: []string{"real-estate", "interior", "furniture", "decor", "apartment", "architecture"},
		Tone:       "light",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1512917774080-9991f1c4c750?auto=format&fit=crop&w=1600&q=80",
		Type:       "background",
		Subject:    "luxury-residence-exterior-bg",
		Category:   "real-estate",
		Categories: []string{"real-estate", "home", "property"},
		Mood:       "warm",
		Tone:       "light",
		Contrast:   "dark-text",
	},

	// PLUMBING & TRADES
	{
		Filename:   "https://images.unsplash.com/photo-1581244277943-fe4a9c777189?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "plumber-tools-pipe-repair",
		Category:   "plumbing",
		Categories: []string{"plumbing", "plumber", "repair", "pipes", "hvac", "contractor", "trades"},
		Tone:       "cool",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1541888946425-d0fbb1861564?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "construction-engineer-site",
		Category:   "construction",
		Categories: []string{"construction", "builder", "contractor", "electrician", "roofing"},
		Tone:       "light",
		Contrast:   "dark-text",
	},

	// BEAUTY, HAIR SALON & SPA
	{
		Filename:   "https://images.unsplash.com/photo-1560066984-138dadb4c035?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "modern-hair-salon-styling",
		Category:   "beauty",
		Categories: []string{"beauty", "hair", "salon", "barber", "hairdresser", "styling"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1540555700478-4be289fbecef?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "luxury-spa-hot-stones",
		Category:   "spa",
		Categories: []string{"spa", "wellness", "massage", "relaxation", "skincare", "beauty"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},

	// RESTAURANTS & FOOD
	{
		Filename:   "https://images.unsplash.com/photo-1550966871-3ed3cdb5ed0c?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "gourmet-chef-plating",
		Category:   "restaurant",
		Categories: []string{"restaurant", "dining", "food", "chef", "gourmet", "dinner"},
		Tone:       "dark",
		Contrast:   "light-text",
	},
	{
		Filename:   "https://images.unsplash.com/photo-1513104890138-7c749659a591?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "woodfired-neapolitan-pizza",
		Category:   "restaurant",
		Categories: []string{"pizza", "italian", "restaurant", "food", "pizzeria"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},

	// CLEANING SERVICES
	{
		Filename:   "https://images.unsplash.com/photo-1581578731548-c64695cc6952?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "eco-cleaning-supplies-kitchen",
		Category:   "cleaning",
		Categories: []string{"cleaning", "maid", "housekeeping", "clean", "janitorial"},
		Tone:       "light",
		Contrast:   "dark-text",
	},

	// EDUCATION & TUTORING
	{
		Filename:   "https://images.unsplash.com/photo-1524995997946-a1c2e315a42f?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "university-library-books",
		Category:   "education",
		Categories: []string{"education", "school", "university", "library", "books", "study"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},

	// PHOTOGRAPHY & CREATIVE
	{
		Filename:   "https://images.unsplash.com/photo-1516035069371-29a1b244cc32?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "mirrorless-camera-lens",
		Category:   "photography",
		Categories: []string{"photography", "camera", "photo", "creative", "video"},
		Tone:       "dark",
		Contrast:   "light-text",
	},

	// TRAVEL & HOTELS
	{
		Filename:   "https://images.unsplash.com/photo-1571896349842-33c89424de2d?auto=format&fit=crop&w=1200&q=80",
		Type:       "gallery",
		Subject:    "tropical-resort-swimming-pool",
		Category:   "travel",
		Categories: []string{"travel", "hotel", "resort", "vacation", "tourism", "holiday"},
		Tone:       "warm",
		Contrast:   "dark-text",
	},
}

// SearchCuratedIndustryImages searches the expanded multi-industry catalog
func SearchCuratedIndustryImages(category, imageType, mood, tone string, limit int) []ImageEntry {
	if limit <= 0 {
		limit = 10
	}

	searchKey := strings.ToLower(strings.TrimSpace(category))
	var results []ImageEntry

	for _, img := range AllCuratedIndustryImages {
		if imageType != "" && img.Type != imageType {
			continue
		}
		if mood != "" && img.Mood != mood {
			continue
		}
		if tone != "" && img.Tone != tone {
			continue
		}

		if searchKey != "" {
			matched := false
			if strings.Contains(strings.ToLower(img.Category), searchKey) {
				matched = true
			}
			for _, cat := range img.Categories {
				if strings.Contains(strings.ToLower(cat), searchKey) || strings.Contains(searchKey, strings.ToLower(cat)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		results = append(results, img)
		if len(results) >= limit {
			break
		}
	}

	return results
}
