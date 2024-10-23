module go.mau.fi/mautrix-slack

go 1.22.0

toolchain go1.23.2

require (
	github.com/lib/pq v1.10.9
	github.com/rs/zerolog v1.33.0
	github.com/slack-go/slack v0.13.1
	github.com/stretchr/testify v1.9.0
	github.com/yuin/goldmark v1.7.8
	go.mau.fi/util v0.8.2-0.20241018231932-9da45c4e6e04
	golang.org/x/net v0.30.0
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.21.2-0.20241023091249-d316a6b55f36
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/mattn/go-sqlite3 v1.14.24 // indirect
	github.com/petermattis/goid v0.0.0-20240813172612-4fcff4a6cae7 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.3 // indirect
	golang.org/x/crypto v0.28.0 // indirect
	golang.org/x/exp v0.0.0-20241009180824-f66d83c29e7c // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.26.0 // indirect
	golang.org/x/text v0.19.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20240930085310-1180a3fa18e0
