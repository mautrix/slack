module github.com/mautrix/slack

go 1.18

require (
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.6
	github.com/mattn/go-sqlite3 v1.14.13
	github.com/slack-go/slack v0.10.3
	github.com/yuin/goldmark v1.4.12
	maunium.net/go/maulogger/v2 v2.3.2
	maunium.net/go/mautrix v0.11.1-0.20220522190042-ec20c3fc994a
)

require (
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/tidwall/gjson v1.14.1 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.4 // indirect
	golang.org/x/crypto v0.0.0-20220513210258-46612604a0f9 // indirect
	golang.org/x/net v0.0.0-20220513224357-95641704303c // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

//replace go.mau.fi/mautrix-slack => github.com/mautrix/slack main
