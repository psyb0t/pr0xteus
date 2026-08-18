#!/bin/bash
# pr0xteus-managed-command
# Shared runtime wrapper for both the installer and local source development.
set -euo pipefail

readonly CONFIG_DIRECTORY_NAME="pr0xteus"
readonly SYSTEM_CONFIG_DIR="/etc/pr0xteus"
readonly IMAGE_REPO="psyb0t/pr0xteus"
readonly ROLLING_IMAGE="psyb0t/pr0xteus:latest"
readonly INSTALLER_URL="https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh"
readonly GITHUB_API_LATEST="https://api.github.com/repos/psyb0t/pr0xteus/releases/latest"
readonly COMMAND_LOG_FILE="${TMPDIR:-/tmp}/pr0xteus-command.log"
readonly TAILSCALE_READY_ATTEMPTS=30
readonly TAILSCALE_READY_DELAY_SECONDS=2
readonly DEVELOPMENT_ENVIRONMENT="dev"
readonly DEVELOPMENT_CONTROLLER_IMAGE="psyb0t/pr0xteus:dev"

# Plain user-facing output; still tee'd to COMMAND_LOG_FILE for a debug trail.
say() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

fail() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

trap 'printf "error: command failed — see %s\n" "$COMMAND_LOG_FILE" >&2' ERR
exec > >(tee -a "$COMMAND_LOG_FILE") 2>&1

usage() {
	cat <<'EOF'
Usage: pr0xteus <command> [--rolling]

Commands:
  setup      Refresh managed Compose templates without replacing your config
  start      Validate the config, pull the pinned images, and start the stack
  stop       Stop the pr0xteus stack
  restart    Restart the stack
  status     Show the controller and socket-proxy state
  logs       Follow logs; pass any extra `docker compose logs` arguments
  upgrade    Re-pin to the latest release, pull it, and drop the previous image
  uninstall  Stop the stack, remove the command, and offer to delete your data
  help       Show this help

  --rolling  On setup/start/upgrade: use the moving :latest image for that run
             only, instead of the pinned release tag. Handy for testing main.

Config location: $PR0XTEUS_HOME if set, else /etc/pr0xteus for a system-wide
install, else ~/.config/pr0xteus for a per-user install.
EOF
}

