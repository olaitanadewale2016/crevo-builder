package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindDeadButtonsCatchesUnwiredCTAsAndSkipsFunctionalControls(t *testing.T) {
	tsx := `
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/button';
import { DialogTrigger } from '@/components/ui/dialog';

export default function Home() {
  return (
    <div>
      <Button size="lg">Get Started</Button>
      <button className="px-4 py-2">Book a Demo</button>
      <Button onClick={() => setOpen(true)}>Working Modal</Button>
      <Button asChild><Link to="/pricing">Go Pricing</Link></Button>
      <DialogTrigger asChild><Button variant="outline">Open Dialog</Button></DialogTrigger>
      <form onSubmit={(e) => e.preventDefault()}>
        <Button type="submit">Submit Form</Button>
        <button>Implicit Submit</button>
      </form>
      <Button disabled>Coming Soon</Button>
    </div>
  );
}
`
	dead := findDeadButtons(tsx)
	if len(dead) != 2 {
		t.Fatalf("expected exactly 2 dead buttons (Get Started & Book a Demo), got %d: %v", len(dead), dead)
	}
	joined := strings.Join(dead, " | ")
	if !strings.Contains(joined, "Get Started") || !strings.Contains(joined, "Book a Demo") {
		t.Fatalf("expected Get Started and Book a Demo to be flagged, got: %v", dead)
	}
}

func TestVerifyIntegrationCatchesDeadCTAsMissingAnchorsAndDeadForms(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "pages"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "src", "components"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "src", "routes.tsx"), []byte(`
import Home from './pages/Home';
export const routes = [{ path: '/', label: 'Home', element: <Home />, showInNav: true, layout: 'default' }];
`), 0644)

	_ = os.WriteFile(filepath.Join(dir, "src", "pages", "Home.tsx"), []byte(`
import { Button } from '@/components/ui/button';
export default function Home() {
  return (
    <div>
      <nav>
        <a href="#pricing">Pricing</a>
        <a href="#">Dead Link</a>
      </nav>
      <section id="hero">
        <Button>Start Free Trial</Button>
      </section>
      <form>
        <input type="email" />
        <button type="submit">Join</button>
      </form>
    </div>
  );
}
`), 0644)

	exec := NewIntegrationExecutor(dir)
	res, err := exec.VerifyIntegration(context.Background(), map[string]interface{}{
		"files": []string{"src/pages/Home.tsx"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatalf("expected VerifyIntegration to fail on dead CTA, missing #pricing id, dead link, and dead form")
	}
	for _, code := range []string{"dead_button", "missing_section_id", "dead_link", "dead_form"} {
		if !strings.Contains(res.Content, code) {
			t.Fatalf("expected issue %q in report, got:\n%s", code, res.Content)
		}
	}
}
