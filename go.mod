module github.com/hermes-waba/hermes

go 1.25.0

require (
	github.com/coder/websocket v1.8.14
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.1
	github.com/nats-io/nats.go v1.50.0
	github.com/pquerna/otp v1.5.0
	github.com/prometheus/client_golang v1.23.2
	github.com/rs/zerolog v1.35.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	go.mau.fi/mautrix-meta v0.0.0-00010101000000-000000000000
	go.mau.fi/util v0.9.9-0.20260505143909-8e67f0d355e0
	go.mau.fi/whatsmeow v0.0.0-20260416104156-3ff20cd3462a
	golang.org/x/crypto v0.50.0
	golang.org/x/net v0.53.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260120221211-b8f7ae30c516
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
	maunium.net/go/mautrix v0.27.1-0.20260507135742-7ec18e08eac3
	mbs-native v0.0.0-00010101000000-000000000000
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/andybalholm/brotli v1.0.6 // indirect
	github.com/beeper/argo-go v1.1.2 // indirect
	github.com/beeper/poly1305 v0.0.0-20250815183548-d4eede7bbf3c // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/elliotchance/orderedmap/v3 v3.1.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/petermattis/goid v0.0.0-20260330135022-df67b199bc81 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/vektah/gqlparser/v2 v2.5.27 // indirect
	github.com/yuin/goldmark v1.8.2 // indirect
	go.mau.fi/libsignal v0.2.1 // indirect
	go.mau.fi/zeroconfig v0.2.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Stage E: hermes-mbs depends on the mbs-native client library and the
// patched mautrix-meta fork. Both are now private git submodules under
// third_party/ (repos EasyNV/hermes-mbs-native + EasyNV/hermes-mautrix-meta).
// Clone hermes with --recurse-submodules (or run git submodule update --init).
replace mbs-native => ./third_party/mbs-native

replace go.mau.fi/mautrix-meta => ./third_party/mautrix-meta-patched

// Stage F follow-up (2026-05-30): mbs-native/transport advertises Meta's
// proprietary 0xfb1a TLS-version codepoint in supported_versions for JA4
// fidelity. Upstream refraction-networking/utls rejects 0xfb1a when the
// server picks it back, with "tls: server selected unsupported protocol
// version fb1a". Our vendored fork at third_party/mbs-native/third_party/utls
// patches pickTLSVersion to alias 0xfb1a → VersionTLS13. The replace
// MUST live in the main module's go.mod — replace directives inside a
// replaced module (mbs-native/go.mod) are ignored by the build.
// See docs/research/mbs-fb1a-utls-fork-future-work.md.
replace github.com/refraction-networking/utls => ./third_party/mbs-native/third_party/utls
