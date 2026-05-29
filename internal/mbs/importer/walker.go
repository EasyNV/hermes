// Package importer migrates JSON-on-disk MBS session inventory into
// encrypted Postgres rows.
//
// Source format (legacy mbs-native sessions directory):
//
//	~/.mbs-native/sessions/<uid>.json         — auth.Creds JSON
//	~/.mbs-native/sessions/<uid>.bridge.json  — auth.BridgeEnvelope JSON (Stage D+ only)
//	~/.mbs-native/sessions/<uid>.json.bak     — backup (SKIPPED)
//
// Target: hermes-mbs encrypted Postgres rows (chunk 2 schema) with
// column-bound AAD via store.BuildAAD.
//
// One-way: importer never writes back to the JSON files. Once imported,
// they become read-only archives. Idempotent: ExistsSession=true rows
// are skipped unless Force is set.
//
// Single tenant per run (Options.TenantID). Multi-tenant migration =
// multiple runs.
package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SessionFile is one paired discovery: the creds JSON path and the
// envelope JSON path (empty if no sidecar).
type SessionFile struct {
	UID          int64
	SessionPath  string // <dir>/<uid>.json
	EnvelopePath string // <dir>/<uid>.bridge.json — "" if missing
}

// walkSessions lists every <uid>.json under dir and pairs it with
// <uid>.bridge.json when present. Single-level walk; sub-directories
// are ignored.
//
// Skipped:
//   - .bak files
//   - non-numeric basenames (e.g., "default.json", "test-fixture.json")
//   - sub-directories
//   - paired .bridge.json files (they're listed via their sister
//     creds file, not as standalone discoveries)
//
// Returns a stable-sorted slice (sorted by uid ascending) so import
// runs are deterministic. Tests assert this order; production
// benefits from predictable log lines.
func walkSessions(dir string) ([]SessionFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read sessions dir %q: %w", dir, err)
	}

	// First pass: collect uid -> creds path mapping.
	creds := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		uid, ok := parseUIDFromName(name)
		if !ok {
			continue
		}
		creds[uid] = filepath.Join(dir, name)
	}

	// Second pass: pair with .bridge.json sidecars if present.
	out := make([]SessionFile, 0, len(creds))
	for uid, credsPath := range creds {
		sf := SessionFile{
			UID:         uid,
			SessionPath: credsPath,
		}
		bridgePath := filepath.Join(dir, fmt.Sprintf("%d.bridge.json", uid))
		if _, err := os.Stat(bridgePath); err == nil {
			sf.EnvelopePath = bridgePath
		}
		out = append(out, sf)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// parseUIDFromName decodes "<uid>.json" -> uid. Rejects:
//   - any name not ending in .json
//   - any name ending in .bak (e.g. "<uid>.json.bak")
//   - sidecars: "<uid>.bridge.json"
//   - non-numeric basenames
//
// Returns (0, false) on any rejection.
func parseUIDFromName(name string) (int64, bool) {
	// Reject backups.
	if strings.HasSuffix(name, ".bak") {
		return 0, false
	}
	// Reject envelope sidecars.
	if strings.HasSuffix(name, ".bridge.json") {
		return 0, false
	}
	// Require .json.
	if !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	base := strings.TrimSuffix(name, ".json")
	uid, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return 0, false
	}
	if uid <= 0 {
		return 0, false
	}
	return uid, true
}
