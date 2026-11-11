# shellcheck shell=sh
# shellcheck disable=SC2154,SC2034 # the settings the caller sets, listed below, and those runner_config sets for it
# Shared by runner/sock-runner, runner/sock-bridge and runner/sock-http, the commands
# `sock listen --exec` runs once per message (docs/specs/integrations.md). Source
# it: it defines functions and runs nothing. A caller sets
#   cmd_name     sock-runner or sock-bridge, for messages
#   state_dir    the state directory
#   log_name     runner.log   lock_name  runner.lock   ledger_name  done.ids
#   timeout      seconds one run may take (also bounds the wait for the lock)
# then calls lib_init, and in this order: install_traps, read_message,
# owner_gate, drop_if_done, take_lock, drop_if_done.
#
# Exit 0 acknowledges a message (handled, or dropped as not the owner's or
# malformed), 1 leaves it pending for a retry, 2 is a configuration error.

UUID='[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'

die_config() {
	echo "$cmd_name: $*" >&2
	exit 2
}

# matches VALUE ERE: the whole value matches. A value with a newline never
# counts: grep -x would match any one of its lines.
matches() {
	case $1 in
	*'
'*) return 1 ;;
	esac
	printf '%s\n' "$1" | grep -Eqx "$2"
}
is_uuid() { matches "$1" "$UUID"; }

now() { date +%s; }

# --- sock-runner's settings ---------------------------------------------------
# runner_config reads and checks them (agent, session, workdir, mode, model
# and own_session), exiting 2 on a bad one. sock-runner-service runs it at install,
# so a unit never starts with a configuration the runner would refuse.
runner_config() {
	agent=${SOCK_RUNNER_AGENT:-}
	session=${SOCK_RUNNER_SESSION:-}
	workdir=${SOCK_RUNNER_WORKDIR:-}
	mode=${SOCK_RUNNER_PERMISSION:-}

	case $agent in
	claude | codex) ;;
	*) die_config "SOCK_RUNNER_AGENT must be claude or codex" ;;
	esac
	# Unset, claude runs in a session the runner made and keeps in the state
	# directory (own_session); codex has no way to name a new session up front.
	own_session=
	if [ -z "$session" ]; then
		[ "$agent" = claude ] || die_config "SOCK_RUNNER_SESSION is required for codex"
		own_session=1
	else
		printf '%s\n' "$session" | grep -Eqx '[A-Za-z0-9_][A-Za-z0-9._-]{0,127}' ||
			die_config "SOCK_RUNNER_SESSION must be 1 to 128 characters of A-Z a-z 0-9 . _ -, not starting with . or -"
	fi
	[ -n "$workdir" ] && [ -d "$workdir" ] || die_config "SOCK_RUNNER_WORKDIR must be an existing directory"
	# exactly the modes each agent has: nothing else reaches its argv
	case "$agent:$mode" in
	claude:acceptEdits | claude:default | claude:dontAsk | claude:manual) ;;
	claude:plan) die_config "plan mode blocks sock_send even when it is allowed (the model says it could not send and claude still exits 0): use default or dontAsk" ;;
	codex:read-only | codex:workspace-write) ;;
	claude:auto | claude:bypassPermissions | codex:danger-full-access)
		[ "${SOCK_RUNNER_ALLOW_BYPASS:-}" = 1 ] || die_config "$mode needs SOCK_RUNNER_ALLOW_BYPASS=1"
		;;
	*) die_config "SOCK_RUNNER_PERMISSION must be one of the $agent modes, got '$mode'" ;;
	esac
	model=${SOCK_RUNNER_MODEL:-}
	[ -z "$model" ] || matches "$model" '[A-Za-z0-9][A-Za-z0-9._:-]{0,63}' ||
		die_config "SOCK_RUNNER_MODEL must be 1 to 64 characters of A-Z a-z 0-9 . _ : -, starting with a letter or digit"
	command -v "$agent" >/dev/null 2>&1 || die_config "$agent is not on PATH"
	command -v jq >/dev/null 2>&1 || die_config "jq is not on PATH"
	command -v sock >/dev/null 2>&1 || die_config "sock is not on PATH"
}

