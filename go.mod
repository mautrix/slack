module go.mau.fi/mautrix-slack

go 1.21

require (
	github.com/beeper/libserv v0.0.0-20231231202820-c7303abfc32c
	github.com/gorilla/mux v1.8.0
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/rs/zerolog v1.32.0
	github.com/slack-go/slack v0.10.3
	github.com/yuin/goldmark v1.7.1
	go.mau.fi/util v0.4.2-0.20240318211948-d27d5a4cda9e
	golang.org/x/exp v0.0.0-20240314144324-c7f7c6466f7f
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.18.1-0.20240413105730-423d32ddf6d6
)

require (
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/rs/xid v1.5.0 // indirect
	github.com/tidwall/gjson v1.17.1 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.2 // indirect
	golang.org/x/crypto v0.21.0 // indirect
	golang.org/x/net v0.22.0 // indirect
	golang.org/x/sys v0.18.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20240418205721-1544a21c071f
