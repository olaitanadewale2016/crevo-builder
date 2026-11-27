package revision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileSnapshot captures a single file's state
type FileSnapshot struct {
	Path    string `json:"path"`
	Content []byte `json:"content,omitempty"`
	Size    int64  `json:"size"`
	Exists  bool   `json:"exists"`
}

// Revision represents a workspace state at a point in time
type Revision struct {
	ID        int            `json:"id"`
	Label     string         `json:"label"`
	Files     []FileSnapshot `json:"-"`
	FileCount int            `json:"file_count"`
	Timestamp time.Time      `json:"timestamp"`
}

// RevisionInfo is the API-safe representation (no file content)
type RevisionInfo struct {
	ID        int       `json:"id"`
	Label     string    `json:"label"`
	FileCount int       `json:"file_count"`
	Timestamp time.Time `json:"timestamp"`
}

type persistedState struct {
	Pointer   int        `json:"pointer"`
	NextID    int        `json:"next_id"`
	Revisions []Revision `json:"revisions"`
}

func (r *Revision) toInfo() RevisionInfo {
	return RevisionInfo{
		ID:        r.ID,
		Label:     r.Label,
		FileCount: r.FileCount,
		Timestamp: r.Timestamp,
	}
}

// Manager manages revision snapshots for a workspace
type Manager struct {
	workspacePath string
	revisions     []Revision
	pointer       int // Current position in revision stack (-1 = no revisions)
	mu            sync.Mutex
	maxRevisions  int
	nextID        int
}

// NewManager creates (and restores from disk if present) a revision manager for the given workspace.
func NewManager(workspacePath string) *Manager {
	m := &Manager{
		workspacePath: workspacePath,
		pointer:       -1,
		maxRevisions:  20,
		nextID:        1,
	}
	m.loadFromDisk()
	return m
}

func (m *Manager) revisionsDir() string {
	return filepath.Join(m.workspacePath, ".revisions")
}

func (m *Manager) stateFilePath() string {
	return filepath.Join(m.revisionsDir(), "state.json")
}

func (m *Manager) revisionFilePath(id int) string {
	return filepath.Join(m.revisionsDir(), fmt.Sprintf("rev_%d.json", id))
}

func (m *Manager) loadFromDisk() {
	data, err := os.ReadFile(m.stateFilePath())
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	loaded := make([]Revision, 0, len(st.Revisions))
	for _, rev := range st.Revisions {
		filesData, fErr := os.ReadFile(m.revisionFilePath(rev.ID))
		if fErr != nil {
			continue
		}
		var snaps []FileSnapshot
		if jErr := json.Unmarshal(filesData, &snaps); jErr != nil {
			continue
		}
		rev.Files = snaps
		loaded = append(loaded, rev)
	}
	m.revisions = loaded
	if len(loaded) == 0 {
		m.pointer = -1
	} else {
		m.pointer = st.Pointer
		if m.pointer < 0 {
			m.pointer = 0
		}
		if m.pointer >= len(loaded) {
			m.pointer = len(loaded) - 1
		}
	}
	if st.NextID > m.nextID {
		m.nextID = st.NextID
	}
}

func (m *Manager) saveToDisk(newRev *Revision, removedIDs []int) {
	dir := m.revisionsDir()
	_ = os.MkdirAll(dir, 0755)
	if newRev != nil {
		if raw, err := json.Marshal(newRev.Files); err == nil {
			_ = os.WriteFile(m.revisionFilePath(newRev.ID), raw, 0644)
		}
	}
	for _, id := range removedIDs {
		_ = os.Remove(m.revisionFilePath(id))
	}
	st := persistedState{
		Pointer:   m.pointer,
		NextID:    m.nextID,
		Revisions: m.revisions,
	}
	if raw, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(m.stateFilePath(), raw, 0644)
	}
}

func snapshotsEqual(a, b []FileSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Exists != b[i].Exists || !bytes.Equal(a[i].Content, b[i].Content) {
			return false
		}
	}
	return true
}

