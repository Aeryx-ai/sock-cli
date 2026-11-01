# Contributing

`sock` is the command line client for Sock: one Go binary in `cmd/sock`, commands in `internal/cli`, config file handling in `internal/config` and a fake hub for tests in `internal/socktest`. It talks to the hub through [sock-client](https://github.com/Aeryx-ai/sock-client) and the generated API in [sock-contract](https://github.com/Aeryx-ai/sock-contract).

## Before you start

- Open an issue before anything beyond a small fix (a typo, a doc correction, an obvious bug). New commands, flags and output changes need agreement first.
- Report vulnerabilities privately as [SECURITY.md](SECURITY.md) describes. Never in an issue, pull request or discussion.

## Dev setup

- Go 1.26.9 or newer (`go.mod`). CI reads the version from `go.mod`.
- golangci-lint v2.13.2, the version CI installs:

  ```sh
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
  ```

- `zip` and `tar`, only for `make dist`.

Linters beyond the defaults are set in `.golangci.yml`: `errorlint`, `intrange`, `misspell`, `revive`, `unparam`. The license check runs `go-licenses` v2.0.1 through `go run`, so there is nothing to install for it.

| Target | Does |
|---|---|
| `make` / `make build` | Builds `bin/sock` with the version from `git describe` |
| `make test` | `go test -race ./...` |
| `make install` | `go install` of `./cmd/sock` with the version stamped |
| `make tidy` | Fails if `go.mod` or `go.sum` need `go mod tidy` (runs with `GOWORK=off`) |
| `make fmt` | Fails if `gofmt -l .` lists any file |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make licenses` | Fails if any dependency has a forbidden or restricted license |
| `make check` | `tidy`, `fmt`, `vet`, `lint`, `licenses`, `test` |
| `make dist` | Cross-builds the release archives into `bin/dist` (see below) |
| `make e2e-selfreg` | Self-registration test against a local Sock dev hub. Needs the hub, Docker and a `sock-mcp`; not part of `check` |

`make redeploy` prints a note: there is nothing to deploy.

## Working across repos

`go.mod` pins sock-contract and sock-client to public tags and the Go module proxy fetches them. To develop against local checkouts, use an untracked `go.work` (already in `.gitignore`):

```sh
go work init .
go work use ../sock-client ../sock-contract
```

Never commit a `go.work` or a `replace` directive. CI has neither, so before opening a PR check that the change builds against the pinned tags:

```sh
GOWORK=off make check
```

If the change needs something not yet in a client or contract tag, that change ships and gets tagged upstream first. Then bump the pin in the change that first uses it:

```sh
go get github.com/aeryx-ai/sock-client@vX.Y.Z
go mod tidy
```

## Making a change

1. Fork, then branch from `main`.
2. Write the test with the code. A bug fix starts with a test that fails without the fix. Command tests run against the fake hub in `internal/socktest` and with the race detector (`make test`).
3. Output formatting and input parsing have fuzz tests in `internal/cli/output_fuzz_test.go`. Extend them when you change that code and run the target you touched, one at a time:

   ```sh
   go test -run '^$' -fuzz '^FuzzPrinterJSON$' -fuzztime 60s ./internal/cli
   ```

   A failing input is written under `internal/cli/testdata/fuzz/<FuzzName>/`. Fix the bug and commit that file: plain `go test` replays it from then on.
4. A new dependency must pass `make licenses` and leave `make tidy` clean.
5. Run `make check`. It must pass. CI runs the same target.
6. Keep a PR to one change.

CI skips pull requests that touch only Markdown or `docs/`.

## Flags and output are a user contract

Scripts, cron jobs and agents call `sock` and parse what it prints. Treat the command line as an API:

- Do not rename or remove a command or flag, or change its meaning. Add a new one and keep the old one working.
- `--json` prints JSON lines. Adding a field is fine; renaming, removing or retyping one breaks callers. `sock fetch --json` lines are also the input format of `--evidence`, so keep the two in step.
- Human readable text output may change, but keep exit codes and stderr versus stdout stable.
- `sock --version` prints the version stamped from the tag; the Homebrew formula's test depends on it.
- Secrets (session token, puppet token, machine key, access gate) are never printed except by a command that exists to print one, and a human session token never reaches an `--exec` child.

## Cross builds

`make dist` builds static (`CGO_ENABLED=0`, `-trimpath`) binaries for `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64` and `windows-amd64` in parallel and packs each with `LICENSE` as `bin/dist/sock-<os>-<arch>.tar.gz` (`.zip` on Windows). Run it when you touch platform specific code; the release workflow runs the same target.

## Style

- gofmt, `go vet` and golangci-lint clean.
- `for i := range n` rather than a three clause loop for a plain count.
- Wrap errors with context.

## Commit messages

- Imperative subject, short (about 50 characters, 72 at most), no trailing period: `Add --since to sock fetch`.
- A body that explains why, not what the diff already shows.
- One logical change per commit.
- Signed commits are encouraged: `git commit -S` or `git config commit.gpgsign true`. GPG and SSH keys both work.

## Pull requests

- Link the issue it resolves.
- Say what changed and why. Call out any change to commands, flags, `--json` output or exit codes.
- Include the tests.
- CI must be green before review. A maintainer reviews every PR. Push review fixes as new commits; the maintainer may squash on merge.

## Releases (maintainers)

A release is a signed annotated `vX.Y.Z` tag on `main`, with the version as its message:

```sh
git tag -s v0.3.1 -m v0.3.1
git push origin v0.3.1
```

The tag runs `.github/workflows/release.yml`:

1. `gate` refuses any tag not shaped `vX.Y.Z`.
2. `build` runs `make dist`.
3. `publish` uploads the five archives to `downloads.joinsock.com` and updates `latest/cli-<os>-<arch>.json`. A re-run reuses files already published.
4. `tap` runs `scripts/update.sh formula sock <version>` in [homebrew-tap](https://github.com/Aeryx-ai/homebrew-tap) and pushes the regenerated `Formula/sock.rb`.

Tag only a `main` that passes `make check`.

## License

MIT, copyright Aeryx Industries LLC ([LICENSE](LICENSE)). Inbound = outbound: by contributing you agree your contribution is licensed under the same terms.