# config_directory resolves where the stack config lives: an explicit
# PR0XTEUS_HOME wins; otherwise a system-wide install (/etc/pr0xteus) is
# preferred when present, falling back to the per-user ~/.config/pr0xteus.
config_directory() {
	local config_dir
	if [[ -n "${PR0XTEUS_HOME:-}" ]]; then
		config_dir="$PR0XTEUS_HOME"
	elif [[ -f "$SYSTEM_CONFIG_DIR/docker-compose.yml" ]]; then
		config_dir="$SYSTEM_CONFIG_DIR"
	else
		config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/$CONFIG_DIRECTORY_NAME"
	fi

	[[ "$config_dir" = /* ]] || fail "PR0XTEUS_HOME must be an absolute path"
	printf '%s\n' "$config_dir"
}

# root_wrap prints the sudo prefix needed to write to a config dir the current
# user does not own (i.e. the system-wide /etc/pr0xteus). Empty for per-user.
root_wrap() {
	local config_dir="$1"
	if [[ -w "$config_dir" ]]; then
		return
	fi
	command -v sudo >/dev/null || fail "writing $config_dir needs root but sudo is not available"
	printf 'sudo'
}

resolve_latest_tag() {
	local tag

	tag="$(curl -fsSL "$GITHUB_API_LATEST" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
	[[ -n "$tag" ]] || fail "could not resolve the latest pr0xteus release tag"

	printf '%s\n' "$tag"
}

env_pinned_image() {
	local env_file="$1/.env"

	[[ -f "$env_file" ]] || return 0
	sed -n 's/^PR0XTEUS_CONTROLLER_IMAGE=//p' "$env_file" | head -n1
}

env_set() {
	local config_dir="$1" key="$2" value="$3"
	local wrap
	wrap="$(root_wrap "$config_dir")"

	if grep -q "^${key}=" "$config_dir/.env"; then
		$wrap sed -i "s|^${key}=.*|${key}=${value}|" "$config_dir/.env"
	else
		# shellcheck disable=SC2016 # positional parameters expand in the child shell.
		$wrap bash -c 'printf "%s=%s\n" "$1" "$2" >>"$3"' _ "$key" "$value" "$config_dir/.env"
	fi
}

# operator_runtime_user keeps the controller non-root while allowing it to read
# the owner-only config bind mount. sudo retains SUDO_USER, so a system install
# still uses the operator identity instead of root.
operator_runtime_user() {
	local user="${SUDO_USER:-}"
	if [[ -n "$user" ]]; then
		printf '%s:%s\n' "$(id -u "$user")" "$(id -g "$user")"

		return
	fi

	printf '%s:%s\n' "$(id -u)" "$(id -g)"
}

# controller_image is the image the stack pins to: the moving :latest under
# --rolling, otherwise the pin recorded in .env, falling back to the latest
# published release when no .env exists yet.
controller_image() {
	local config_dir="$1" image

	if development_enabled; then
		printf '%s\n' "$DEVELOPMENT_CONTROLLER_IMAGE"

		return
	fi

	if [[ "${PR0XTEUS_ROLLING:-}" == "1" ]]; then
		printf '%s\n' "$ROLLING_IMAGE"

		return
	fi

	image="$(env_pinned_image "$config_dir")"
	if [[ -n "$image" ]]; then
		printf '%s\n' "$image"

		return
	fi

	printf '%s:%s\n' "$IMAGE_REPO" "$(resolve_latest_tag)"
}

development_enabled() {
	case "${PR0XTEUS_ENV:-}" in
	"") return 1 ;;
	"$DEVELOPMENT_ENVIRONMENT") return 0 ;;
	*) fail "PR0XTEUS_ENV must be dev when set" ;;
	esac
}

pull_controller_image() {
	local image="$1"

	development_enabled || docker pull "$image"
}

env_value() {
	local config_dir="$1"
	local key="$2"

	awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' \
		"$config_dir/.env"
}

tailscale_enabled() {
	local config_dir="$1"
	local enabled

	enabled="$(env_value "$config_dir" "PR0XTEUS_TAILSCALE_ENABLED")"

	case "$enabled" in
	true)
		return 0
		;;
	"" | false)
		return 1
		;;
	*)
		fail "PR0XTEUS_TAILSCALE_ENABLED must be true or false"
		;;
	esac
}

host_ports_disabled() {
	local config_dir="$1"
	local disabled

	disabled="$(env_value "$config_dir" "PR0XTEUS_DISABLE_HOST_PORTS")"

	case "$disabled" in
	"" | false)
		return 1
		;;
	true)
		return 0
		;;
	*)
		fail "PR0XTEUS_DISABLE_HOST_PORTS must be true or false"
		;;
	esac
}

compose() {
	local config_dir="$1"
	shift
	local -a command=(docker compose)

	if tailscale_enabled "$config_dir"; then
		command+=(--profile tailscale)
	fi

	command+=(
		--project-directory "$config_dir"
		--env-file "$config_dir/.env"
		-f "$config_dir/docker-compose.yml"
	)

	local ports_file
	if host_ports_disabled "$config_dir"; then
		ports_file="$config_dir/docker-compose.no-host-ports.yml"
	else
		ports_file="$config_dir/docker-compose.host-ports.yml"
	fi
	[[ -f "$ports_file" ]] || fail "missing $ports_file; run pr0xteus setup"
	command+=(-f "$ports_file")

	"${command[@]}" "$@"
}

config_command() {
	local config_dir="$1" image="$2"
	shift 2

	local wrap owner
	wrap="$(root_wrap "$config_dir")"
	if [[ -n "$wrap" ]]; then owner="0:0"; else owner="$(id -u):$(id -g)"; fi

	$wrap docker run --rm \
		--user "$owner" \
		-v "$config_dir:/config" \
		"$image" config "$@" --config-dir /config
}

setup() {
	local config_dir image runtime_user wrap
	config_dir="$(config_directory)"
	wrap="$(root_wrap "$config_dir")"
	runtime_user="$(operator_runtime_user)"

	$wrap mkdir -p "$config_dir"
	image="$(controller_image "$config_dir")"

	say "pulling $image"
	pull_controller_image "$image"
	say "writing config into $config_dir"
	config_command "$config_dir" "$image" init \
		--host-config-dir "$config_dir" \
		--controller-image "$image" \
		--runtime-user "$runtime_user" \
		--refresh-runtime-templates

	# Keep the shared-stack model when setup re-runs against system config.
	if [[ -n "$wrap" ]] && getent group docker >/dev/null 2>&1; then
		$wrap chgrp -R docker "$config_dir" || true
		$wrap find "$config_dir" -type d -exec chmod g+rx {} + || true
		$wrap find "$config_dir" -type f -exec chmod g+r {} + || true
	fi

	# config init preserves an existing .env; re-pin so the recorded image
	# always matches what we just pulled.
	env_set "$config_dir" PR0XTEUS_CONTROLLER_IMAGE "$image"
	env_set "$config_dir" PR0XTEUS_RUNTIME_USER "$runtime_user"
	say "config ready at $config_dir (pinned to $image)"
}

check_config() {
	local config_dir image wrap owner
	config_dir="$(config_directory)"
	image="$(controller_image "$config_dir")"
	wrap="$(root_wrap "$config_dir")"
	if [[ -n "$wrap" ]]; then owner="0:0"; else owner="$(id -u):$(id -g)"; fi
	[[ -f "$config_dir/docker-compose.yml" ]] || fail "run pr0xteus setup first"

	$wrap docker run --rm \
		--user "$owner" \
		-v "$config_dir:/config:ro" \
		"$image" config check --config-dir /config
}

wire_tailscale_serve() {
	local config_dir="$1"
	local attempt dns_name

	if ! tailscale_enabled "$config_dir"; then
		return
	fi

	say "waiting for Tailscale to authenticate"
	for ((attempt = 1; attempt <= TAILSCALE_READY_ATTEMPTS; attempt++)); do
		# `tailscale status --json` exits successfully while logged out. Wait
		# for the actual running backend before attempting Serve.
		if compose "$config_dir" exec -T tailscale tailscale status --json 2>/dev/null |
			awk '/"BackendState": "Running"/ { running = 1 } END { exit !running }'; then
			if compose "$config_dir" exec -T tailscale \
				tailscale serve --bg --http=80 http://pr0xteus:8000 &&
				compose "$config_dir" exec -T tailscale \
					tailscale serve --bg --tcp=9091 tcp://pr0xteus:9091 &&
				compose "$config_dir" exec -T tailscale \
					tailscale serve --bg --tcp=1080 tcp://pr0xteus:1080; then
				dns_name="$(compose "$config_dir" exec -T tailscale tailscale status --json | awk '
                    /"Self":/ { in_self = 1; next }
                    in_self && /"DNSName":/ {
                        line = $0
                        sub(/.*"DNSName": *"/, "", line)
                        sub(/".*/, "", line)
                        sub(/\.$/, "", line)
                        print line
                        exit
                    }
                ')"
				[[ -n "$dns_name" ]] || fail "Tailscale did not report its DNS name"
				env_set "$config_dir" PR0XTEUS_SOCKS_PUBLIC_ADDRESS "${dns_name}:1080"
				compose "$config_dir" up --detach --no-deps --force-recreate pr0xteus
				say "Tailscale Serve is routing tailnet API, metrics, and SOCKS5 to pr0xteus"

				return
			fi

			fail "Tailscale authenticated but could not configure Tailscale Serve"
		fi

		sleep "$TAILSCALE_READY_DELAY_SECONDS"
	done

	fail "Tailscale did not authenticate; check TS_AUTHKEY and pr0xteus logs"
}

