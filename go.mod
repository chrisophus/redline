module github.com/chrisophus/redline

go 1.26

// The release everything here builds and lints with: the newest of the line
// the directive above names. `make lint-install` reads it, because
// golangci-lint has to be built with a Go at least as new as .golangci.yml's
// run.go, and a language version on its own could only name the .0 patch.
toolchain go1.26.8

require (
	github.com/anthropics/anthropic-sdk-go v1.71.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/sync v0.16.0 // indirect
)
