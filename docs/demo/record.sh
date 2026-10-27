#!/bin/sh
# Regenerate the README GIF: sh docs/demo/record.sh (from the repository root).
# Needs Go, vhs, ttyd and ffmpeg. Public monitors need internet access.
# All database and build state stays in a fresh scratch directory, never the default DB.
set -eu

scratch=$(mktemp -d "${TMPDIR:-/tmp}/up-demo.XXXXXX")
export UP_DB="$scratch/up.db"
export PATH="$scratch:$HOME/go/bin:$HOME/.local/bin:$PATH"
server_pid=
daemon_pid=
cleanup() {
    if [ -n "$daemon_pid" ]; then
        kill "$daemon_pid" 2>/dev/null || :
        wait "$daemon_pid" 2>/dev/null || :
    fi
    if [ -n "$server_pid" ]; then
        kill "$server_pid" 2>/dev/null || :
        wait "$server_pid" 2>/dev/null || :
    fi
    # Retain scratch for inspection. Do not delete paths outside this directory.
    printf 'Demo scratch retained: %s\n' "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go build -o "$scratch/up-bin" ./cmd/up
# The header shows the hostname. Run `up` in a private namespace named prod-01 so
# the GIF never shows the recording machine's name; the real hostname is untouched.
cat >"$scratch/up" <<'WRAP'
#!/bin/sh
exec unshare --user --map-root-user --uts python3 -I -c \
    'import os, socket, sys; socket.sethostname("prod-01"); os.execv(sys.argv[1], sys.argv[1:])' \
    "$(dirname "$0")/up-bin" "$@"
WRAP
chmod +x "$scratch/up"
go build -o "$scratch/demo-server" ./docs/demo/server
go build -o "$scratch/demo-seed" ./docs/demo/seed
"$scratch/demo-seed" --db "$UP_DB"
"$scratch/demo-server" >"$scratch/server.log" 2>&1 &
server_pid=$!
up serve >"$scratch/daemon.log" 2>&1 &
daemon_pid=$!
"$scratch/demo-seed" --db "$UP_DB" --wait-ready
# Do not record a different service if port 8099 was already occupied.
kill -0 "$server_pid"
kill -0 "$daemon_pid"
vhs docs/demo/demo.tape