start() {
	local config_dir image compose_pull_policy
	config_dir="$(config_directory)"
	check_config
	image="$(controller_image "$config_dir")"

	# A shell export overrides the same key in --env-file, so --rolling swaps in
	# :latest for this run without touching the recorded pin.
	export PR0XTEUS_CONTROLLER_IMAGE="$image"
	say "starting the stack ($image)"
	if development_enabled; then compose_pull_policy="missing"; else compose_pull_policy="always"; fi
	compose "$config_dir" up --detach --pull "$compose_pull_policy"
	wire_tailscale_serve "$config_dir"
	compose "$config_dir" ps
}

stop() {
	local config_dir
	config_dir="$(config_directory)"
	compose "$config_dir" --profile "*" down
}

restart() {
	stop
	start
}

status() {
	local config_dir
	config_dir="$(config_directory)"
	compose "$config_dir" ps
}

show_logs() {
	local config_dir
	config_dir="$(config_directory)"
	compose "$config_dir" logs "$@"
}

upgrade() {
	local config_dir runtime_user
	config_dir="$(config_directory)"
	runtime_user="$(operator_runtime_user)"
	[[ -f "$config_dir/docker-compose.yml" ]] || fail "run pr0xteus setup first"

	if [[ "${PR0XTEUS_ROLLING:-}" == "1" ]]; then
		warn "pulling the rolling :latest image (recorded pin unchanged)"
		docker pull "$ROLLING_IMAGE"
		config_command "$config_dir" "$ROLLING_IMAGE" init \
			--host-config-dir "$config_dir" \
			--controller-image "$ROLLING_IMAGE" \
			--runtime-user "$runtime_user" \
			--refresh-runtime-templates
		env_set "$config_dir" PR0XTEUS_RUNTIME_USER "$runtime_user"
		export PR0XTEUS_CONTROLLER_IMAGE="$ROLLING_IMAGE"
		compose "$config_dir" up --detach
		compose "$config_dir" ps

		return
	fi

	local old_image new_tag new_image
	old_image="$(env_pinned_image "$config_dir")"
	new_tag="$(resolve_latest_tag)"
	new_image="$IMAGE_REPO:$new_tag"

	if [[ "$old_image" == "$new_image" ]]; then
		say "already on the latest release $new_tag; re-pulling"
	fi

	say "pinning to $new_image"
	docker pull "$new_image"
	config_command "$config_dir" "$new_image" init \
		--host-config-dir "$config_dir" \
		--controller-image "$new_image" \
		--runtime-user "$runtime_user" \
		--refresh-runtime-templates
	env_set "$config_dir" PR0XTEUS_CONTROLLER_IMAGE "$new_image"
	env_set "$config_dir" PR0XTEUS_RUNTIME_USER "$runtime_user"
	export PR0XTEUS_CONTROLLER_IMAGE="$new_image"
	compose "$config_dir" up --detach
	compose "$config_dir" ps

	# Refresh the wrapper itself from the new installer (self-update), re-running
	# in the same mode this install used.
	say "refreshing the installer + wrapper"
	if [[ "$config_dir" == "$SYSTEM_CONFIG_DIR" ]]; then
		curl -fsSL "$INSTALLER_URL" | sudo bash -s -- --system
	else
		curl -fsSL "$INSTALLER_URL" | bash -s -- --user
	fi

	# Reclaim space: drop the previous image once the new one is running.
	if [[ -n "$old_image" && "$old_image" != "$new_image" ]]; then
		if docker image rm "$old_image" >/dev/null 2>&1; then
			say "removed the previous image $old_image"
		else
			warn "kept $old_image (still in use); prune it later for the space"
		fi
	fi
}