# the start time is compared as text: pin the locale and zone it is printed in
start_of() { LC_ALL=C TZ=UTC ps -o lstart= -p "$1" 2>/dev/null | tr -s ' ' | sed 's/^ //'; }

hash16() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -c1-16
}

# The prefixes of the temp files, named <prefix>.<pid> in the state directory.
tmp_prefixes='curl code hdr resp stream streamh body answer parts prompt new newc batch ids page hist histp turn turnn reason cfnew cfadd cfall'

# sweep_dead removes the temp files a run killed with SIGKILL (or a lost power)
# left, the bridge's curl config with its API key among them: those of pids that
# no longer exist, and any older than a run can last whatever its pid, since a
# pid can be reused. Legacy names (hist.<pid>.pages, new.<pid>.cut) count by
# their pid.
sweep_dead() {
	for p in $tmp_prefixes; do
		for f in "$state_dir/$p".*; do
			[ -e "$f" ] || continue
			pid=${f#"$state_dir/$p."}
			pid=${pid%.pages}
			pid=${pid%.cut}
			pid=${pid%.timeout}
			case $pid in
			'' | *[!0-9]*) continue ;;
			esac
			kill -0 "$pid" 2>/dev/null || rm -rf "$f"
		done
		find "$state_dir" -maxdepth 1 -name "$p.*" -mmin "+$((timeout / 60 + 2))" -exec rm -rf {} + 2>/dev/null || true
	done
}

# require_state_dir fails fast, as a configuration error, unless the state
# directory is this user's alone: owned by it, mode 700 and writable. A shared
# one would put the lock, the log and predictable temp names where others can
# reach them, and an unwritable one would spin the lock wait to the timeout.
require_state_dir() {
	[ -d "$state_dir" ] && [ -w "$state_dir" ] &&
		[ -n "$(find "$state_dir" -maxdepth 0 -user "$(id -u)" ! \( -perm -g+r -o -perm -g+w -o -perm -g+x -o -perm -o+r -o -perm -o+w -o -perm -o+x \) 2>/dev/null)" ] ||
		die_config "the state directory $state_dir is not writable, or cannot be made this user's alone (owner, mode 700)"
}

# lib_init makes the state directory, checks it and sweeps what a SIGKILLed run
# left.
lib_init() {
	# absolute, so a later cd (the runner's into its workdir) keeps every path. With
	# no start directory (deleted, so pwd fails) a relative one means nothing.
	case $state_dir in
	/*) ;;
	*)
		start=$(pwd -P 2>/dev/null) || start=
		case $start in
		/*) state_dir=$start/$state_dir ;;
		*) die_config "the state directory $state_dir is relative and the start directory is gone" ;;
		esac
		;;
	esac
	log=$state_dir/$log_name
	lock=$state_dir/$lock_name
	done_file=$state_dir/$ledger_name
	mkdir -p "$state_dir" 2>/dev/null && chmod 700 "$state_dir" 2>/dev/null
	require_state_dir
	sweep_dead
	msg_id=-
	prompt=
	mytoken=
}

# --- log: one line per message, never text, token, key or output -------------
log_line() {
	{
		if [ -f "$log" ] && [ "$(wc -c <"$log")" -gt 102400 ]; then : >"$log"; fi
		printf '%s id=%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$msg_id" "$*" >>"$log"
	} 2>/dev/null || true
}

# --- cleanup: the caller's own (extra_cleanup), the prompt file, our lock ----
# shellcheck disable=SC2329 # run by the EXIT trap
lib_cleanup() {
	[ -n "${nap_pid:-}" ] && kill "$nap_pid" 2>/dev/null
	if command -v extra_cleanup >/dev/null 2>&1; then extra_cleanup; fi
	[ -n "$prompt" ] && rm -f "$prompt" "$prompt.timeout"
	if [ -n "$mytoken" ] && [ "$(readlink "$lock" 2>/dev/null)" = "$mytoken" ]; then rm -f "$lock"; fi
}
# nap sleeps as a job waited on, so a TERM ends the run at once: a trap waits
# for a foreground command to finish, a sleep of the whole turn among them
nap() {
	sleep "$1" &
	nap_pid=$!
	wait "$nap_pid"
	nap_pid=
}
install_traps() {
	trap lib_cleanup EXIT
	trap 'exit 143' TERM INT HUP
}

# retry_after_seconds FILE prints the seconds a Retry-After header in a curl -D
# dump asks for (delta-seconds or an HTTP-date; negative for a past date), 999999
# for a number too long to count, and nothing when there is none or it is garbage.
retry_after_seconds() {
	ra=$(sed -n 's/^[Rr]etry-[Aa]fter: *//p' "$1" | tr -d '\r' | sed 's/ *$//' | head -n 1)
	case $ra in
	'') ;;
	*[!0-9]*)
		when=$(LC_ALL=C TZ=UTC date -j -f '%a, %d %b %Y %H:%M:%S GMT' "$ra" +%s 2>/dev/null ||
			LC_ALL=C date -u -d "$ra" +%s 2>/dev/null || echo "")
		case $when in
		'' | *[!0-9]*) ;;
		*) echo $((when - $(now))) ;;
		esac
		;;
	*)
		digits=$(printf '%s' "$ra" | sed 's/^0*//')
		case ${#digits} in
		0) echo 0 ;;
		[1-6]) echo "$digits" ;;
		*) echo 999999 ;;
		esac
		;;
	esac
}

