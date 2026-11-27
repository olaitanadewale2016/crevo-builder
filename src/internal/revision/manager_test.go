package revision

import (
	"os"
	"path/filepath"
	"testing"
)

func writeWorkspaceFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", relPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func readWorkspaceFile(t *testing.T, root, relPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	return string(data)
}

func TestManagerUndoRedoAndRestore(t *testing.T) {
	ws := t.TempDir()
	writeWorkspaceFile(t, ws, "src/App.tsx", "export default function App() { return 'v1'; }")

	mgr := NewManager(ws)
	if err := mgr.EnsureBaselineSnapshot("Initial baseline"); err != nil {
		t.Fatalf("EnsureBaselineSnapshot: %v", err)
	}

	writeWorkspaceFile(t, ws, "src/App.tsx", "export default function App() { return 'v2'; }")
	writeWorkspaceFile(t, ws, "src/Extra.tsx", "export const extra = 2;")
	changed, err := mgr.CreateSnapshotIfChanged("After: update v2")
	if err != nil || !changed {
		t.Fatalf("CreateSnapshotIfChanged v2: changed=%v err=%v", changed, err)
	}

	writeWorkspaceFile(t, ws, "src/App.tsx", "export default function App() { return 'v3'; }")
	changed, err = mgr.CreateSnapshotIfChanged("Edit: App.tsx")
	if err != nil || !changed {
		t.Fatalf("CreateSnapshotIfChanged v3: changed=%v err=%v", changed, err)
	}

	revs, ptr := mgr.List()
	if len(revs) != 3 || ptr != 2 {
		t.Fatalf("expected 3 revisions at ptr 2, got len=%d ptr=%d", len(revs), ptr)
	}

	if _, err := mgr.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := readWorkspaceFile(t, ws, "src/App.tsx"); got != "export default function App() { return 'v2'; }" {
		t.Fatalf("expected v2 after undo, got %q", got)
	}

	if _, err := mgr.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := readWorkspaceFile(t, ws, "src/App.tsx"); got != "export default function App() { return 'v3'; }" {
		t.Fatalf("expected v3 after redo, got %q", got)
	}

	info, err := mgr.Restore(revs[0].ID)
	if err != nil {
		t.Fatalf("Restore baseline: %v", err)
	}
	if info.ID != revs[0].ID {
		t.Fatalf("expected restored id %d, got %d", revs[0].ID, info.ID)
	}
	if got := readWorkspaceFile(t, ws, "src/App.tsx"); got != "export default function App() { return 'v1'; }" {
		t.Fatalf("expected v1 after restore, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws, "src", "Extra.tsx")); !os.IsNotExist(err) {
		t.Fatalf("expected Extra.tsx to be removed when restoring v1, stat err=%v", err)
	}

	reloaded := NewManager(ws)
	_, reloadedTotal, canUndo, canRedo := reloaded.Status()
	if reloadedTotal != 3 || canUndo || !canRedo {
		t.Fatalf("unexpected reloaded status: total=%d canUndo=%v canRedo=%v", reloadedTotal, canUndo, canRedo)
	}
}
