package bridge

import (
	"strings"
	"testing"
)

func TestNormalizeTOTPSecret_StripsSpacesAndDashes(t *testing.T) {
	got, err := normalizeTOTPSecret("JBSW Y3DP-EHPK_3PXP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "JBSWY3DPEHPK3PXP" {
		t.Errorf("got %q, want JBSWY3DPEHPK3PXP", got)
	}
}

func TestNormalizeTOTPSecret_UppercasesLowercase(t *testing.T) {
	got, err := normalizeTOTPSecret("jbswy3dpehpk3pxp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "JBSWY3DPEHPK3PXP" {
		t.Errorf("got %q, want JBSWY3DPEHPK3PXP", got)
	}
}

func TestNormalizeTOTPSecret_RejectsEmpty(t *testing.T) {
	if _, err := normalizeTOTPSecret(""); err == nil {
		t.Errorf("expected error on empty input")
	}
}

func TestNormalizeTOTPSecret_RejectsInvalidBase32Chars(t *testing.T) {
	// '0', '1', '8', '9' are NOT valid base32. Punctuation/symbols neither.
	cases := []string{
		"01JBSWY3DPEHPK3PXP", // '0' '1'
		"JBSWY9DPEHPK3PXP9",  // '9'
		"JBSWY3DP!EHPK3PXP",  // '!'
		"JBSWY3DP.EHPK3PXP",  // '.'
	}
	for _, in := range cases {
		if _, err := normalizeTOTPSecret(in); err == nil {
			t.Errorf("expected error for invalid input %q", in)
		} else if !strings.Contains(err.Error(), "invalid base32 char") {
			t.Errorf("input %q: error message should mention 'invalid base32 char', got %v", in, err)
		}
	}
}

func TestNormalizeTOTPSecret_RejectsTooShort(t *testing.T) {
	// Less than 16 chars after normalization fails.
	if _, err := normalizeTOTPSecret("ABCDEFGH"); err == nil {
		t.Errorf("expected error on 8-char secret")
	} else if !strings.Contains(err.Error(), ">= 16") {
		t.Errorf("error should mention 16-char minimum, got %v", err)
	}
}

func TestNormalizeTOTPSecret_AcceptsPaddingChar(t *testing.T) {
	// '=' is allowed (base32 padding).
	got, err := normalizeTOTPSecret("JBSWY3DPEHPK3PXP====")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "JBSWY3DPEHPK3PXP====" {
		t.Errorf("got %q, want padding preserved", got)
	}
}

func TestNormalizeTOTPSecret_IsByteEquivalentToPOC(t *testing.T) {
	// Same fixtures as re/mbs/mbs-bridge-login/totp_test.go to pin
	// the byte-for-byte equivalence claimed in the docstring.
	cases := map[string]string{
		"JBSWY3DPEHPK3PXP":                      "JBSWY3DPEHPK3PXP",
		"jbswy3dpehpk3pxp":                      "JBSWY3DPEHPK3PXP",
		"JBSW Y3DP EHPK 3PXP":                   "JBSWY3DPEHPK3PXP",
		"JBSW-Y3DP-EHPK-3PXP":                   "JBSWY3DPEHPK3PXP",
		"jbsw_y3dp_ehpk_3pxp":                   "JBSWY3DPEHPK3PXP",
		"JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP":      "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
	}
	for in, want := range cases {
		got, err := normalizeTOTPSecret(in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
