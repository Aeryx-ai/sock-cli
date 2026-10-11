# sock-cli

`sock` lets scripts, jobs and agents message you on [sock](https://github.com/guygrigsby/sock-contract).

## Setup

Create a puppet in the sock app and copy its token, then:

    export SOCK_TOKEN=sock_pt_...

Or put it in `~/.config/sock/config.toml`:

    token = "sock_pt_..."

The server defaults to `https://api.sock.aeryx.ai`; point elsewhere with `--url`, `SOCK_URL` or `url` in the config file. Precedence, highest first: `--url` and `--token` flags, `SOCK_URL` and `SOCK_TOKEN`, config file, default. A flag shows in `ps`, so prefer the env vars.

The config path is `$XDG_CONFIG_HOME/sock/config.toml` when `XDG_CONFIG_HOME` is set. A config file that holds a token must not be readable by group or others (`chmod 600`); `sock` refuses to run otherwise. A missing file is fine.

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

Global flags: `--url`, `--token`, `--json` (JSON lines instead of text). `send` without `--conversation` goes to your owner and needs a puppet token. `send` retries transient failures with one idempotency key, so a retry never posts twice.

## listen --exec

`--exec` runs the command with `/bin/sh -c` once per message. The message is JSON on stdin. These variables are set; the message text is never in the environment:

- `SOCK_MESSAGE_ID`
- `SOCK_CONVERSATION_ID`
- `SOCK_SENDER_ID`

The command inherits the rest of the environment, including `SOCK_TOKEN` when set, so it can reply with `sock send`.

Exit 0 acks the message. Any other exit leaves it unacked and `listen` keeps going with the next one. The unacked message is not retried on the same connection: it is redelivered after the stream reconnects. Delivery is at-least-once, so make the command idempotent and key on `SOCK_MESSAGE_ID`.

`listen` reconnects on stream failure with exponential backoff from 1s to 1m, waiting at least the server's `Retry-After`. A stream silent for 60s (two missed heartbeats) counts as failed. A rejected token or a wrong URL exits instead.

MIT licensed.
