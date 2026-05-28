package crypto_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hermes-waba/hermes/pkg/crypto"
)

// genDEK returns a fresh random DEK. Used in every test so tests are
// independent.
func genDEK(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var k crypto.DataEncryptionKey
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return k
}

// ──────────────────────────────────────────────────────────────────
// Round-trip
// ──────────────────────────────────────────────────────────────────

func TestRoundTrip(t *testing.T) {
	dek := genDEK(t)

	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"single byte", []byte{0xAB}},
		{"short string", []byte("hello world")},
		{"access token shape", []byte("EAABu2IFZCq3oBPK3VYjvKLR8a2QZBcDe4Bxxxxxxxxxxxxx")},
		{"jsonb-ish blob", []byte(`{"xs":"abc","sessionid":"1674772559:xxx","fr":"yyy"}`)},
		{"1KB random", randomBytes(t, 1024)},
		{"64KB random", randomBytes(t, 64*1024)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealed, err := crypto.EncryptAESGCM(dek, tc.plaintext, nil)
			if err != nil {
				t.Fatalf("EncryptAESGCM: %v", err)
			}
			// Layout check: 12B nonce + plaintext + 16B tag.
			wantLen := 12 + len(tc.plaintext) + 16
			if len(sealed) != wantLen {
				t.Errorf("sealed length: got %d, want %d", len(sealed), wantLen)
			}
			got, err := crypto.DecryptAESGCM(dek, sealed, nil)
			if err != nil {
				t.Fatalf("DecryptAESGCM: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Errorf("round-trip mismatch:\n got: %q\nwant: %q", got, tc.plaintext)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────
// Tampered ciphertext is rejected
// ──────────────────────────────────────────────────────────────────

func TestTamperedCiphertextRejected(t *testing.T) {
	dek := genDEK(t)
	pt := []byte("the quick brown fox jumps over the lazy dog")
	sealed, err := crypto.EncryptAESGCM(dek, pt, nil)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}

	// Flip a bit in each region and confirm Decrypt rejects.
	regions := []struct {
		name string
		idx  int
	}{
		{"nonce first byte", 0},
		{"nonce last byte", 11},
		{"ciphertext first byte", 12},
		{"ciphertext mid byte", 12 + len(pt)/2},
		{"auth tag first byte", 12 + len(pt)},
		{"auth tag last byte", len(sealed) - 1},
	}

	for _, r := range regions {
		t.Run(r.name, func(t *testing.T) {
			tampered := append([]byte(nil), sealed...)
			tampered[r.idx] ^= 0x01
			_, err := crypto.DecryptAESGCM(dek, tampered, nil)
			if !errors.Is(err, crypto.ErrDecryptFailed) {
				t.Errorf("expected ErrDecryptFailed, got %v", err)
			}
		})
	}

	// Truncated input (< nonce + tag) is also rejected.
	t.Run("truncated", func(t *testing.T) {
		_, err := crypto.DecryptAESGCM(dek, sealed[:20], nil)
		if !errors.Is(err, crypto.ErrDecryptFailed) {
			t.Errorf("expected ErrDecryptFailed, got %v", err)
		}
	})
}

// ──────────────────────────────────────────────────────────────────
// Wrong DEK is rejected
// ──────────────────────────────────────────────────────────────────

func TestWrongDEKRejected(t *testing.T) {
	good := genDEK(t)
	bad := genDEK(t)
	if good == bad {
		t.Fatalf("two random DEKs collided — rand broken?")
	}
	sealed, err := crypto.EncryptAESGCM(good, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	_, err = crypto.DecryptAESGCM(bad, sealed, nil)
	if !errors.Is(err, crypto.ErrDecryptFailed) {
		t.Errorf("decryption with wrong DEK should fail with ErrDecryptFailed, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────
// Nonce uniqueness across many encryptions of the same plaintext
// ──────────────────────────────────────────────────────────────────

func TestNonceUniqueAcrossEncryptions(t *testing.T) {
	dek := genDEK(t)
	pt := []byte("identical plaintext on every iteration")

	const iterations = 10_000
	seen := make(map[[12]byte]struct{}, iterations)

	for i := 0; i < iterations; i++ {
		sealed, err := crypto.EncryptAESGCM(dek, pt, nil)
		if err != nil {
			t.Fatalf("EncryptAESGCM at iter %d: %v", i, err)
		}
		var nonce [12]byte
		copy(nonce[:], sealed[:12])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("nonce collision at iter %d (nonce=%x)", i, nonce)
		}
		seen[nonce] = struct{}{}
	}
	if len(seen) != iterations {
		t.Errorf("unique nonces: got %d, want %d", len(seen), iterations)
	}
}

// ──────────────────────────────────────────────────────────────────
// LoadDEKFromHexEnv
// ──────────────────────────────────────────────────────────────────

func TestLoadDEKFromHexEnv(t *testing.T) {
	const envVar = "HERMES_TEST_DEK_HEX"

	// Generate a known key, hex-encode it, set env, load, verify match.
	want := genDEK(t)
	wantHex := hex.EncodeToString(want[:])

	t.Run("happy path", func(t *testing.T) {
		t.Setenv(envVar, wantHex)
		got, err := crypto.LoadDEKFromHexEnv(envVar)
		if err != nil {
			t.Fatalf("LoadDEKFromHexEnv: %v", err)
		}
		if got != want {
			t.Errorf("key mismatch:\n got: %x\nwant: %x", got, want)
		}
	})

	t.Run("happy path with whitespace tolerance", func(t *testing.T) {
		t.Setenv(envVar, "  "+wantHex+"\n")
		got, err := crypto.LoadDEKFromHexEnv(envVar)
		if err != nil {
			t.Fatalf("LoadDEKFromHexEnv: %v", err)
		}
		if got != want {
			t.Errorf("key mismatch with whitespace:\n got: %x\nwant: %x", got, want)
		}
	})

	t.Run("unset rejected", func(t *testing.T) {
		// Explicit empty via Setenv (test runs in isolation; env may already be unset)
		t.Setenv(envVar, "")
		_, err := crypto.LoadDEKFromHexEnv(envVar)
		if err == nil {
			t.Error("expected error for empty env var")
		}
	})

	t.Run("invalid hex rejected", func(t *testing.T) {
		t.Setenv(envVar, "not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-")
		_, err := crypto.LoadDEKFromHexEnv(envVar)
		if err == nil {
			t.Error("expected error for non-hex input")
		}
	})

	t.Run("wrong length rejected", func(t *testing.T) {
		t.Setenv(envVar, "abcd")
		_, err := crypto.LoadDEKFromHexEnv(envVar)
		if !errors.Is(err, crypto.ErrInvalidKey) {
			t.Errorf("expected ErrInvalidKey, got %v", err)
		}
	})

	t.Run("zero key rejected by Encrypt", func(t *testing.T) {
		// A 64-char hex string of all zeros decodes to a zero DEK.
		// The loader returns it without complaint (it IS valid hex,
		// it IS 32 bytes) but Encrypt MUST refuse to use it.
		t.Setenv(envVar, "0000000000000000000000000000000000000000000000000000000000000000")
		dek, err := crypto.LoadDEKFromHexEnv(envVar)
		if err != nil {
			t.Fatalf("LoadDEKFromHexEnv: %v", err)
		}
		if !dek.IsZero() {
			t.Fatalf("expected zero DEK after loading zero hex")
		}
		_, err = crypto.EncryptAESGCM(dek, []byte("x"), nil)
		if err == nil {
			t.Error("expected error encrypting with zero DEK")
		}
	})
}

// ──────────────────────────────────────────────────────────────────
// LoadDEKFromFile
// ──────────────────────────────────────────────────────────────────

func TestLoadDEKFromFile(t *testing.T) {
	want := genDEK(t)
	wantHex := hex.EncodeToString(want[:])
	dir := t.TempDir()

	t.Run("happy path no newline", func(t *testing.T) {
		path := filepath.Join(dir, "dek-plain")
		if err := os.WriteFile(path, []byte(wantHex), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := crypto.LoadDEKFromFile(path)
		if err != nil {
			t.Fatalf("LoadDEKFromFile: %v", err)
		}
		if got != want {
			t.Errorf("key mismatch")
		}
	})

	t.Run("happy path trailing newline tolerated", func(t *testing.T) {
		// Matches `openssl rand -hex 32 > file` which appends "\n".
		path := filepath.Join(dir, "dek-with-newline")
		if err := os.WriteFile(path, []byte(wantHex+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := crypto.LoadDEKFromFile(path)
		if err != nil {
			t.Fatalf("LoadDEKFromFile: %v", err)
		}
		if got != want {
			t.Errorf("key mismatch with trailing newline")
		}
	})

	t.Run("empty path rejected", func(t *testing.T) {
		_, err := crypto.LoadDEKFromFile("")
		if err == nil {
			t.Error("expected error for empty path")
		}
	})

	t.Run("missing file rejected", func(t *testing.T) {
		_, err := crypto.LoadDEKFromFile(filepath.Join(dir, "does-not-exist"))
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("empty file rejected", func(t *testing.T) {
		path := filepath.Join(dir, "empty")
		if err := os.WriteFile(path, []byte("   \n\n  "), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := crypto.LoadDEKFromFile(path)
		if err == nil {
			t.Error("expected error for whitespace-only file")
		}
	})

	t.Run("invalid hex rejected", func(t *testing.T) {
		path := filepath.Join(dir, "bad-hex")
		if err := os.WriteFile(path, []byte(
			"ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"),
			0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := crypto.LoadDEKFromFile(path)
		if err == nil {
			t.Error("expected error for non-hex contents")
		}
	})
}

// ──────────────────────────────────────────────────────────────────
// Zero-DEK guards (both directions)
// ──────────────────────────────────────────────────────────────────

func TestZeroDEKRefusedOnEncrypt(t *testing.T) {
	var zero crypto.DataEncryptionKey
	if !zero.IsZero() {
		t.Fatal("zero-value DataEncryptionKey should report IsZero()=true")
	}
	_, err := crypto.EncryptAESGCM(zero, []byte("anything"), nil)
	if err == nil {
		t.Error("EncryptAESGCM with zero DEK should error")
	}
}

func TestZeroDEKRefusedOnDecrypt(t *testing.T) {
	// First, produce a real ciphertext under a real key.
	good := genDEK(t)
	sealed, err := crypto.EncryptAESGCM(good, []byte("payload"), nil)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	// Now try to decrypt with the zero DEK.
	var zero crypto.DataEncryptionKey
	_, err = crypto.DecryptAESGCM(zero, sealed, nil)
	if err == nil {
		t.Error("DecryptAESGCM with zero DEK should error")
	}
	// We don't pin which error type — just that it refuses before
	// touching the auth tag (so an attacker can't even induce a tag
	// compare with a zero key).
}

// ──────────────────────────────────────────────────────────────────
// Pathological short ciphertext lengths
// ──────────────────────────────────────────────────────────────────

func TestDecryptRejectsShortInputs(t *testing.T) {
	dek := genDEK(t)
	for _, n := range []int{0, 1, 11, 12, 13, 27} {
		t.Run(fmt.Sprintf("len_%d", n), func(t *testing.T) {
			short := make([]byte, n)
			_, err := crypto.DecryptAESGCM(dek, short, nil)
			if !errors.Is(err, crypto.ErrDecryptFailed) {
				t.Errorf("len=%d: expected ErrDecryptFailed, got %v", n, err)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────
// AAD binding (Additional Authenticated Data)
// ──────────────────────────────────────────────────────────────────

func TestAAD_RoundTripWithMatchingAAD(t *testing.T) {
	dek := genDEK(t)
	pt := []byte("EAABu2IFZCq3oBPK3VYjvKLR8a2QZBcDe4Bxxxxxxxxxxxxx")
	aad := []byte("mbs.access_token.uid=61590134170831")

	sealed, err := crypto.EncryptAESGCM(dek, pt, aad)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	got, err := crypto.DecryptAESGCM(dek, sealed, aad)
	if err != nil {
		t.Fatalf("DecryptAESGCM with matching AAD: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Errorf("round-trip mismatch: got %q want %q", got, pt)
	}
}

func TestAAD_MismatchedAADRejected(t *testing.T) {
	dek := genDEK(t)
	pt := []byte("the secret value")

	cases := []struct {
		name    string
		encAAD  []byte
		decAAD  []byte
		wantErr bool
	}{
		// Threat model: an attacker with row-write access tries to swap
		// a ciphertext from one column to another, or from one uid to
		// another. The AAD binding makes every such swap fail.
		{
			name:    "swap access_token to secret column",
			encAAD:  []byte("mbs.access_token.uid=61590134170831"),
			decAAD:  []byte("mbs.secret.uid=61590134170831"),
			wantErr: true,
		},
		{
			name:    "swap between uids on same column",
			encAAD:  []byte("mbs.access_token.uid=61590134170831"),
			decAAD:  []byte("mbs.access_token.uid=1674772559"),
			wantErr: true,
		},
		{
			name:    "strip AAD (encrypted with, decrypt without)",
			encAAD:  []byte("mbs.access_token.uid=61590134170831"),
			decAAD:  nil,
			wantErr: true,
		},
		{
			name:    "inject AAD (encrypted without, decrypt with)",
			encAAD:  nil,
			decAAD:  []byte("mbs.access_token.uid=61590134170831"),
			wantErr: true,
		},
		{
			name:    "single byte AAD difference",
			encAAD:  []byte("mbs.access_token.uid=61590134170831"),
			decAAD:  []byte("mbs.access_token.uid=61590134170830"),
			wantErr: true,
		},
		{
			name:    "nil-on-both still valid",
			encAAD:  nil,
			decAAD:  nil,
			wantErr: false,
		},
		{
			name:    "empty-bytes equivalent to nil",
			encAAD:  []byte{},
			decAAD:  nil,
			wantErr: false, // GCM treats []byte{} and nil identically
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealed, err := crypto.EncryptAESGCM(dek, pt, tc.encAAD)
			if err != nil {
				t.Fatalf("EncryptAESGCM: %v", err)
			}
			got, err := crypto.DecryptAESGCM(dek, sealed, tc.decAAD)
			if tc.wantErr {
				if !errors.Is(err, crypto.ErrDecryptFailed) {
					t.Errorf("expected ErrDecryptFailed, got %v", err)
				}
				if got != nil {
					t.Errorf("expected nil plaintext on failure, got %q", got)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !bytes.Equal(got, pt) {
					t.Errorf("round-trip mismatch: got %q want %q", got, pt)
				}
			}
		})
	}
}

func TestAAD_DoesNotEnlargeCiphertext(t *testing.T) {
	// AAD is authenticated but not encrypted — output size must be
	// independent of AAD length. Pins the "AAD length doesn't grow
	// output" property documented in the package doc.
	dek := genDEK(t)
	pt := []byte("plaintext payload of known length")

	withoutAAD, err := crypto.EncryptAESGCM(dek, pt, nil)
	if err != nil {
		t.Fatalf("EncryptAESGCM nil AAD: %v", err)
	}
	withLongAAD, err := crypto.EncryptAESGCM(dek, pt, []byte(
		"mbs.access_token.uid=61590134170831.column.context.padding.padding.padding"))
	if err != nil {
		t.Fatalf("EncryptAESGCM long AAD: %v", err)
	}
	if len(withoutAAD) != len(withLongAAD) {
		t.Errorf("ciphertext length should be independent of AAD: nil=%d long=%d",
			len(withoutAAD), len(withLongAAD))
	}
}

func TestAAD_BindingSurvivesTampering(t *testing.T) {
	// Layered defense — even if the attacker correctly guesses the
	// AAD format (e.g. by reading our source), tampering with the
	// ciphertext still fails because AAD does NOT replace the auth
	// tag, it composes with it.
	dek := genDEK(t)
	aad := []byte("mbs.access_token.uid=61590134170831")

	sealed, err := crypto.EncryptAESGCM(dek, []byte("secret"), aad)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	// Flip a bit in the ciphertext region.
	tampered := append([]byte(nil), sealed...)
	tampered[15] ^= 0x01

	_, err = crypto.DecryptAESGCM(dek, tampered, aad)
	if !errors.Is(err, crypto.ErrDecryptFailed) {
		t.Errorf("expected ErrDecryptFailed even with correct AAD, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}