# --- parse the message (protojson, whitespace randomized) --------------------
# read_message sets input, sender, stdin_id, stdin_conv and msg_id, or exits.
field() {
	printf '%s' "$input" | jq -r --arg k "$1" 'if (.[$k] | type) == "string" then .[$k] else empty end' 2>/dev/null
}
read_message() {
	input=$(cat)
	if ! printf '%s' "$input" | jq -e 'type == "object"' >/dev/null 2>&1; then
		log_line "dropped: malformed"
		exit 0
	fi
	sender=$(field senderId)
	stdin_id=$(field id)
	stdin_conv=$(field conversationId)
	if is_uuid "${SOCK_MESSAGE_ID:-}"; then msg_id=$SOCK_MESSAGE_ID; fi
	for v in "$sender" "$stdin_id" "$stdin_conv" "${SOCK_SENDER_ID:-}" "${SOCK_MESSAGE_ID:-}" "${SOCK_CONVERSATION_ID:-}"; do
		if ! is_uuid "$v"; then
			log_line "dropped: malformed"
			exit 0
		fi
	done
	if [ "$stdin_id" != "$SOCK_MESSAGE_ID" ] || [ "$stdin_conv" != "$SOCK_CONVERSATION_ID" ]; then
		log_line "dropped: malformed"
		exit 0
	fi
	if ! printf '%s' "$input" | jq -e '(.text | type) == "string"' >/dev/null 2>&1; then
		log_line "dropped: malformed"
		exit 0
	fi
	msg_id=$stdin_id
}

