package db

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOverlayURL(t *testing.T) {
	const base = "postgres://hermes:pw@host:5432/hermes"

	cases := []struct {
		name      string
		input     string
		opts      PoolOpts
		wantQuery map[string]string
		wantErr   bool
	}{
		{
			name:  "empty opts leaves URL unchanged",
			input: base,
			opts:  PoolOpts{},
		},
		{
			name:      "sslmode added to URL with no query",
			input:     base,
			opts:      PoolOpts{SSLMode: "require"},
			wantQuery: map[string]string{"sslmode": "require"},
		},
		{
			name:      "sslmode replaces existing query value",
			input:     base + "?sslmode=disable",
			opts:      PoolOpts{SSLMode: "verify-full"},
			wantQuery: map[string]string{"sslmode": "verify-full"},
		},
		{
			name:  "sslrootcert added alongside existing params",
			input: base + "?pool_max_conns=10",
			opts:  PoolOpts{SSLRootCert: "/etc/ssl/ca.pem"},
			wantQuery: map[string]string{
				"sslrootcert":    "/etc/ssl/ca.pem",
				"pool_max_conns": "10",
			},
		},
		{
			name:  "both ssl opts applied together",
			input: base,
			opts:  PoolOpts{SSLMode: "verify-ca", SSLRootCert: "/etc/ssl/ca.pem"},
			wantQuery: map[string]string{
				"sslmode":     "verify-ca",
				"sslrootcert": "/etc/ssl/ca.pem",
			},
		},
		{
			name:      "MaxConns and ConnMaxLifetime do not touch URL",
			input:     base,
			opts:      PoolOpts{MaxConns: 50, ConnMaxLifetime: 10 * time.Minute},
			wantQuery: nil, // no overlay → original returned as-is
		},
		{
			name:    "malformed URL surfaces parse error",
			input:   "://not a url",
			opts:    PoolOpts{SSLMode: "require"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := overlayURL(tc.input, tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (out=%q)", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Empty opts path returns the original string verbatim.
			if tc.wantQuery == nil {
				if out != tc.input {
					t.Errorf("expected URL unchanged:\n got: %q\nwant: %q", out, tc.input)
				}
				return
			}

			u, err := url.Parse(out)
			if err != nil {
				t.Fatalf("parse output URL %q: %v", out, err)
			}
			q := u.Query()
			for k, want := range tc.wantQuery {
				if got := q.Get(k); got != want {
					t.Errorf("query[%q]: got %q, want %q (full URL: %s)", k, got, want, out)
				}
			}

			// User-info, host, path preserved.
			if !strings.HasPrefix(out, "postgres://hermes:pw@host:5432/hermes") {
				t.Errorf("output does not preserve userinfo/host/path: %s", out)
			}
		})
	}
}
