package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyEditIntent(t *testing.T) {
	if intent := ClassifyEditIntent("Add a testimonials section below pricing"); intent.WantsLogoChange || intent.WantsFullRedesign || intent.WantsThemeChange {
		t.Fatalf("expected strict preservation for routine follow-up, got %+v", intent)
	}

	if intent := ClassifyEditIntent("Replace the logo with this uploaded image"); !intent.WantsLogoChange || intent.WantsFullRedesign {
		t.Fatalf("expected logo change only, got %+v", intent)
	}

	if intent := ClassifyEditIntent("Don't change the logo, just fix the signup button"); intent.WantsLogoChange {
		t.Fatalf("expected negation to lock logo, got %+v", intent)
	}

	if intent := ClassifyEditIntent("Redesign the pricing card"); intent.WantsFullRedesign {
		t.Fatalf("expected scoped card redesign not to trigger full redesign, got %+v", intent)
	}

	if intent := ClassifyEditIntent("Redesign the entire app from scratch"); !intent.WantsFullRedesign {
		t.Fatalf("expected full redesign to be detected, got %+v", intent)
	}
}

func TestValidateFollowUpWriteProtectsLogoThemeAndUnreadFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "components"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "src", "pages"), 0755)

	cssPath := filepath.Join(dir, "src", "custom.css")
	_ = os.WriteFile(cssPath, []byte(":root { --primary: 214 89% 52%; }\n"), 0644)

	navPath := filepath.Join(dir, "src", "components", "Navigation.tsx")
	navContent := `export function Navigation() { return <header><img src="/api/files/proj1/brand-logo.png" alt="Logo" />` +
		`<span>My Brand</span><nav><a href="#home">Home</a><a href="#about">About</a><a href="#pricing">Pricing</a></nav></header>; }` +
		`// padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding`
	_ = os.WriteFile(navPath, []byte(navContent), 0644)

	intent := ClassifyEditIntent("Add a contact button to the page")
	readFiles := map[string]bool{}

	if msg := ValidateFollowUpWrite(dir, "src/custom.css", ":root { --primary: 0 0% 0%; }", intent, readFiles); msg == "" {
		t.Fatalf("expected src/custom.css overwrite to be blocked without theme/redesign request")
	}

	if msg := ValidateFollowUpWrite(dir, "src/components/Navigation.tsx", navContent, intent, readFiles); msg == "" {
		t.Fatalf("expected unread existing file overwrite to be blocked")
	}

	readFiles["src/components/Navigation.tsx"] = true
	if msg := ValidateFollowUpWrite(dir, "src/components/Navigation.tsx", `export function Navigation() { return <header><span>New Icon</span></header>; }`, intent, readFiles); msg == "" {
		t.Fatalf("expected logo URL removal to be blocked when logo change was not requested")
	}

	if msg := ValidateFollowUpWrite(dir, "src/pages/About.tsx", "export default function About() { return <div>About</div>; }", intent, readFiles); msg != "" {
		t.Fatalf("expected brand-new file creation to be allowed, got: %s", msg)
	}
}
