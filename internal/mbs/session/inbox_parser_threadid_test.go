package session

import (
	"testing"

	"mbs-native/fb"
)

func TestDeriveThreadID(t *testing.T) {
	tests := []struct {
		name string
		msg  fb.Message
		want string
	}{
		{
			name: "outbound echo prefers OTID",
			msg:  fb.Message{OTID: "1234567890123456789", SenderURL: "fb://profile/9999"},
			want: "1234567890123456789",
		},
		{
			name: "inbound falls back to profile id",
			msg:  fb.Message{OTID: "", SenderURL: "fb://profile/1674772559"},
			want: "1674772559",
		},
		{
			name: "inbound profile id with query string",
			msg:  fb.Message{SenderURL: "fb://profile/1674772559?foo=bar&baz=1"},
			want: "1674772559",
		},
		{
			name: "inbound profile id with trailing path",
			msg:  fb.Message{SenderURL: "fb://profile/1674772559/extra"},
			want: "1674772559",
		},
		{
			name: "no OTID, non-profile URL → empty (un-keyable)",
			msg:  fb.Message{SenderURL: "https://example.com/x"},
			want: "",
		},
		{
			name: "nothing → empty",
			msg:  fb.Message{},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveThreadID(tc.msg); got != tc.want {
				t.Fatalf("deriveThreadID(%+v) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

func TestProfileIDFromURL(t *testing.T) {
	cases := map[string]string{
		"fb://profile/123":              "123",
		"fb://profile/123?x=1":          "123",
		"fb://profile/123#frag":         "123",
		"fb://profile/123/more":         "123",
		"fb://profile/":                 "",
		"https://facebook.com/profile/1": "",
		"":                              "",
		"garbage":                       "",
	}
	for in, want := range cases {
		if got := profileIDFromURL(in); got != want {
			t.Errorf("profileIDFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
