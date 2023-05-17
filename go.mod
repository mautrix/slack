module go.mau.fi/mautrix-slack

go 1.18

require (
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.16
	github.com/slack-go/slack v0.10.3
	github.com/yuin/goldmark v1.5.4
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.15.2
)

require (
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/rs/zerolog v1.29.1 // indirect
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.2 // indirect
	golang.org/x/crypto v0.9.0 // indirect
	golang.org/x/exp v0.0.0-20230510235704-dd950f8aeaea
	golang.org/x/net v0.10.0 // indirect
	golang.org/x/sys v0.8.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20230503153722-8b0e7827de67
