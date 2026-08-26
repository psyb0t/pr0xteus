#!/bin/bash
set -euo pipefail

readonly LOG_FILE="${TMPDIR:-/tmp}/pr0xteus-test-wrapper-upgrade.log"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly WRAPPER_SOURCE="$SCRIPT_DIR/pr0xteus.sh"
readonly TARGET_IMAGE="psyb0t/pr0xteus:v9.9.9"
readonly PREVIOUS_IMAGE="psyb0t/pr0xteus:v0.0.1"
readonly ROLLING_IMAGE="psyb0t/pr0xteus:latest"

declare -a temporary_directories=()
CASE_DIR=""
COMMAND_PATH=""
CONFIG_DIR=""
MOCKS_DIR=""

log() {
	local level="$1"
	shift
	printf '{"time":"%s","level":"%s","file":"test-wrapper-upgrade.sh","line":%d,"func":"%s","msg":"%s"}\n' \
		"$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$level" "${BASH_LINENO[0]}" \
		"${FUNCNAME[1]:-main}" "$*" >&2
}

fail() {
	log ERROR "$*"
	exit 1
}

cleanup() {
	local status=$? temporary_dir

	trap - EXIT
	for temporary_dir in "${temporary_directories[@]}"; do
		[[ -d "$temporary_dir" ]] || continue
		chmod -R u+w "$temporary_dir"
		rm -rf -- "$temporary_dir"
	done
	exit "$status"
}

on_error() {
	local status=$?
	log ERROR "wrapper upgrade regression test failed exit=$status"
	return "$status"
}

assert_contains() {
	local file="$1" expected="$2"
	grep -Fqx "$expected" "$file" || fail "missing event: $expected"
}

assert_not_contains() {
	local file="$1" unexpected="$2"
	if grep -Fqx "$unexpected" "$file"; then
		fail "unexpected event: $unexpected"
	fi
}

assert_contains_fragment() {
	local file="$1" expected="$2"
	grep -Fq "$expected" "$file" || fail "missing event containing: $expected"
}

event_line() {
	local file="$1" expected="$2"
	grep -Fnx "$expected" "$file" | cut -d: -f1
}

assert_after() {
	local file="$1" earlier="$2" later="$3"
	local earlier_line later_line
	earlier_line="$(event_line "$file" "$earlier")"
	later_line="$(event_line "$file" "$later")"
	[[ "$later_line" -gt "$earlier_line" ]] || fail "$later did not follow $earlier"
}

write_mock_commands() {
	local mocks_dir="$1"

	cat >"$mocks_dir/curl" <<'EOF'
#!/bin/bash
set -euo pipefail

case "$*" in
*"/releases/latest") printf '{"tag_name":"v9.9.9"}\n' ;;
*"/scripts/pr0xteus.sh")
	printf 'curl:%s\n' "$*" >>"$PR0XTEUS_TEST_EVENTS"
	case "${PR0XTEUS_TEST_REFRESH_MODE:-success}" in
	success) cat "$PR0XTEUS_TEST_NEW_WRAPPER" ;;
	fail) exit 22 ;;
	invalid) printf '# pr0xteus-managed-command\nif\n' ;;
	*) exit 64 ;;
	esac
	;;
*) exit 64 ;;
esac
EOF

	cat >"$mocks_dir/docker" <<'EOF'
#!/bin/bash
set -euo pipefail

printf 'docker:%s\n' "$*" >>"$PR0XTEUS_TEST_EVENTS"
EOF

	cat >"$mocks_dir/sudo" <<'EOF'
#!/bin/bash
set -euo pipefail

printf 'sudo:%s\n' "$*" >>"$PR0XTEUS_TEST_EVENTS"
if [[ "$1" == "install" ]]; then
	if [[ "${PR0XTEUS_TEST_FAIL_INSTALL:-false}" == "true" ]]; then
		exit 33
	fi
	target="${!#}"
	chmod u+w "$(dirname "$target")"
	"$@"
	chmod u-w "$(dirname "$target")"

	exit 0
fi

"$@"
EOF

	chmod 0755 "$mocks_dir/curl" "$mocks_dir/docker" "$mocks_dir/sudo"
}

write_fresh_wrapper() {
	local path="$1"

	cat >"$path" <<'EOF'
#!/bin/bash
# pr0xteus-managed-command
set -euo pipefail

printf 'fresh:%s:rolling=%s\n' "$1" "${PR0XTEUS_ROLLING:-0}" >>"$PR0XTEUS_TEST_EVENTS"
EOF
	chmod 0755 "$path"
}

prepare_case() {
	local case_name="$1" installation_mode="$2"
	local command_dir
	CASE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pr0xteus-wrapper-upgrade.${case_name}.XXXXXX")"
	temporary_directories+=("$CASE_DIR")
	command_dir="$CASE_DIR/bin"
	COMMAND_PATH="$command_dir/pr0xteus"
	CONFIG_DIR="$CASE_DIR/config"
	MOCKS_DIR="$CASE_DIR/mocks"
	mkdir -p "$command_dir" "$CONFIG_DIR" "$MOCKS_DIR" "$CASE_DIR/tmp"
	cp "$WRAPPER_SOURCE" "$COMMAND_PATH"
	sed -i "s|readonly SYSTEM_CONFIG_DIR=\"/etc/pr0xteus\"|readonly SYSTEM_CONFIG_DIR=\"$CONFIG_DIR\"|" \
		"$COMMAND_PATH"
	cp "$COMMAND_PATH" "$CASE_DIR/original-wrapper"
	chmod 0755 "$COMMAND_PATH"
	printf 'services: {}\n' >"$CONFIG_DIR/docker-compose.yml"
	printf 'PR0XTEUS_CONTROLLER_IMAGE=%s\n' "$PREVIOUS_IMAGE" >"$CONFIG_DIR/.env"
	write_mock_commands "$MOCKS_DIR"
	write_fresh_wrapper "$CASE_DIR/fresh-wrapper"

	if [[ "$installation_mode" == "system" ]]; then
		chmod u-w "$command_dir"
	fi
}

