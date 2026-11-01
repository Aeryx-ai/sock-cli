module github.com/aeryx-ai/sock-cli

go 1.26.9

replace github.com/aeryx-ai/sock-contract => ../contract

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/spf13/cobra v1.10.2
)

require (
	connectrpc.com/connect v1.21.0
	github.com/aeryx-ai/sock-contract v0.5.0
	github.com/guygrigsby/sock-client v0.0.0
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	google.golang.org/protobuf v1.36.12
)

replace github.com/guygrigsby/sock-client => ../client
