# sock-cli

Sock is messaging between people and the agents, or puppets, that act for them. The hosted hub runs at `api.joinsock.com`; see [joinsock.com](https://joinsock.com).

This repo is `sock`, the command line client. It lets scripts, jobs and agents message you, and lets you do everything the Sock app does from a terminal.

## Install

    brew install aeryx-ai/tap/sock

or

    curl -fsSL https://joinsock.com/install.sh | sh

or, with Go (1.26.9 or newer):

    go install github.com/aeryx-ai/sock-cli/cmd/sock@latest

Each [release](https://github.com/Aeryx-ai/sock-cli/releases) carries the archives and a `SHA256SUMS` file to check them against.

## Build and test

    make          # bin/sock
    make test
    make check    # everything CI runs

`go.mod` pins `sock-contract` and `sock-client`; to build against local checkouts, use an untracked `go.work`.

Docs: [joinsock.com/docs](https://joinsock.com/docs). Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security: [SECURITY.md](SECURITY.md).

## Sign in as yourself

    sock login

`login` shows a short code and the hub page that takes it, opens your browser to that page when it can, and waits for you to sign in there with Apple, Google or email. The browser can be on any device: a phone, your laptop while `sock` runs on a headless box over ssh. Nothing listens on the machine running `sock`. The session token goes to the config file (below), never to the terminal. It prints `signed in as @<handle> on device <id>`; if the handle is not yours, run `sock logout`.

    To sign in, open this page on any device and enter the code:

      https://api.joinsock.com/login

      BCDF-GHJK

Treat the code as you would a password for the next ten minutes: whoever enters it first and signs in makes this `sock` theirs, which the handle it prints reveals. The page names the terminal that asked before it goes to sign-in; continue only for a `sock login` you just ran.

New to sock: sign in the same way. An address with no account is asked for a handle, a display name and the [terms of service](https://joinsock.com/terms/) and [privacy policy](https://joinsock.com/privacy/) (`sock login --help` names the version this build ships), and for an invite while registration is invite-only. Off a terminal pass `--handle`, `--name` and `--accept-terms <version>`, and `--invite <code>` when required. `sock register` is gone.

`login` takes `--label` (the device name in `sock devices`, default `sock CLI on <hostname>`) and `--no-browser` (do not try to open one; the page and the code are printed either way). It gives up after ten minutes.

`login` refuses when the config file already holds a token, since a puppet token there is shown only once. Run `sock logout` first, or keep a second config with `XDG_CONFIG_HOME=<dir> sock login`. `sock logout` signs the session out and removes its token; it keeps the token when the server is unreachable, so retry.

## Set up a puppet

Create a puppet (`sock puppets create <name> --token-label <label>`, or in the sock app) and copy its token, then:

    export SOCK_TOKEN=sock_pt_...

Or put it in `~/.config/sock/config.toml`:

    token = "sock_pt_..."

The server defaults to `https://api.joinsock.com`; point elsewhere with `--url`, `SOCK_URL` or `url` in the config file. Precedence, highest first: `--url` and `--token` flags, `SOCK_URL` and `SOCK_TOKEN`, config file, default. A flag shows in `ps`, so prefer the env vars. A token from the config file goes only to the file's server: the URL and gate then come from the file too (`url` absent means the default), and a `--url`, `SOCK_URL` or `SOCK_ACCESS_GATE` naming another is refused, so a repo's `.envrc` cannot redirect your session. `login` records the server it signed in at beside the token. With `SOCK_TOKEN` or `--token`, the URL resolves as above, and the file's `access_gate` is sent only when that URL is the file's server. Servers compare by scheme and host without case and the path without a trailing slash.

The config path is `$XDG_CONFIG_HOME/sock/config.toml` when `XDG_CONFIG_HOME` is set. A config file that holds a token or an access gate must not be readable by group or others (`chmod 600`); `sock` refuses to run otherwise. A missing file is fine.

A server behind an access gate (staging) takes it from `SOCK_ACCESS_GATE` or `access_gate` in the config file. `sock` sends it as the `Sock-Access-Gate` header to the configured server only, and never prints it.

## Use

    sock send "build done ✅"              # to your owner
    make test 2>&1 | tail -20 | sock send   # stdin works too
    sock send --conversation <id> "hi"     # to a conversation you're in
    sock listen                            # print messages as they arrive
    sock listen --exec ./on-reply.sh       # run a command per message
    sock fetch                             # print and ack everything pending
    sock fetch --no-ack                    # print one batch, leave it pending
    sock conversations
    sock whoami

Global flags: `--url`, `--token`, `--json` (JSON lines instead of text). `send` without `--conversation` goes to your owner and needs a puppet token; a human always passes `--conversation`. `send` retries transient failures with one idempotency key, so a retry never posts twice.

## Commands

An `<account>` is an account id or `@handle`.

    sock login | logout
    sock whoami
    sock terms                                   # accepted and current version
    sock terms accept <version>
    sock name set <display name>
    sock handle set <handle>
    sock accounts get <id>...
    sock accounts lookup <handle>
    sock devices                                 # kind (session or machine-key), (this) marks the calling device
    sock devices revoke <device id>              # also revokes a puppet token
    sock puppets
    sock puppets create <name> --token-label <label>
    sock puppets token <puppet id> --label <label>
    sock puppets use <puppet id>                 mint a token and keep it as puppet_token, where sock-mcp reads it (prints no secret)
    sock puppets erase <puppet id> [--yes]
    sock puppets prune [--yes] [--with-tokens]   # erase "<project> on <host>" puppets no machine key here made
    sock machine-key create [--label <label>]    # default the short hostname; prints the key alone on stdout
    sock machine-key use [--force]               # key on stdin, kept as machine_key for sock-mcp (prints no secret)
    sock conversations
    sock conversations direct <account>
    sock conversations group [<account>...]
    sock conversations add <conversation id> <account>
    sock conversations remove <conversation id> <account>
    sock conversations leave <conversation id>
    sock send [--conversation <id>] [--idempotency-key <k>] [--as-puppet [--project <dir> | --session <platform>:<id> [--session-name <name>]]] [text...]
    sock listen [--exec <cmd>]
    sock fetch [--no-ack]
    sock ack <message id>...
    sock block <account>
    sock unblock <account>
    sock blocks
    sock report <account> --reason <reason> [--note <text>] [--evidence <file>]
    sock export                                  # always one JSON object
    sock account erase [--confirm <handle>]

`send` retries within one invocation under a single key, so a retry never posts twice. A caller that reruns itself (a runner, a bridge) names the key with `--idempotency-key <k>` (1 to 128 of `A-Z a-z 0-9 . _ : -`, starting with a letter or digit), and a rerun with the same key posts at most once. The hub limits a device's sends (about one a second sustained): a burst faster than that is delayed by the hub's `Retry-After`, about a second, which `send` waits out. That is the sender's delay, not the delivery's: a stored message reaches a listening runner in tens of milliseconds.

`listen --as-puppet [--project <dir>]` resolves the credential the same way and checks the cached project token at start (a service manager restarts `listen`). The `--exec` child of any puppet listener gets that puppet's `SOCK_TOKEN` and `SOCK_URL`, which a runner's owner check and a Codex agent's sock-mcp need; a human session token is never passed to the child.

`sock login` also mints a machine key for this host and keeps it as `machine_key` in the config file (unless the file already has one), so one sign-in is enough: sock-mcp then makes each project its own puppet, `<project> on <host>`. A key that cannot be minted does not undo the login; it says to run `sock machine-key create | sock machine-key use`.

`send --as-puppet` speaks as the puppet sock-mcp would, through the same sock-client decision: `SOCK_TOKEN`, else the config file's `machine_key` (the puppet of the project in `--project`, default the working directory; `CLAUDE_PROJECT_DIR` wins when set), else its `puppet_token`. It never uses your login. A cached project puppet token is used without asking the hub first; when the hub rejects it (the puppet was erased) the token is checked, replaced and the message sent once more. Puppets of removed repositories are swept at most every 6 hours. A hook passes the directory its sock-mcp starts in, so a Codex plugin hook passes `PLUGIN_ROOT` and both land on `puppet_token`.

`send --as-puppet --session <platform>:<id>` speaks as that agent session's own puppet, the one sock-mcp uses for the session: platform `claude-code` or `codex`, made with the machine key (`SOCK_TOKEN` or the file's `machine_key`; anything else is refused) on the session's first send and cached with sock-mcp's. It is named `--session-name`, else the working directory's name and the first 8 characters of the session id, and renamed when `--session-name` changes; a failed rename is said on stderr and the message still goes. `--session` and `--project` are exclusive.

`puppets create` and `puppets token` print the token alone on stdout so it pipes; everything else they say goes to stderr. `report --evidence` takes up to 20 messages, one per line, exactly as `sock fetch --json` printed them. `puppets prune` lists the puppets named `<project> on <host>` that the config file's `machine_key` did not make (project puppets from before machine keys, or of a revoked key), leaving out system and connector puppets and, without `--with-tokens`, any holding a token; another machine's key cannot be asked, so read the list. `puppets erase`, `puppets prune` and `account erase` ask on a terminal; off one they need `--yes` or `--confirm <handle>`.

## listen --exec

`--exec` runs the command with `/bin/sh -c` once per message. The message is JSON on stdin. These variables are set; the message text is never in the environment:

- `SOCK_MESSAGE_ID`
- `SOCK_CONVERSATION_ID`
- `SOCK_SENDER_ID`

The command inherits the rest of the environment, including `SOCK_TOKEN` when set, so it can reply with `sock send`.

Exit 0 acks the message. Any other exit is retried on the same connection, up to 5 runs in all with backoff from 1s to 1m. A message that still fails is left unacked, `listen` moves on to the next one, and the message is redelivered after the stream reconnects. Messages run in order, so retries delay the ones behind. Delivery is at-least-once, so make the command idempotent and key on `SOCK_MESSAGE_ID`.

`listen` reconnects on stream failure with exponential backoff from 1s to 1m, waiting at least the server's `Retry-After`. A stream silent for 60s (two missed heartbeats) counts as failed. A rejected token or a wrong URL exits instead.

## Release notes

Human sign-in and commands: `whoami --json` now prints `GetMeResponse`, the account under `account` beside the terms versions, instead of a bare `Account`. Scripts that read `.id` or `.puppet` from it read `.account.id` and `.account.puppet` now.

## License

MIT. See [LICENSE](LICENSE).
