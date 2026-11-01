# Contributor and agent guide

`sock` is the command line client for Sock: one Go binary in `cmd/sock`, commands in `internal/cli`, config file handling in `internal/config`, a fake hub for tests in `internal/socktest`. It talks to the hub through `github.com/aeryx-ai/sock-client` and the generated API in `github.com/aeryx-ai/sock-contract`.

## Build and test

    make            # build bin/sock
    make test       # go test -race ./...
    make check      # what CI runs: tidy, gofmt, vet, golangci-lint, licenses, tests

`make check` must pass before every commit.

`go.mod` pins `sock-contract` and `sock-client` by tag and the Go proxy fetches them. To build against local checkouts, use an untracked `go.work` (gitignored). Bump a pin in the change that starts using what the new tag adds.

## Conventions

- Standard Go style, `gofmt` clean, lint config in `.golangci.yml`.
- Use integer `for range` loops (`for i := range n`), not three clause loops for a plain count.
- A bug fix starts with a test that fails without it.
- Secrets (session token, puppet token, machine key, access gate) are never printed except by a command that exists to print one, and a human session token never reaches an `--exec` child.
- Commit messages: short, imperative, one logical change each.
