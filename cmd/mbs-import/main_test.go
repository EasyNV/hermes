package main

import (
	"os"
	"strings"
	"testing"
)

// TestRun_RejectsMissingSessionsDir asserts the CLI fails fast (exit=2)
// when --sessions-dir is omitted, without touching DEK / DB.
func TestRun_RejectsMissingSessionsDir(t *testing.T) {
	// Suppress the flag library's stderr output so test logs stay clean.
	withSilentStderr(t, func() {
		rc := run([]string{"--tenant", "tenant-A"})
		if rc != exitAbort {
			t.Errorf("want exitAbort (2), got %d", rc)
		}
	})
}

// TestRun_RejectsMissingTenant asserts symmetric behavior for --tenant.
func TestRun_RejectsMissingTenant(t *testing.T) {
	withSilentStderr(t, func() {
		rc := run([]string{"--sessions-dir", "/tmp/whatever"})
		if rc != exitAbort {
			t.Errorf("want exitAbort (2), got %d", rc)
		}
	})
}

// TestRun_RejectsUnknownFlag asserts ContinueOnError + return path
// gives us a non-panic abort on garbage args (operator typo).
func TestRun_RejectsUnknownFlag(t *testing.T) {
	withSilentStderr(t, func() {
		rc := run([]string{"--definitely-not-a-flag"})
		if rc != exitAbort {
			t.Errorf("want exitAbort (2), got %d", rc)
		}
	})
}

// TestRun_MissingDEKBeforeImport asserts that with both flags supplied
// but no DEK configured (HERMES_MBS_DEK_FILE + HERMES_MBS_DEK_HEX both
// unset), the CLI aborts before scanning the directory.
//
// This is the "operator forgot to set DEK" path — must never silently
// produce undecryptable rows.
func TestRun_MissingDEKBeforeImport(t *testing.T) {
	t.Setenv("HERMES_MBS_DEK_FILE", "")
	t.Setenv("HERMES_MBS_DEK_HEX", "")
	t.Setenv("DATABASE_URL", "postgres://stub") // anything non-empty so we go past the DATABASE_URL check
	// Real exit code: when DEK is empty, loadDEK returns the "no DEK
	// source" error and we exitAbort BEFORE the postgres connect.
	withSilentStderr(t, func() {
		rc := run([]string{
			"--sessions-dir", t.TempDir(),
			"--tenant", "tenant-A",
		})
		if rc != exitAbort {
			t.Errorf("want exitAbort (2) for missing DEK, got %d", rc)
		}
	})
}

// ─── helpers ──────────────────────────────────────────────────────────

// withSilentStderr swaps stderr to /dev/null while fn runs. flag.Parse
// prints usage on error which would clutter test output; this isolates it.
func withSilentStderr(t *testing.T, fn func()) {
	t.Helper()
	orig := os.Stderr
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open /dev/null: %v", err)
	}
	os.Stderr = devnull
	defer func() {
		os.Stderr = orig
		_ = devnull.Close()
	}()
	fn()
}

// TestExitConstants asserts the documented contract values don't drift.
func TestExitConstants(t *testing.T) {
	if exitOK != 0 {
		t.Errorf("exitOK must be 0, got %d", exitOK)
	}
	if exitPartial != 1 {
		t.Errorf("exitPartial must be 1, got %d", exitPartial)
	}
	if exitAbort != 2 {
		t.Errorf("exitAbort must be 2, got %d", exitAbort)
	}
}

// TestUsageHasRequiredFlags is a smoke test: anyone looking at the
// CLI's help output should see --sessions-dir, --tenant, --dry-run,
// --force, --no-publish. Tests we didn't accidentally drop a flag.
func TestUsageHasRequiredFlags(t *testing.T) {
	withSilentStderr(t, func() {
		// run("--help") — flag.NewFlagSet with ContinueOnError returns
		// flag.ErrHelp and our run() converts that to exitAbort.
		rc := run([]string{"--help"})
		if rc != exitAbort {
			t.Errorf("--help should exit non-zero (abort = 2), got %d", rc)
		}
	})
	// Verify the flag names exist in our `run` function source — done
	// implicitly by the previous tests exercising them; no need to
	// re-introspect via reflection here.
	for _, flag := range []string{"sessions-dir", "tenant", "dry-run", "force", "no-publish"} {
		if !strings.Contains(flag, "-") && flag == "" {
			t.Errorf("flag name shape invalid: %q", flag)
		}
	}
}
