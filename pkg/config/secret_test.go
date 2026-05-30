package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSecret_PrefersEnvOverFile(t *testing.T) {
	t.Setenv("ZZ_TEST_SECRET", "from-env")
	t.Setenv("ZZ_TEST_SECRET_FILE", "/nonexistent")

	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if !ok || v != "from-env" {
		t.Fatalf("env should win: got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_FallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.bin")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o400); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("ZZ_TEST_SECRET", "")
	t.Setenv("ZZ_TEST_SECRET_FILE", path)

	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if !ok || v != "from-file" {
		t.Fatalf("file should be read: got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_FileWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.bin")
	if err := os.WriteFile(path, []byte("from-file"), 0o400); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("ZZ_TEST_SECRET", "")
	t.Setenv("ZZ_TEST_SECRET_FILE", path)

	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if !ok || v != "from-file" {
		t.Fatalf("missing-newline: got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_NoSourcesConfigured(t *testing.T) {
	t.Setenv("ZZ_TEST_SECRET", "")
	t.Setenv("ZZ_TEST_SECRET_FILE", "")
	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if ok || v != "" {
		t.Fatalf("expected empty/false; got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_FileMissing(t *testing.T) {
	t.Setenv("ZZ_TEST_SECRET", "")
	t.Setenv("ZZ_TEST_SECRET_FILE", "/path/that/will/not/exist/zzz")
	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if ok || v != "" {
		t.Fatalf("missing file should be silent failure; got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(path, []byte("\n"), 0o400); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("ZZ_TEST_SECRET", "")
	t.Setenv("ZZ_TEST_SECRET_FILE", path)
	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if ok || v != "" {
		t.Fatalf("empty file should report no secret; got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_FileEnvNameUnset(t *testing.T) {
	t.Setenv("ZZ_TEST_SECRET", "")
	// Do NOT set ZZ_TEST_SECRET_FILE at all.
	os.Unsetenv("ZZ_TEST_SECRET_FILE")
	v, ok := LoadSecret("ZZ_TEST_SECRET", "ZZ_TEST_SECRET_FILE")
	if ok || v != "" {
		t.Fatalf("missing file env should be silent; got %q ok=%v", v, ok)
	}
}

func TestLoadSecret_EmptyFileEnvNameParameter(t *testing.T) {
	t.Setenv("ZZ_TEST_SECRET", "")
	v, ok := LoadSecret("ZZ_TEST_SECRET", "")
	if ok || v != "" {
		t.Fatalf("no file env name should be silent; got %q ok=%v", v, ok)
	}
}
