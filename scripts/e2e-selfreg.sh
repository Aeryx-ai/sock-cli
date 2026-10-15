#!/usr/bin/env bash
# Unattended self-registration test against the local dev hub (sock-server's
# `make dev-up`, files in ~/.config/sock-dev). A fresh HOME, the dev hub's
# seeded owner session standing in for a browser sign-in (the real Apple
# sign-in is a one-time human step on a new machine and is not part of any
# test), then:
#   machine key  ->  two projects  ->  two puppets  ->  messages reach the right one
#   ->  restart reuses the cache  ->  cleanup leaves the hub as found.
#
#   SOCK=bin/sock SOCK_MCP=$(command -v sock-mcp) scripts/e2e-selfreg.sh
set -euo pipefail

SOCK=${SOCK:-sock}
SOCK_MCP=${SOCK_MCP:-sock-mcp}
DEV=${SOCK_DEV_DIR:-$HOME/.config/sock-dev}
for f in url owner.device1.token seed.json; do
	[ -r "$DEV/$f" ] || { echo "e2e-selfreg: $DEV/$f missing: run make dev-up in sock-server first" >&2; exit 2; }
done
command -v python3 >/dev/null || { echo "e2e-selfreg: python3 is required" >&2; exit 2; }

URL=$(cat "$DEV/url")
OWNER_TOKEN=$(cat "$DEV/owner.device1.token")
W=$(mktemp -d "${TMPDIR:-/tmp}/sock-selfreg.XXXXXX")
chmod 700 "$W"
export HOME=$W/home XDG_CONFIG_HOME=$W/home/.config XDG_STATE_HOME=$W/home/.local/state
mkdir -p "$HOME"
unset SOCK_TOKEN SOCK_ACCESS_GATE CLAUDE_PROJECT_DIR PLUGIN_ROOT
export SOCK_URL=$URL

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

pids=()
cleanup() {
	for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null || true; done
	# leave the hub as found: erase the puppets and revoke the machine key this run made
	if [ -n "${OWNER_TOKEN:-}" ]; then
		for id in $(SOCK_TOKEN=$OWNER_TOKEN "$SOCK" puppets --json 2>/dev/null | python3 -c '
import json,sys
for l in sys.stdin:
    l=l.strip()
    if not l: continue
    d=json.loads(l)
    a=d.get("puppet",{})
    if str(a.get("displayName","")).endswith(" on '"$(hostname -s)"'") and "selfreg-" in str(a.get("displayName","")):
        print(a["id"])' 2>/dev/null || true); do
			SOCK_TOKEN=$OWNER_TOKEN "$SOCK" puppets erase "$id" --yes >/dev/null 2>&1 || true
		done
		if [ -n "${KEY_DEVICE:-}" ]; then SOCK_TOKEN=$OWNER_TOKEN "$SOCK" devices revoke "$KEY_DEVICE" >/dev/null 2>&1 || true; fi
	fi
	rm -rf "$W"
}
trap cleanup EXIT

owner() { SOCK_TOKEN=$OWNER_TOKEN "$SOCK" "$@"; }

# 1. "sock login", simulated: the owner's session in the config file, as login writes it
mkdir -p "$XDG_CONFIG_HOME/sock"
printf 'url = "%s"\ntoken = "%s"\n' "$URL" "$OWNER_TOKEN" >"$XDG_CONFIG_HOME/sock/config.toml"
chmod 600 "$XDG_CONFIG_HOME/sock/config.toml"
# the machine key login keeps (login does this itself; browserless here, so the same two commands)
KEY_OUT=$("$SOCK" machine-key create --label "selfreg-$$" 2>"$W/key.err" | "$SOCK" machine-key use 2>&1) || fail "machine key: $KEY_OUT"
KEY_DEVICE=$(sed -n 's/^device \([0-9a-f-]\{36\}\)$/\1/p' "$W/key.err" | head -1)
[ -n "$KEY_DEVICE" ] || fail "no device id from machine-key create"
grep -q '^machine_key = ' "$XDG_CONFIG_HOME/sock/config.toml" || fail "no machine_key in the config file"
ok "machine key kept in the config file (mode $(stat -f '%Lp' "$XDG_CONFIG_HOME/sock/config.toml" 2>/dev/null || stat -c '%a' "$XDG_CONFIG_HOME/sock/config.toml"))"
OWNER_TOKEN=$(cat "$DEV/owner.device1.token")

# 2. two projects
P1=$W/selfreg-alpha
P2=$W/selfreg-beta
for p in "$P1" "$P2"; do mkdir -p "$p" && git -C "$p" init -q; done

# whoami as sock-mcp would see it in a project (no SOCK_TOKEN: the machine key, then the project's puppet)
mcp_whoami() {
	CLAUDE_PROJECT_DIR=$1 python3 - "$SOCK_MCP" <<'PY'
import json, subprocess, sys, os
p = subprocess.Popen([sys.argv[1], "--outbound-only"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, env=os.environ)
def send(o): p.stdin.write(json.dumps(o) + "\n"); p.stdin.flush()
send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}}})
p.stdout.readline()
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
send({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "sock_whoami", "arguments": {}}})
while True:
    line = p.stdout.readline()
    if not line: sys.exit("sock-mcp closed")
    m = json.loads(line)
    if m.get("id") == 2:
        r = m["result"]
        if r.get("isError"): sys.exit(r["content"][0]["text"])
        print(json.dumps(r["structuredContent"])); break
