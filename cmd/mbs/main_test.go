package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mbsconfig "github.com/hermes-waba/hermes/internal/mbs/config"
)

// genHexKey returns a fresh hex-encoded 32-byte AES-256 key.
func genHexKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestLoadDEK_FilePreferredOverEnv pins the documented preference:
// when both file and env are set, file path wins.
func TestLoadDEK_FilePreferredOverEnv(t *testing.T) {
	tmpDir := t.TempDir()
	fileKeyHex := genHexKey(t)
	envKeyHex := genHexKey(t)
	if fileKeyHex == envKeyHex {
		t.Fatal("hex keys collided; rand broken")
	}

	keyPath := filepath.Join(tmpDir, "dek.hex")
	if err := os.WriteFile(keyPath, []byte(fileKeyHex+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	// Even though env is set, file should win.
	t.Setenv("HERMES_MBS_DEK_HEX", envKeyHex)

	cfg := mbsconfig.Config{
		DEKFile: keyPath,
		DEKHex:  envKeyHex,
	}
	got, err := loadDEK(cfg)
	if err != nil {
		t.Fatalf("loadDEK: %v", err)
	}

	gotHex := hex.EncodeToString(got[:])
	if gotHex != fileKeyHex {
		t.Errorf("loaded DEK should match file, not env: got=%s file=%s env=%s",
			gotHex, fileKeyHex, envKeyHex)
	}
}

// TestLoadDEK_FallsBackToEnv pins the fallback when DEKFile is empty.
func TestLoadDEK_FallsBackToEnv(t *testing.T) {
	envKeyHex := genHexKey(t)
	t.Setenv("HERMES_MBS_DEK_HEX", envKeyHex)

	cfg := mbsconfig.Config{
		DEKFile: "",
		DEKHex:  envKeyHex,
	}
	got, err := loadDEK(cfg)
	if err != nil {
		t.Fatalf("loadDEK: %v", err)
	}
	if hex.EncodeToString(got[:]) != envKeyHex {
		t.Errorf("loaded DEK should match env key")
	}
}

// TestLoadDEK_FailsClosedWhenBothMissing pins the fail-closed contract:
// no fallback to a zero key, descriptive error.
func TestLoadDEK_FailsClosedWhenBothMissing(t *testing.T) {
	t.Setenv("HERMES_MBS_DEK_HEX", "")
	cfg := mbsconfig.Config{DEKFile: "", DEKHex: ""}
	got, err := loadDEK(cfg)
	if err == nil {
		t.Fatal("expected error when both DEK sources missing")
	}
	if !got.IsZero() {
		t.Error("returned key should be zero on error")
	}
	// Error message names BOTH env vars so operator knows the
	// configuration surface.
	for _, want := range []string{"HERMES_MBS_DEK_FILE", "HERMES_MBS_DEK_HEX"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q should mention %q", err.Error(), want)
		}
	}
}

// TestLoadDEK_FileMissingReturnsWrappedError pins that file-not-found
// surfaces with the path in the error message (operator triage).
func TestLoadDEK_FileMissingReturnsWrappedError(t *testing.T) {
	cfg := mbsconfig.Config{DEKFile: "/nonexistent/dek.hex"}
	_, err := loadDEK(cfg)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "/nonexistent/dek.hex") {
		t.Errorf("err %q should contain the path", err.Error())
	}
}

// TestLoadDEK_BadHexInFile pins the validation path: 31-byte key
// rejected with descriptive error.
func TestLoadDEK_BadHexInFile(t *testing.T) {
	tmpDir := t.TempDir()
	bad := strings.Repeat("ab", 31) // 62 hex chars = 31 bytes, not 32
	keyPath := filepath.Join(tmpDir, "dek.hex")
	if err := os.WriteFile(keyPath, []byte(bad), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := mbsconfig.Config{DEKFile: keyPath}
	_, err := loadDEK(cfg)
	if err == nil {
		t.Fatal("expected error for short hex key")
	}
}
