module go.mau.fi/mautrix-slack

go 1.22

require (
	github.com/lib/pq v1.10.9
	github.com/rs/zerolog v1.33.0
	github.com/slack-go/slack v0.10.3
	github.com/stretchr/testify v1.9.0
	github.com/yuin/goldmark v1.7.4
	go.mau.fi/util v0.6.1-0.20240719175439-20a6073e1dd4
	golang.org/x/net v0.27.0
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.19.1-0.20240730133608-779f61ac9c69
)

require (
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rs/xid v1.5.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.17.1 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.3 // indirect
	golang.org/x/crypto v0.25.0 // indirect
	golang.org/x/exp v0.0.0-20240707233637-46b078467d37 // indirect
	golang.org/x/sys v0.22.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20240727084049-0bd52ec9575e