func (m *Manager) appendSnapshotLocked(label string, files []FileSnapshot) error {
	var removedIDs []int
	// Truncate future revisions when branching from an earlier point
	if m.pointer >= 0 && m.pointer < len(m.revisions)-1 {
		for _, r := range m.revisions[m.pointer+1:] {
			removedIDs = append(removedIDs, r.ID)
		}
		m.revisions = m.revisions[:m.pointer+1]
	} else if m.pointer < 0 && len(m.revisions) > 0 {
		for _, r := range m.revisions {
			removedIDs = append(removedIDs, r.ID)
		}
		m.revisions = nil
	}

	rev := Revision{
		ID:        m.nextID,
		Label:     label,
		Files:     files,
		FileCount: len(files),
		Timestamp: time.Now(),
	}
	m.nextID++

	m.revisions = append(m.revisions, rev)
	m.pointer = len(m.revisions) - 1

	// Enforce max revisions
	if len(m.revisions) > m.maxRevisions {
		excess := len(m.revisions) - m.maxRevisions
		for _, r := range m.revisions[:excess] {
			removedIDs = append(removedIDs, r.ID)
		}
		m.revisions = m.revisions[excess:]
		m.pointer -= excess
		if m.pointer < 0 {
			m.pointer = 0
		}
	}

	m.saveToDisk(&rev, removedIDs)
	return nil
}

// CreateSnapshot captures the current workspace source files (src/ + index.html).
func (m *Manager) CreateSnapshot(label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	files, err := m.snapshotSrcFiles()
	if err != nil {
		return fmt.Errorf("failed to snapshot files: %w", err)
	}
	return m.appendSnapshotLocked(label, files)
}

// EnsureBaselineSnapshot records a snapshot if none exists OR if disk files diverged
// out-of-band from the current revision (e.g., manual edit before a build).
func (m *Manager) EnsureBaselineSnapshot(label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	files, err := m.snapshotSrcFiles()
	if err != nil {
		return fmt.Errorf("failed to snapshot files: %w", err)
	}
	if len(m.revisions) == 0 || m.pointer < 0 || m.pointer >= len(m.revisions) {
		return m.appendSnapshotLocked(label, files)
	}
	if !snapshotsEqual(files, m.revisions[m.pointer].Files) {
		return m.appendSnapshotLocked(label, files)
	}
	return nil
}

// CreateSnapshotIfChanged records a new snapshot after edits complete only if files actually changed.
func (m *Manager) CreateSnapshotIfChanged(label string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	files, err := m.snapshotSrcFiles()
	if err != nil {
		return false, fmt.Errorf("failed to snapshot files: %w", err)
	}
	if len(m.revisions) == 0 || m.pointer < 0 || m.pointer >= len(m.revisions) {
		if err := m.appendSnapshotLocked(label, files); err != nil {
			return false, err
		}
		return true, nil
	}
	if snapshotsEqual(files, m.revisions[m.pointer].Files) {
		return false, nil
	}
	if err := m.appendSnapshotLocked(label, files); err != nil {
		return false, err
	}
	return true, nil
}

// Status returns pointer, total revisions, canUndo, and canRedo flags.
func (m *Manager) Status() (pointer int, total int, canUndo bool, canRedo bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	total = len(m.revisions)
	pointer = m.pointer
	canUndo = total > 0 && pointer > 0
	canRedo = total > 0 && pointer >= 0 && pointer < total-1
	return pointer, total, canUndo, canRedo
}

// Undo reverts workspace to the previous revision state.
func (m *Manager) Undo() (*RevisionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pointer <= 0 || len(m.revisions) == 0 {
		return nil, fmt.Errorf("nothing to undo")
	}

	m.pointer--
	rev := &m.revisions[m.pointer]
	if err := m.restoreRevision(rev); err != nil {
		m.pointer++ // rollback on failure
		return nil, fmt.Errorf("failed to restore revision: %w", err)
	}
	m.saveToDisk(nil, nil)

	info := rev.toInfo()
	return &info, nil
}

// Redo moves forward to the next revision.
func (m *Manager) Redo() (*RevisionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pointer >= len(m.revisions)-1 || len(m.revisions) == 0 {
		return nil, fmt.Errorf("nothing to redo")
	}

	return m.restoreIndexLocked(m.pointer + 1)
}

// Restore jumps directly to the revision matching revisionID.
func (m *Manager) Restore(revisionID int) (*RevisionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	targetIdx := -1
	for i := range m.revisions {
		if m.revisions[i].ID == revisionID {
			targetIdx = i
			break
		}
	}
	if targetIdx == -1 {
		return nil, fmt.Errorf("revision %d not found", revisionID)
	}

	return m.restoreIndexLocked(targetIdx)
}

