#!/bin/bash
# Upgrade and rollback against a real release, on a Mac (docs/dev/multigame
# spec, 7.3 steps 1-5). Installed copies update themselves from GitHub
# releases, and go back by themselves when a new build doesn't start, while
# Valheim keeps running. So before anything is released, CI checks with a
# stand-in Valheim server running the whole time that:
#
#   1-2. the newest real release (or the build-N tag given) starts on a new
#        data folder, installs the stand-in server, starts it and posts in a
#        stand-in Discord;
#   3-4. it updates itself to this commit (copy N), which takes the running
#        server over without restarting it or posting again and watches it
#        for three minutes; N then updates itself to copy N+1 the same way,
#        which proves a build can deliver its own fix; and an update to a
#        build that doesn't start (/usr/bin/false) goes back to N+1 by
#        itself, still with the same server;
#   5.   the old release, started again on the same folder, takes the server
#        back, keeps the settings and its own build number, then stops the
#        server (saving the world), starts it and stops it again.
#
# The numbered copies are built here, into a test folder that is never
# uploaded, and they only ever talk to the stand-in GitHub (cmd/ci-stubs), so
# a test copy can never update itself to a real release in the middle of the
# test. The program runs bare, from a plain folder: inside an .app it would
# detach itself, and macOS could run it from a translocated copy.
#
# Usage: upgrade-test.sh [build-N]    (default: the newest build-N release)
# Needs go, gh (with GH_TOKEN), jq, curl and lipo, and GITHUB_REPOSITORY and
# RUN_NUMBER as GitHub Actions sets them. Run it after the waker and Doorstop
# are built, so the copies carry them as a release does. Bash 3.2 (macOS's
# own) is enough.
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/../.." && pwd)"
from="${1:-}"
: "${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY to owner/name}" "${RUN_NUMBER:?set RUN_NUMBER to the number of this run}"
root="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/ves-upgrade"
data="$root/data"
app="$root/app/ValheimEasyServer"
fake="$root/bin/fakevalheim"
panel=http://127.0.0.1:18457
stub_github=http://127.0.0.1:18500
stub_discord=http://127.0.0.1:18501
# A made-up webhook: with --dev-fake-discord the app sends its posts to the
# stand-in instead of discord.com.
webhook=https://discord.com/api/webhooks/100000000000000001/ci-upgrade-test
st=""        # the last status wait_for saw
stubs_pid=""

fail() {
	echo "::error::$*" >&2
	exit 1
}
step() {
	echo
	echo "=== $*"
}

cleanup() {
	local code=$?
	set +e
	echo
	echo "--- the app's log"
	cat "$data/logs/app.log" 2>/dev/null
	echo "--- the stand-ins' log"
	cat "$root/ci-stubs.log" 2>/dev/null
	echo "--- the Discord posts"
	cat "$root/stubs/posts.json" 2>/dev/null
	pkill -f -- "--data $data" 2>/dev/null
	pkill -f -- "$data/server/" 2>/dev/null
	if [ -n "$stubs_pid" ]; then
		kill "$stubs_pid" 2>/dev/null
	fi
	exit "$code"
}
trap cleanup EXIT

# status prints the app's status, or nothing while it doesn't answer (it
# doesn't for a few seconds while it restarts into another build).
status() {
	curl -fsS --max-time 5 "$panel/api/status" 2>/dev/null || true
}

# wait_for SECONDS WHAT FILTER waits until the status matches the jq FILTER
# and keeps that status in $st.
wait_for() {
	local deadline=$((SECONDS + $1)) s
	while [ "$SECONDS" -lt "$deadline" ]; do
		s=$(status)
		if [ -n "$s" ] && jq -e "$3" >/dev/null 2>&1 <<<"$s"; then
			st=$s
			return 0
		fi
		sleep 2
	done
	echo "The last status: $(status | jq -c '{state, installed, app, job, discord, lastExit}' 2>/dev/null)" >&2
	fail "Timed out after $1 seconds waiting for $2."
}

# expect FILTER WHY checks the last status against a jq filter.
expect() {
	if ! jq -e "$1" >/dev/null 2>&1 <<<"$st"; then
		fail "$2 (status: $(jq -c '{state, app, job, discord, lastExit, live, sleep}' <<<"$st"))"
	fi
}