run_success_case() {
	local case_name="$1" installation_mode="$2"
	local events
	prepare_case "$case_name" "$installation_mode"
	events="$CASE_DIR/events"
	touch "$events"

	log INFO "running $installation_mode wrapper upgrade regression case"
	PATH="$MOCKS_DIR:$PATH" \
		TMPDIR="$CASE_DIR/tmp" \
		PR0XTEUS_HOME="$CONFIG_DIR" \
		PR0XTEUS_TEST_EVENTS="$events" \
		PR0XTEUS_TEST_NEW_WRAPPER="$CASE_DIR/fresh-wrapper" \
		"$COMMAND_PATH" upgrade

	assert_contains "$events" "docker:pull $TARGET_IMAGE"
	assert_contains "$events" "curl:-fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/v9.9.9/scripts/pr0xteus.sh"
	assert_contains "$events" "fresh:start:rolling=0"
	assert_contains "$events" "docker:image rm $PREVIOUS_IMAGE"
	assert_after "$events" "fresh:start:rolling=0" "docker:image rm $PREVIOUS_IMAGE"
	assert_not_contains "$events" "docker:compose up --detach"
	cmp -- "$CASE_DIR/fresh-wrapper" "$COMMAND_PATH" || fail "wrapper was not replaced"

	if [[ "$installation_mode" == "system" ]]; then
		assert_contains_fragment "$events" "sudo:install -m 0755 "
	else
		if grep -Fq 'sudo:install' "$events"; then
			fail "user wrapper upgrade unexpectedly used sudo"
		fi
	fi
}

run_rolling_case() {
	local events
	prepare_case rolling user
	events="$CASE_DIR/events"
	touch "$events"

	log INFO "running rolling wrapper upgrade regression case"
	PATH="$MOCKS_DIR:$PATH" \
		TMPDIR="$CASE_DIR/tmp" \
		PR0XTEUS_HOME="$CONFIG_DIR" \
		PR0XTEUS_TEST_EVENTS="$events" \
		PR0XTEUS_TEST_NEW_WRAPPER="$CASE_DIR/fresh-wrapper" \
		"$COMMAND_PATH" upgrade --rolling

	assert_contains "$events" "docker:pull $ROLLING_IMAGE"
	assert_contains "$events" "curl:-fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/scripts/pr0xteus.sh"
	assert_contains "$events" "fresh:start:rolling=1"
	assert_not_contains "$events" "docker:image rm $PREVIOUS_IMAGE"
	assert_not_contains "$events" "docker:compose up --detach"
}

run_refresh_failure_case() {
	local refresh_mode="$1"
	local events
	prepare_case "refresh-$refresh_mode" "user"
	events="$CASE_DIR/events"
	touch "$events"

	log INFO "running refresh-$refresh_mode failure regression case"
	if PATH="$MOCKS_DIR:$PATH" \
		TMPDIR="$CASE_DIR/tmp" \
		PR0XTEUS_HOME="$CONFIG_DIR" \
		PR0XTEUS_TEST_EVENTS="$events" \
		PR0XTEUS_TEST_NEW_WRAPPER="$CASE_DIR/fresh-wrapper" \
		PR0XTEUS_TEST_REFRESH_MODE="$refresh_mode" \
		"$COMMAND_PATH" upgrade; then
		fail "refresh-$refresh_mode upgrade unexpectedly succeeded"
	fi

	assert_not_contains "$events" "fresh:start:rolling=0"
	assert_not_contains "$events" "fresh:start:rolling=1"
	assert_not_contains "$events" "docker:image rm $PREVIOUS_IMAGE"
	cmp -- "$CASE_DIR/original-wrapper" "$COMMAND_PATH" || fail "failed refresh changed the installed wrapper"
}

run_install_failure_case() {
	local events
	prepare_case install-failure system
	events="$CASE_DIR/events"
	touch "$events"

	log INFO "running privileged wrapper install failure regression case"
	if PATH="$MOCKS_DIR:$PATH" \
		TMPDIR="$CASE_DIR/tmp" \
		PR0XTEUS_HOME="$CONFIG_DIR" \
		PR0XTEUS_TEST_EVENTS="$events" \
		PR0XTEUS_TEST_FAIL_INSTALL=true \
		PR0XTEUS_TEST_NEW_WRAPPER="$CASE_DIR/fresh-wrapper" \
		"$COMMAND_PATH" upgrade; then
		fail "install-failure upgrade unexpectedly succeeded"
	fi

	assert_not_contains "$events" "fresh:start:rolling=0"
	assert_not_contains "$events" "docker:image rm $PREVIOUS_IMAGE"
	cmp -- "$CASE_DIR/original-wrapper" "$COMMAND_PATH" || fail "failed install changed the installed wrapper"
}

main() {
	[[ -f "$WRAPPER_SOURCE" ]] || fail "missing wrapper source: $WRAPPER_SOURCE"
	run_success_case user user
	run_success_case system system
	run_rolling_case
	run_refresh_failure_case fail
	run_refresh_failure_case invalid
	run_install_failure_case
	log INFO "wrapper upgrade regression tests passed"
}

trap on_error ERR
trap cleanup EXIT
exec > >(tee -a "$LOG_FILE") 2>&1

main "$@"