# --- the owner gate ----------------------------------------------------------
# The sender in the environment and on stdin must be one id, and be the
# puppet's owner or another puppet of that owner. The owner id is cached for a
# minute per token, and a sender's account for a minute per sender, so a
# stranger in a shared conversation costs one lookup a minute, not one per
# message. It also sets puppet_id, the puppet's own account id, cached beside
# the owner; a caller that sets need_puppet_id=1 takes no cache without it, and
# a whoami without it is not a puppet token. On the way out sender_from is
# "owner" or "puppet", and sender_name the puppet's display name, safe for a
# tag attribute.
sender_from=owner
sender_name=
owner_gate() {
	if [ "$sender" != "$SOCK_SENDER_ID" ]; then
		log_line "dropped: sender $SOCK_SENDER_ID"
		exit 0
	fi
	owner_cache=$state_dir/owner.$(printf '%s' "${SOCK_TOKEN:-}" | hash16)
	owner=
	puppet_id=
	if [ -f "$owner_cache" ]; then
		read -r cached_at cached_owner cached_puppet <"$owner_cache" 2>/dev/null || true
		is_uuid "${cached_puppet:-}" || cached_puppet=
		case ${cached_at:-} in
		'' | *[!0-9]*) ;;
		*) if [ $(($(now) - cached_at)) -lt 60 ] && is_uuid "${cached_owner:-}" &&
			{ [ -z "${need_puppet_id:-}" ] || [ -n "$cached_puppet" ]; }; then
			owner=$cached_owner
			puppet_id=$cached_puppet
		fi ;;
		esac
	fi
	if [ -z "$owner" ]; then
		if ! me=$(sock whoami --json 2>/dev/null); then
			log_line "whoami failed"
			exit 1
		fi
		owner=$(printf '%s' "$me" | jq -r 'if (.account.puppet.ownerId | type) == "string" then .account.puppet.ownerId else empty end' 2>/dev/null)
		puppet_id=$(printf '%s' "$me" | jq -r 'if (.account.id | type) == "string" then .account.id else empty end' 2>/dev/null)
		is_uuid "$puppet_id" || puppet_id=
		if ! is_uuid "$owner" || { [ -n "${need_puppet_id:-}" ] && [ -z "$puppet_id" ]; }; then
			# a human session token, or anything that is not a puppet: acknowledging
			# would consume that account's own deliveries and lose them, so this is a
			# configuration error: exit 2, nothing acknowledged
			echo "$cmd_name: SOCK_TOKEN is not a puppet token (sock whoami shows no puppet); nothing was acknowledged" >&2
			log_line "not a puppet token"
			exit 2
		fi
		printf '%s %s %s\n' "$(now)" "$owner" "$puppet_id" >"$owner_cache" 2>/dev/null || true
	fi
	[ "$sender" = "$owner" ] && return 0
	# another account: with SOCK_ACCEPT_PUPPETS=1 a puppet of the same owner passes; anything
	# else, and everything without the setting, is dropped. Off by default so two runners of one
	# owner in one conversation cannot answer each other without end.
	if [ "${SOCK_ACCEPT_PUPPETS:-}" != 1 ]; then
		log_line "dropped: sender $sender"
		exit 0
	fi
	sender_cache=$state_dir/sender.$(printf '%s' "$sender" | hash16)
	sender_owner=
	if [ -f "$sender_cache" ]; then
		read -r cached_at cached_owner cached_name <"$sender_cache" 2>/dev/null || true
		case ${cached_at:-} in
		'' | *[!0-9]*) ;;
		*) if [ $(($(now) - cached_at)) -lt 60 ] && is_uuid "${cached_owner:-}"; then
			sender_owner=$cached_owner
			sender_name=$(printf '%s' "${cached_name:-}" | LC_ALL=C tr -cd 'A-Za-z0-9 ._-' | cut -c1-64)
		fi ;;
		esac
	fi
	if [ -z "$sender_owner" ]; then
		if ! account=$(sock accounts get "$sender" --json 2>/dev/null); then
			log_line "account lookup failed"
			exit 1
		fi
		sender_owner=$(printf '%s' "$account" | jq -r 'if (.puppet.ownerId | type) == "string" then .puppet.ownerId else empty end' 2>/dev/null)
		# the name goes into a tag attribute: letters, digits, space, . _ and -, at most 64
		sender_name=$(printf '%s' "$account" | jq -r 'if (.displayName | type) == "string" then .displayName else "" end' 2>/dev/null |
			LC_ALL=C tr -cd 'A-Za-z0-9 ._-' | cut -c1-64)
		# an account that is no puppet is cached as owned by nobody, so it is dropped without a call
		is_uuid "$sender_owner" || sender_owner=00000000-0000-4000-8000-000000000000
		printf '%s %s %s\n' "$(now)" "$sender_owner" "$sender_name" >"$sender_cache" 2>/dev/null || true
	fi
	if [ "$sender_owner" != "$owner" ]; then
		log_line "dropped: sender $sender"
		exit 0
	fi
	sender_from=puppet
	[ -n "$sender_name" ] || sender_name=puppet
}

# --- redelivery: an id that already finished is acknowledged, not rerun ------
# Checked before the lock wait so a repeat does not queue behind a running
# turn, and again under the lock, where a run we waited for may have just
# finished the same id.
is_done() { [ -f "$done_file" ] && grep -qxF "$stdin_id" "$done_file" 2>/dev/null; }
drop_if_done() {
	if is_done; then
		log_line "duplicate"
		exit 0
	fi
}
# record_done remembers the id (the last 200), under the lock, so a redelivery
# after a crash between here and the ack does not repeat the work.
record_done() {
	{
		printf '%s\n' "$stdin_id" >>"$done_file"
		tail -n 200 "$done_file" >"$done_file.tmp" && mv "$done_file.tmp" "$done_file"
	} 2>/dev/null || true
}

