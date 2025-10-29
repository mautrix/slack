module go.mau.fi/mautrix-slack

go 1.24.0

toolchain go1.25.3

require (
	github.com/gabriel-vasile/mimetype v1.4.10
	github.com/lib/pq v1.10.9
	github.com/rs/zerolog v1.34.0
	github.com/slack-go/slack v0.16.0
	github.com/stretchr/testify v1.11.1
	github.com/yuin/goldmark v1.7.13
	go.mau.fi/util v0.9.2
	golang.org/x/net v0.46.0
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.25.3-0.20251029185846-0da017515743
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-sqlite3 v1.14.32 // indirect
	github.com/petermattis/goid v0.0.0-20250904145737-900bdf8bb490 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.2.0 // indirect
	golang.org/x/crypto v0.43.0 // indirect
	golang.org/x/exp v0.0.0-20251009144603-d2f985daa21b // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/sys v0.37.0 // indirect
	golang.org/x/text v0.30.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20250919162927-24f5c5701366