func (m *Manager) restoreIndexLocked(targetIdx int) (*RevisionInfo, error) {
	if targetIdx < 0 || targetIdx >= len(m.revisions) {
		return nil, fmt.Errorf("revision index %d out of range", targetIdx)
	}

	prevPointer := m.pointer
	m.pointer = targetIdx
	rev := &m.revisions[m.pointer]
	if err := m.restoreRevision(rev); err != nil {
		m.pointer = prevPointer // rollback pointer on failure
		return nil, fmt.Errorf("failed to restore revision: %w", err)
	}
	m.saveToDisk(nil, nil)

	info := rev.toInfo()
	return &info, nil
}

// List returns all revisions with the current pointer position.
func (m *Manager) List() ([]RevisionInfo, int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]RevisionInfo, len(m.revisions))
	for i, rev := range m.revisions {
		infos[i] = rev.toInfo()
	}
	return infos, m.pointer
}

// HasRevisions returns whether any snapshots exist
func (m *Manager) HasRevisions() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.revisions) > 0
}

// restoreRevision writes all file snapshots from a revision to disk
// and invalidates stale dist/ output so the next build compiles the restored files.
func (m *Manager) restoreRevision(rev *Revision) error {
	srcDir := filepath.Join(m.workspacePath, "src")

	// Build set of files in the revision (normalized with forward slashes)
	revFiles := make(map[string]bool)
	for _, snap := range rev.Files {
		revFiles[filepath.ToSlash(snap.Path)] = true
	}

	// Remove tracked files that exist now under src/ (or index.html) but weren't in the revision
	currentFiles, _ := m.walkSrcFiles()
	for _, rel := range currentFiles {
		norm := filepath.ToSlash(rel)
		if !revFiles[norm] {
			if err := os.Remove(filepath.Join(m.workspacePath, filepath.FromSlash(norm))); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove %s during restore: %w", norm, err)
			}
		}
	}

	// Restore files from the revision
	for _, snap := range rev.Files {
		norm := filepath.ToSlash(snap.Path)
		targetPath := filepath.Join(m.workspacePath, filepath.FromSlash(norm))
		if !snap.Exists {
			if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove %s during restore: %w", norm, err)
			}
			continue
		}
		dir := filepath.Dir(targetPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		if err := os.WriteFile(targetPath, snap.Content, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", norm, err)
		}
	}

	// Clean up empty directories in src/
	cleanEmptyDirs(srcDir)

	// Invalidate dist/index.html so runBuildInWorkspace never skips Vite via isDistUpToDate
	_ = os.Remove(filepath.Join(m.workspacePath, "dist", "index.html"))

	return nil
}

// snapshotSrcFiles walks src/ and captures index.html in deterministic sorted order.
func (m *Manager) snapshotSrcFiles() ([]FileSnapshot, error) {
	srcDir := filepath.Join(m.workspacePath, "src")
	var snapshots []FileSnapshot

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(m.workspacePath, path)
		if err != nil {
			return nil
		}
		norm := filepath.ToSlash(relPath)

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		snapshots = append(snapshots, FileSnapshot{
			Path:    norm,
			Content: content,
			Size:    int64(len(content)),
			Exists:  true,
		})
		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// Also snapshot root index.html (fonts/title changes)
	indexPath := filepath.Join(m.workspacePath, "index.html")
	if content, iErr := os.ReadFile(indexPath); iErr == nil {
		snapshots = append(snapshots, FileSnapshot{
			Path:    "index.html",
			Content: content,
			Size:    int64(len(content)),
			Exists:  true,
		})
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Path < snapshots[j].Path
	})

	return snapshots, nil
}

// walkSrcFiles returns all tracked file paths under src/ and index.html relative to workspace.
func (m *Manager) walkSrcFiles() ([]string, error) {
	srcDir := filepath.Join(m.workspacePath, "src")
	var paths []string

	_ = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(m.workspacePath, path)
		if err != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(relPath))
		return nil
	})

	if _, err := os.Stat(filepath.Join(m.workspacePath, "index.html")); err == nil {
		paths = append(paths, "index.html")
	}

	return paths, nil
}

// cleanEmptyDirs removes empty directories under the given path
func cleanEmptyDirs(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		entries, _ := os.ReadDir(path)
		if len(entries) == 0 {
			_ = os.Remove(path)
		}
		return nil
	})
}