p.stdin.close(); p.terminate()
PY
}
field() { python3 -c 'import json,sys; print(json.loads(sys.argv[1])[sys.argv[2]])' "$1" "$2"; }

W1=$(mcp_whoami "$P1") || fail "sock-mcp in project 1: $W1"
W2=$(mcp_whoami "$P2") || fail "sock-mcp in project 2: $W2"
ID1=$(field "$W1" id); ID2=$(field "$W2" id)
N1=$(field "$W1" display_name); N2=$(field "$W2" display_name)
[ "$ID1" != "$ID2" ] || fail "two projects, one puppet ($ID1)"
case $N1 in selfreg-alpha\ on\ *) ;; *) fail "project 1 puppet is called '$N1'" ;; esac
case $N2 in selfreg-beta\ on\ *) ;; *) fail "project 2 puppet is called '$N2'" ;; esac
ok "two projects, two puppets: '$N1' and '$N2'"

# 3. the cache is private and survives a restart: the same puppet, no new one
STATE=$XDG_STATE_HOME/sock-integrations/puppets
[ -d "$STATE" ] || fail "no puppet cache at $STATE"
bad=$(find "$XDG_STATE_HOME/sock-integrations" -type f \( -perm -g+r -o -perm -o+r \) | head -1)
[ -z "$bad" ] || fail "a cache file is readable by others: $bad"
R1=$(mcp_whoami "$P1") || fail "restart in project 1: $R1"
[ "$(field "$R1" id)" = "$ID1" ] || fail "restart minted another puppet for project 1"
n=$(owner puppets --json | python3 -c 'import sys; print(sum(1 for l in sys.stdin if "selfreg-" in l))')
[ "$n" = 2 ] || fail "$n selfreg puppets after a restart, want 2"
ok "restart reuses the cache: still two puppets, files private"

# 4. messages reach the right puppet only
conv() { owner conversations direct "$1" --json 2>/dev/null | python3 -c 'import json,sys; print(json.loads(sys.stdin.readline())["id"])'; }
C1=$(conv "$ID1"); C2=$(conv "$ID2")
[ -n "$C1" ] && [ -n "$C2" ] && [ "$C1" != "$C2" ] || fail "no distinct directs ($C1, $C2)"
listen_to() { # project outfile
	"$SOCK" listen --as-puppet --project "$1" --exec "cat >> '$2'; echo >> '$2'" >/dev/null 2>"$2.err" &
	pids+=($!)
}
listen_to "$P1" "$W/got1"; listen_to "$P2" "$W/got2"
sleep 2
owner send --conversation "$C1" "for-alpha-$$" >/dev/null
owner send --conversation "$C2" "for-beta-$$" >/dev/null
for i in $(seq 40); do
	[ -s "$W/got1" ] && [ -s "$W/got2" ] && break
	sleep 0.25
done
grep -q "for-alpha-$$" "$W/got1" || fail "alpha did not get its message"
grep -q "for-beta-$$" "$W/got2" || fail "beta did not get its message"
! grep -q "for-beta-$$" "$W/got1" || fail "alpha received beta's message"
! grep -q "for-alpha-$$" "$W/got2" || fail "beta received alpha's message"
ok "each puppet received only its own message"

echo "PASS: self-registration, two puppets, routing, cache"