uninstall() {
	local config_dir answer delete_data=0 cmd_path wrap
	config_dir="$(config_directory)"
	wrap="$(root_wrap "$config_dir")"

	read -r -p "Also delete your data ($config_dir and its Docker volumes)? [y/N] " answer
	case "$answer" in
	y | Y | yes | YES) delete_data=1 ;;
	esac

	if [[ -f "$config_dir/docker-compose.yml" ]]; then
		say "stopping the stack"
		# Best-effort teardown: a half-configured or already-down stack must not
		# block removing the command.
		if ((delete_data)); then
			compose "$config_dir" down --volumes --remove-orphans || true
		else
			compose "$config_dir" down --remove-orphans || true
		fi
	fi

	# Resolve the concrete file THIS invocation is running as, never a fresh
	# PATH search: with both a per-user and system install present, `command -v
	# pr0xteus` can resolve to whichever one wins $PATH and delete the OTHER
	# install while reporting success. BASH_SOURCE[0] is the absolute path the
	# shell actually exec'd (bash resolves PATH lookups to an absolute path
	# before exec'ing, which the shebang re-exec then passes through), so this
	# only ever removes the install this wrapper belongs to.
	cmd_path="$(readlink -f -- "${BASH_SOURCE[0]}")"
	say "removing the pr0xteus command ($cmd_path)"
	if [[ -w "$(dirname "$cmd_path")" ]]; then
		rm -f "$cmd_path"
	else
		command -v sudo >/dev/null || fail "removing $cmd_path needs root but sudo is not available"
		sudo rm -f "$cmd_path"
	fi

	if ((delete_data)); then
		$wrap rm -rf -- "$config_dir"
		say "deleted $config_dir and its volumes"
	else
		say "kept your data at $config_dir"
	fi

	say "pr0xteus uninstalled"
}

main() {
	local subcommand="${1:-help}"
	shift || true

	# A single global flag: --rolling forces the moving :latest image.
	local -a rest=()
	local arg
	for arg in "$@"; do
		case "$arg" in
		--rolling) export PR0XTEUS_ROLLING=1 ;;
		*) rest+=("$arg") ;;
		esac
	done

	case "$subcommand" in
	setup) setup ;;
	start) start ;;
	stop) stop ;;
	restart) restart ;;
	status) status ;;
	logs) show_logs "${rest[@]}" ;;
	upgrade) upgrade ;;
	uninstall) uninstall ;;
	help | --help | -h) usage ;;
	*) fail "unknown command: $subcommand (try: pr0xteus help)" ;;
	esac
}

main "$@"