# api METHOD PATH [JSON] calls the control panel's API as the panel does,
# and prints the answer. A refusal is shown with the app's reason.
api() {
	local body='{}' out
	if [ $# -ge 3 ]; then
		body=$3
	fi
	if ! out=$(curl -sS --fail-with-body --max-time 60 -X "$1" -H 'X-VES: 1' -H 'Content-Type: application/json' \
		--data-binary "$body" "$panel$2"); then
		echo "$1 $2 was refused: $out" >&2
		return 1
	fi
	echo "$out"
}

# update asks the app to install the newest build it finds now. A refusal
# is shown, and the wait that follows says what didn't happen.
update() {
	api POST /api/app/update '{"install":true}' >/dev/null || true
}

# stub PATH JSON tells the stand-in GitHub what to offer.
stub() {
	local out
	if ! out=$(curl -sS --fail-with-body --max-time 60 -X POST --data-binary "$2" "$stub_github$1"); then
		fail "The stand-in GitHub refused $1: $out"
	fi
}

publish() {
	stub /ci/publish "$(jq -cn --argjson b "$1" --arg f "$2" '{build: $b, file: $f}')"
	echo "The stand-in GitHub now offers $2 as build-$1."
}

posts() {
	curl -fsS --max-time 5 "$stub_discord/ci/posts"
}

# wait_posts N TEXT waits until Discord has had N posts and the Nth one
# says TEXT.
wait_posts() {
	local deadline=$((SECONDS + 60))
	while [ "$SECONDS" -lt "$deadline" ]; do
		if posts | jq -e --argjson n "$1" --arg t "$2" 'length == $n and (.[$n - 1].content | contains($t))' >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	fail "Expected Discord post $1 to say \"$2\"; the posts are: $(posts | jq -c '[.[].content]')"
}

settings_snapshot() {
	jq -cS '{password, world, port}' "$data/settings.json"
}

# world_gen prints the newest save generation of the world (Valheim 1.0
# writes _main.<generation>.* each time it saves), or -1.
world_gen() {
	local best=-1 f g
	for f in "$data/saves/worlds_local/$world"/_main.*.fwl2; do
		if [ -e "$f" ]; then
			g=${f##*/_main.}
			g=${g%.fwl2}
			if [ "$g" -gt "$best" ]; then
				best=$g
			fi
		fi
	done
	echo "$best"
}

start_app() {
	"$1" --no-window --data "$data" --port 18457 --dev-fake-server "$fake" \
		--dev-fake-github "$stub_github" --dev-fake-discord "$stub_discord" >>"$root/app-output.log" 2>&1 &
}

wait_app_gone() {
	local deadline=$((SECONDS + 90))
	while pgrep -f -- "--data $data" >/dev/null; do
		if [ "$SECONDS" -ge "$deadline" ]; then
			fail "The app didn't close."
		fi
		sleep 1
	done
}

# check_server WHEN: the server started in step 2 is still the one running,
# online, announced once, and the settings are as they were.
check_server() {
	expect '.state == "online"' "$1: the server isn't online."
	if [ "$(jq -r .pid "$data/runtime.json")" != "$pid" ]; then
		fail "$1: runtime.json names process $(jq -r .pid "$data/runtime.json"), not the server started in step 2 ($pid)."
	fi
	if ! kill -0 "$pid" 2>/dev/null; then
		fail "$1: the server process ($pid) is gone."
	fi
	if [ "$(posts | jq length)" != 1 ]; then
		fail "$1: Discord got these posts instead of one: $(posts | jq -c '[.[].content]')"
	fi
	if [ "$(settings_snapshot)" != "$settings_before" ]; then
		fail "$1: settings.json's password, world or port changed: $(settings_snapshot), was $settings_before."
	fi
	local damaged
	damaged=$(find "$data" -name '*.damaged-*' | head -5)
	if [ -n "$damaged" ]; then
		fail "$1: a file was found damaged and set aside: $damaged"
	fi
	echo "$1: the same server (PID $pid) is online, posted once, settings unchanged."
}

check_updated() {
	expect "any(.activity[]; .text | startswith(\"The app was updated to build $1\"))" "The activity doesn't say the app was updated to build $1."
}

command -v jq >/dev/null || fail "This test needs jq."
cd "$repo_dir"

step "1. The release to start from"
if [ -z "$from" ]; then
	# The newest build-N, which is what installed copies update to.
	from=$(gh release list --repo "$GITHUB_REPOSITORY" --exclude-drafts --exclude-pre-releases --limit 100 --json tagName \
		--jq '[.[].tagName | select(test("^build-[0-9]{1,9}$")) | ltrimstr("build-") | tonumber] | max | if . == null then empty else "build-\(.)" end')
fi
if ! [[ $from =~ ^build-([0-9]{1,9})$ ]]; then
	fail "No release to start from (got \"$from\"); give a tag such as build-29."
fi
old=$((10#${BASH_REMATCH[1]}))
rm -rf "$root"
mkdir -p "$root/bin" "$root/release" "$root/builds" "$root/app" "$root/stubs"
gh release download "$from" --repo "$GITHUB_REPOSITORY" --dir "$root/release" \
	--pattern ValheimEasyServer-mac --pattern ValheimEasyServer-mac.sha256
(cd "$root/release" && shasum -a 256 -c ValheimEasyServer-mac.sha256) || fail "$from's program doesn't match its checksum."
cp "$root/release/ValheimEasyServer-mac" "$app"
chmod +x "$app"
xattr -d com.apple.quarantine "$app" 2>/dev/null || true

# The copies are numbered above the release even when this run's number
# isn't (a re-run of an older run).
n=$RUN_NUMBER
if [ "$n" -le "$old" ]; then
	n=$((old + 1))
	echo "This run's number ($RUN_NUMBER) isn't above $from, so the test copies are numbered from $n."
fi
next=$((n + 1))
broken=$((n + 2))
# A build that doesn't start, which the app still accepts as a Mac program.
broken_exe=/usr/bin/false
case "$(od -An -tx1 -N4 "$broken_exe" | tr -d ' \n')" in
cafebabe | cffaedfe) ;;
*) fail "$broken_exe doesn't start with a Mach-O header, so the app would refuse it as an update; the test needs another program that doesn't start." ;;
esac

echo "Starting from $from; this commit is tested as builds $n and $next."
go build -o "$root/bin/ci-stubs" ./cmd/ci-stubs
go build -o "$fake" ./cmd/fakevalheim
# build_copy N builds this commit as build N, for Intel and Apple silicon
# like a release.
build_copy() {
	local out="$root/builds/build-$1/ValheimEasyServer-mac" arch
	mkdir -p "$(dirname "$out")"
	for arch in amd64 arm64; do
		GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=build-$1 -X main.build=$1" -o "$out-$arch" .
	done
	lipo -create -output "$out" "$out-amd64" "$out-arm64"
	rm "$out-amd64" "$out-arm64"
}
build_copy "$n"
build_copy "$next"

"$root/bin/ci-stubs" -dir "$root/stubs" 2>"$root/ci-stubs.log" &
stubs_pid=$!
deadline=$((SECONDS + 30))
until curl -fsS "$stub_github/ci/ready" >/dev/null 2>&1 && curl -fsS "$stub_discord/ci/ready" >/dev/null 2>&1; do
	if ! kill -0 "$stubs_pid" 2>/dev/null || [ "$SECONDS" -ge "$deadline" ]; then
		fail "The stand-ins for GitHub and Discord didn't start."
	fi
	sleep 0.5
done

step "2. $from installs the stand-in server, starts it and posts in Discord"
start_app "$app"
wait_for 60 "$from to start" ".app.build == $old"
settings=$(curl -fsS "$panel/api/settings" | jq -c --arg w "$webhook" '.settings | .discord = true | .discordWebhook = $w')
api POST /api/settings "$settings" >/dev/null
api POST /api/server/install '{"repair":false}' >/dev/null
wait_for 180 "the stand-in server to be installed" '.installed and (.job == null or .job.finished != null)'
expect '(.job.error // "") == ""' "Installing failed."
api POST /api/server/start >/dev/null
wait_for 180 "the server to be online and posted in Discord" \
	'.state == "online" and (.join.code // "") != "" and (.discord.posted // null) != null'
expect '(.discord.error // "") == ""' "Posting in Discord failed."
wait_posts 1 " is online"
pid=$(jq -r .pid "$data/runtime.json")
world=$(jq -r .world "$data/settings.json")
settings_before=$(settings_snapshot)
echo "The server (PID $pid) is online with the world $world, and Discord got the join code."

step "3. $from updates itself to this commit, build $n"
publish "$n" "$root/builds/build-$n/ValheimEasyServer-mac"
update
wait_for 180 "the update to build $n" ".app.build == $n and .state == \"online\""

step "4. Build $n took the server over"
check_server "After the update to build $n"
check_updated "$n"
since=$(($(date +%s) * 1000))
echo "Keeping build $n running for three minutes with the server it took over..."
end=$((SECONDS + 180))
while [ "$SECONDS" -lt "$end" ]; do
	sleep 15
	if ! kill -0 "$pid" 2>/dev/null; then
		fail "The server process ($pid) ended while build $n watched it."
	fi
	wait_for 30 "build $n to answer" ".app.build == $n and .state == \"online\""
done
expect ".live.t > $since and .live.running and .live.mem > 0" "Build $n hasn't measured the server it took over since then."
expect '.sleep.available' "Build $n's setup checks haven't run."
expect '.app.checkedAt != null and .app.latest == null and (.app.error // "") == ""' \
	"Build $n's own update check didn't run two minutes in, or found something to update to."
check_server "After three minutes on build $n"

publish "$next" "$root/builds/build-$next/ValheimEasyServer-mac"
update
wait_for 180 "build $n to update itself to build $next" ".app.build == $next and .state == \"online\""
check_server "After the update to build $next"
check_updated "$next"

echo "An update to a build that doesn't start ($broken_exe as build $broken):"
publish "$broken" "$broken_exe"
update
wait_for 240 "the app to go back to build $next" ".app.build == $next and .app.failed == $broken and .state == \"online\""
check_server "After build $broken didn't start"
expect "any(.activity[]; .text | startswith(\"Build $broken of the app didn't start\"))" "The activity doesn't say build $broken didn't start."

step "5. Back to $from on the same folder"
# Nothing newer stays on offer, and automatic updates are off, so the old
# release can't update itself again two minutes in.
stub /ci/unpublish "{\"above\": $old}"
settings=$(curl -fsS "$panel/api/settings" | jq -c '.settings | .autoUpdate = false')
api POST /api/settings "$settings" >/dev/null
api POST /api/system/quit '{"stopServer":false}' >/dev/null
wait_app_gone
if ! kill -0 "$pid" 2>/dev/null; then
	fail "Quitting the app (leaving the server running) stopped the server."
fi
# The release's program goes back in place, as a rollback would put it. A
# new file rather than one written over: macOS remembers a program's
# signature by the file, and could refuse the changed one.
rm -f "$app"
cp "$root/release/ValheimEasyServer-mac" "$app"
chmod +x "$app"
start_app "$app"
wait_for 60 "$from to start again" ".app.build == $old and .state == \"online\""
check_server "Back on $from"

gen=$(world_gen)
api POST /api/server/stop >/dev/null
wait_for 120 "$from to stop the server" '.state == "stopped"'
expect ".lastExit.expected and .app.build == $old" "$from didn't stop the server cleanly."
if [ "$(world_gen)" -le "$gen" ]; then
	fail "Stopping the server didn't save the world (its newest save is still generation $gen)."
fi
if kill -0 "$pid" 2>/dev/null; then
	fail "The server process ($pid) still runs after the stop."
fi
wait_posts 2 "has stopped"
echo "$from stopped the server it took over, and the world was saved."

api POST /api/server/start >/dev/null
wait_for 180 "$from to start the server again" ".state == \"online\" and .app.build == $old and (.discord.posted // null) != null"
wait_posts 3 " is online"
gen=$(world_gen)
api POST /api/server/stop >/dev/null
wait_for 120 "$from to stop the server again" '.state == "stopped"'
expect ".lastExit.expected and .app.build == $old" "$from didn't stop the server cleanly the second time."
if [ "$(world_gen)" -le "$gen" ]; then
	fail "The second stop didn't save the world."
fi
wait_posts 4 "has stopped"

# The old release checks for updates two minutes after it starts: it must
# find nothing to update to, and stay on its own build.
wait_for 200 "$from's own update check" ".app.checkedAt != null"
expect ".app.build == $old and .app.latest == null and (.app.updating // false) == false" "$from would update itself."
api POST /api/system/quit '{"stopServer":false}' >/dev/null
wait_app_gone
echo
echo "Upgrade and rollback between $from and this commit (builds $n and $next) all worked."