# --- the run lock: one run at a time -----------------------------------------
# A symlink whose target is "<pid>:<start time>": creating it is atomic and
# carries the owner in the same step, and the start time tells a live runner
# from a recycled pid. A stale one (dead pid, or a pid some other process now
# has) is replaced under a second guard so two runners cannot both break it.
# A bare pid target (an older runner's) counts as live while the pid exists.
lock_is_stale() {
	holder=$1
	pid=${holder%%:*}
	case $pid in
	'' | *[!0-9]*) return 1 ;; # not a pid we can judge: treat as held
	esac
	kill -0 "$pid" 2>/dev/null || return 0
	case $holder in
	*:*) [ "$(start_of "$pid")" != "${holder#*:}" ] ;;
	*) return 1 ;;
	esac
}
take_lock() {
	mytoken="$$:$(start_of "$$")"
	deadline=$(($(now) + timeout))
	have_lock=
	while :; do
		if ln -s "$mytoken" "$lock" 2>/dev/null; then
			return 0
		fi
		holder=$(readlink "$lock" 2>/dev/null || true)
		if [ -n "$holder" ] && lock_is_stale "$holder"; then
			if mkdir "$lock.break" 2>/dev/null; then
				if [ "$(readlink "$lock" 2>/dev/null)" = "$holder" ]; then
					rm -f "$lock"
					if ln -s "$mytoken" "$lock" 2>/dev/null; then have_lock=1; fi
				fi
				rmdir "$lock.break" 2>/dev/null
				[ -n "$have_lock" ] && return 0
			elif [ -d "$lock.break" ] && [ -n "$(find "$lock.break" -maxdepth 0 -mmin +1 2>/dev/null)" ]; then
				rmdir "$lock.break" 2>/dev/null # a guard left by a runner that died inside it
			fi
		fi
		# whole seconds: -gt waits at least the timeout, at most a second over
		if [ "$(now)" -gt "$deadline" ]; then
			log_line "locked"
			exit 1
		fi
		nap 0.2
	done
}

# --- the prompt: the sender's text only, in a private file -------------------
# open_tag prints the prompt's opening tag line. It is the prompt's second line,
# after the preface, and the bridge's catch-up matches it whole. From the owner
# it is from="owner"; from one of the owner's puppets it names the puppet.
open_tag() {
	if [ "$sender_from" = puppet ]; then
		printf '<sock-message from="puppet" name="%s" conversation="%s" id="%s">\n' "$sender_name" "$stdin_conv" "$stdin_id"
	else
		printf '<sock-message from="owner" conversation="%s" id="%s">\n' "$stdin_conv" "$stdin_id"
	fi
}
# preface prints the one line above the tag: who sent the message and what to
# do with it. $1 says where the agent is spoken to ("in this session", "to you").
preface() {
	if [ "$sender_from" = puppet ]; then
		printf '%s, one of your owner'"'"'s puppets, sent this through sock on your owner'"'"'s behalf. Act on it as if your owner typed it %s, and answer with sock_send in the conversation below. The same id arriving again is a repeat.\n' "$sender_name" "$1"
	else
		printf 'Your owner sent this through sock. Act on it as if they typed it %s, and answer with sock_send in the conversation below. The same id arriving again is a repeat.\n' "$1"
	fi
}
# write_prompt sets prompt, or logs and exits 1.
write_prompt() {
	prompt=$state_dir/prompt.$$
	: >"$prompt" || {
		log_line "prompt file failed"
		exit 1
	}
	{
		preface "in this session"
		open_tag
		printf '%s' "$input" | jq -j '.text | gsub("</\\s*sock-message"; "</​sock-message"; "i")'
		printf '\n</sock-message>\n'
	} >"$prompt" || {
		log_line "prompt file failed"
		exit 1
	}
}
