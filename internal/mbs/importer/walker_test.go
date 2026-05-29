package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny test helper.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestWalkSessions_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "100.json"), `{}`)
	writeFile(t, filepath.Join(dir, "200.json"), `{}`)
	writeFile(t, filepath.Join(dir, "100.bridge.json"), `{}`)

	got, err := walkSessions(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2", len(got))
	}

	// Sort order: ascending uid.
	if got[0].UID != 100 || got[1].UID != 200 {
		t.Errorf("order: got [%d, %d], want [100, 200]", got[0].UID, got[1].UID)
	}

	// Envelope pairing.
	if got[0].EnvelopePath == "" {
		t.Error("uid=100 should have envelope path set (100.bridge.json present)")
	}
	if !strings.HasSuffix(got[0].EnvelopePath, "100.bridge.json") {
		t.Errorf("envelope path: got %q", got[0].EnvelopePath)
	}
	if got[1].EnvelopePath != "" {
		t.Errorf("uid=200 has no sidecar; envelope path should be empty, got %q", got[1].EnvelopePath)
	}
}

func TestWalkSessions_SkipsBakFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "100.json"), `{}`)
	writeFile(t, filepath.Join(dir, "100.json.bak"), `{}`)
	writeFile(t, filepath.Join(dir, "200.json.bak"), `{}`)

	got, err := walkSessions(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 1 || got[0].UID != 100 {
		t.Errorf("got %+v, want only uid=100", got)
	}
}

func TestWalkSessions_SkipsNonNumericBasenames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "100.json"), `{}`)
	writeFile(t, filepath.Join(dir, "default.json"), `{}`)
	writeFile(t, filepath.Join(dir, "test-fixture.json"), `{}`)
	writeFile(t, filepath.Join(dir, "0.json"), `{}`) // zero uid rejected

	got, err := walkSessions(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 1 || got[0].UID != 100 {
		t.Errorf("got %+v, want only uid=100", got)
	}
}

func TestWalkSessions_IgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "archive")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "100.json"), `{}`)
	writeFile(t, filepath.Join(subdir, "200.json"), `{}`) // shouldn't be picked up

	got, err := walkSessions(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 1 || got[0].UID != 100 {
		t.Errorf("got %+v, want only uid=100", got)
	}
}

func TestWalkSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := walkSessions(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty dir should yield no files, got %d", len(got))
	}
}

func TestWalkSessions_MissingDirReturnsError(t *testing.T) {
	_, err := walkSessions("/nonexistent/path/does/not/exist")
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
	if !strings.Contains(err.Error(), "/nonexistent/path/does/not/exist") {
		t.Errorf("err should contain the path, got %q", err.Error())
	}
}

func TestParseUIDFromName(t *testing.T) {
	cases := []struct {
		name      string
		wantUID   int64
		wantValid bool
	}{
		{"100.json", 100, true},
		{"1674772559.json", 1674772559, true},
		{"61590134170831.json", 61590134170831, true},
		{"0.json", 0, false},          // zero uid
		{"-1.json", 0, false},         // negative
		{"100.json.bak", 0, false},    // backup
		{"100.bridge.json", 0, false}, // sidecar
		{"default.json", 0, false},    // non-numeric
		{"100.txt", 0, false},         // wrong extension
		{"", 0, false},                // empty
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotUID, gotValid := parseUIDFromName(c.name)
			if gotUID != c.wantUID || gotValid != c.wantValid {
				t.Errorf("parseUID(%q): got (%d, %v), want (%d, %v)",
					c.name, gotUID, gotValid, c.wantUID, c.wantValid)
			}
		})
	}
}
