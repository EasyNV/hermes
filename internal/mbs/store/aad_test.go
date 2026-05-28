package store

import (
	"bytes"
	"testing"
)

func TestBuildAAD_AllColumns(t *testing.T) {
	const uid = int64(61590134170831)

	cases := []struct {
		col  AADColumn
		want string
	}{
		{AADAccessToken, "mbs.access_token.uid=61590134170831"},
		{AADSecret, "mbs.secret.uid=61590134170831"},
		{AADSessionKey, "mbs.session_key.uid=61590134170831"},
		{AADCookies, "mbs.cookies.uid=61590134170831"},
		{AADTOTPSecret, "mbs.totp_secret.uid=61590134170831"},
	}

	for _, tc := range cases {
		t.Run(string(tc.col), func(t *testing.T) {
			got := BuildAAD(tc.col, uid)
			if !bytes.Equal(got, []byte(tc.want)) {
				t.Errorf("BuildAAD: got %q want %q", got, tc.want)
			}
			if s := FormatAAD(tc.col, uid); s != tc.want {
				t.Errorf("FormatAAD drift: got %q want %q", s, tc.want)
			}
			// The two functions MUST produce byte-identical output —
			// otherwise encrypt/decrypt drift bug.
			if string(got) != FormatAAD(tc.col, uid) {
				t.Errorf("BuildAAD and FormatAAD disagree: %q vs %q",
					string(got), FormatAAD(tc.col, uid))
			}
		})
	}
}

func TestBuildAAD_DifferentUIDs_DifferentAAD(t *testing.T) {
	// uid in the AAD is what prevents cross-account ciphertext swaps.
	// Two ciphertexts encrypted under different uids must NOT have
	// identical AAD — pinned here.
	a := BuildAAD(AADAccessToken, 61590134170831)
	b := BuildAAD(AADAccessToken, 1674772559)
	if bytes.Equal(a, b) {
		t.Errorf("AAD for different uids collided:\n a: %q\n b: %q", a, b)
	}

	// Edge cases: negative uid (shouldn't happen but let's be defensive),
	// zero uid, very large uid.
	for _, uid := range []int64{0, -1, 1, 9_223_372_036_854_775_807} {
		t.Run("uid_distinct_format", func(t *testing.T) {
			got := BuildAAD(AADAccessToken, uid)
			if len(got) == 0 {
				t.Errorf("uid=%d produced empty AAD", uid)
			}
		})
	}
}
