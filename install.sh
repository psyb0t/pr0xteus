#!/bin/bash
set -euo pipefail

# pr0xteus installer. Run with:
#   curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
#
# It pins the local stack to the latest RELEASE tag (never :latest on your box),
# runs `pr0xteus config init` to drop the compose file plus an owner-only config
# into ~/.pr0xteus, and installs a `pr0xteus` command that wraps the usual Docker
# commands. Pass --rolling to pin the moving :latest image instead:
#   curl -fsSL .../install.sh | sudo bash -s -- --rolling

readonly INSTALL_LOG_FILE="/tmp/pr0xteus-install.log"
readonly INSTALL_PATH="/usr/local/bin/pr0xteus"
readonly WRAPPER_MARKER="pr0xteus-managed-command"
readonly CONFIG_DIRECTORY_NAME=".pr0xteus"
readonly IMAGE_REPO="psyb0t/pr0xteus"
readonly ROLLING_IMAGE="psyb0t/pr0xteus:latest"
readonly GITHUB_API_LATEST="https://api.github.com/repos/psyb0t/pr0xteus/releases/latest"

ROLLING=0
WRAPPER_TEMPORARY_FILE=""

# This is a user-facing installer, so the output is deliberately plain prose
# (per the bash logging rule's carve-out for CLI output the user asked for),
# not JSON. Everything is still tee'd to INSTALL_LOG_FILE for a debug trail.
say() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }

fail() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

trap 'printf "error: install failed — see %s\n" "$INSTALL_LOG_FILE" >&2' ERR
trap 'rm -f "$WRAPPER_TEMPORARY_FILE"' EXIT
exec > >(tee -a "$INSTALL_LOG_FILE") 2>&1

require_root() {
	if ((EUID != 0)); then
		fail "run this installer with sudo"
	fi

	if [[ -z "${SUDO_USER:-}" || "$SUDO_USER" == "root" ]]; then
		fail "run this through sudo from the account that will use pr0xteus"
	fi
}

resolve_target_user() {
	local account_record

	account_record="$(getent passwd "$SUDO_USER")"
	[[ -n "$account_record" ]] || fail "could not resolve sudo user $SUDO_USER"

	TARGET_HOME="$(cut -d: -f6 <<<"$account_record")"
	TARGET_UID="$(id -u "$SUDO_USER")"
	TARGET_GID="$(id -g "$SUDO_USER")"
	TARGET_CONFIG_DIR="$TARGET_HOME/$CONFIG_DIRECTORY_NAME"
}

run_as_target_user() {
	sudo -H -u "$SUDO_USER" "$@"
}

resolve_latest_tag() {
	local tag

	tag="$(curl -fsSL "$GITHUB_API_LATEST" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
	[[ -n "$tag" ]] || fail "could not resolve the latest pr0xteus release tag"

	printf '%s\n' "$tag"
}

# resolve_controller_image picks the image the local stack pins to: the moving
# :latest when --rolling was given, otherwise the latest published release tag.
# The controller derives its matching cell image from this tag at runtime
# (vX.Y.Z -> cell-vX.Y.Z, latest -> cell-latest), so pinning here pins both.
resolve_controller_image() {
	if ((ROLLING)); then
		printf '%s\n' "$ROLLING_IMAGE"

		return
	fi

	printf '%s:%s\n' "$IMAGE_REPO" "$(resolve_latest_tag)"
}

env_pinned_image() {
	local env_file="$1"

	[[ -f "$env_file" ]] || return 0
	sed -n 's/^PR0XTEUS_CONTROLLER_IMAGE=//p' "$env_file" | head -n1
}

# pin_env_image rewrites PR0XTEUS_CONTROLLER_IMAGE in an existing .env as the
# target user. config init preserves an existing .env (writeFileIfAbsent), so an
# upgrade re-runs config init but must re-pin the image line here itself.
pin_env_image() {
	local env_file="$1" image="$2"

	[[ -f "$env_file" ]] || return 0
	# shellcheck disable=SC2016  # $1/$2 are expanded by the inner bash, not here — intentional
	run_as_target_user bash -c '
		env_file="$1"
		image="$2"
		if grep -q "^PR0XTEUS_CONTROLLER_IMAGE=" "$env_file"; then
			sed -i "s|^PR0XTEUS_CONTROLLER_IMAGE=.*|PR0XTEUS_CONTROLLER_IMAGE=${image}|" "$env_file"
		else
			printf "PR0XTEUS_CONTROLLER_IMAGE=%s\n" "$image" >>"$env_file"
		fi
	' _ "$env_file" "$image"
}

write_command() {
	if [[ -e "$INSTALL_PATH" ]] && ! grep -Fq "$WRAPPER_MARKER" "$INSTALL_PATH"; then
		fail "$INSTALL_PATH already exists and is not managed by this installer"
	fi

	WRAPPER_TEMPORARY_FILE="$(mktemp)"

	cat >"$WRAPPER_TEMPORARY_FILE" <<'SCRIPT'
#!/bin/bash
# pr0xteus-managed-command
set -euo pipefail

readonly CONFIG_DIRECTORY_NAME=".pr0xteus"
readonly IMAGE_REPO="psyb0t/pr0xteus"
readonly ROLLING_IMAGE="psyb0t/pr0xteus:latest"
readonly INSTALLER_URL="https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh"
readonly GITHUB_API_LATEST="https://api.github.com/repos/psyb0t/pr0xteus/releases/latest"
readonly COMMAND_LOG_FILE="${TMPDIR:-/tmp}/pr0xteus-command.log"
readonly TAILSCALE_READY_ATTEMPTS=30
readonly TAILSCALE_READY_DELAY_SECONDS=2

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
  setup      Create ~/.pr0xteus (compose + config) without replacing your config
  start      Validate the config, pull the pinned images, and start the stack
  stop       Stop the pr0xteus stack
  status     Show the controller and socket-proxy state
  logs       Follow logs; pass any extra `docker compose logs` arguments
  upgrade    Re-pin to the latest release, pull it, and drop the previous image
  uninstall  Stop the stack, remove the command, and offer to delete your data
  help       Show this help

  --rolling  On setup/start/upgrade: use the moving :latest image for that run
             only, instead of the pinned release tag. Handy for testing main.
EOF
}

config_directory() {
    local config_dir="${PR0XTEUS_HOME:-$HOME/$CONFIG_DIRECTORY_NAME}"

    [[ "$config_dir" = /* ]] || fail "PR0XTEUS_HOME must be an absolute path"
    printf '%s\n' "$config_dir"
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

    if grep -q "^${key}=" "$config_dir/.env"; then
        sed -i "s|^${key}=.*|${key}=${value}|" "$config_dir/.env"
    else
        printf '%s=%s\n' "$key" "$value" >>"$config_dir/.env"
    fi
}

# controller_image is the image the stack pins to: the moving :latest under
# --rolling, otherwise the pin recorded in .env, falling back to the latest
# published release when no .env exists yet.
controller_image() {
    local config_dir="$1" image

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
    "${command[@]}" "$@"
}

setup() {
    local config_dir image

    config_dir="$(config_directory)"
    mkdir -p "$config_dir"
    chmod 700 "$config_dir"
    image="$(controller_image "$config_dir")"

    say "pulling $image"
    docker pull "$image"
    say "writing config into $config_dir"
    docker run --rm \
        --user "$(id -u):$(id -g)" \
        -v "$config_dir:/config" \
        "$image" config init \
        --config-dir /config \
        --host-config-dir "$config_dir" \
        --controller-image "$image"

    # config init preserves an existing .env; re-pin so the recorded image
    # always matches what we just pulled.
    env_set "$config_dir" PR0XTEUS_CONTROLLER_IMAGE "$image"
    say "config ready at $config_dir (pinned to $image)"
}

check_config() {
    local config_dir image

    config_dir="$(config_directory)"
    image="$(controller_image "$config_dir")"
    [[ -f "$config_dir/docker-compose.yml" ]] || fail "run pr0xteus setup first"

    docker run --rm \
        --user "$(id -u):$(id -g)" \
        -v "$config_dir:/config:ro" \
        "$image" config check --config-dir /config
}

wire_tailscale_serve() {
    local config_dir="$1"
    local attempt

    if ! tailscale_enabled "$config_dir"; then
        return
    fi

    say "waiting for Tailscale to authenticate"
    for ((attempt = 1; attempt <= TAILSCALE_READY_ATTEMPTS; attempt++)); do
        if compose "$config_dir" exec -T tailscale tailscale status --json >/dev/null; then
            if compose "$config_dir" exec -T tailscale \
                tailscale serve --bg --http=80 http://pr0xteus:8000; then
                say "Tailscale Serve is routing tailnet HTTP to pr0xteus"

                return
            fi

            fail "Tailscale authenticated but could not configure Tailscale Serve"
        fi

        sleep "$TAILSCALE_READY_DELAY_SECONDS"
    done

    fail "Tailscale did not authenticate; check TS_AUTHKEY and pr0xteus logs"
}

start() {
    local config_dir image

    config_dir="$(config_directory)"
    check_config
    image="$(controller_image "$config_dir")"

    # A shell export overrides the same key in --env-file, so --rolling swaps in
    # :latest for this run without touching the recorded pin.
    export PR0XTEUS_CONTROLLER_IMAGE="$image"
    say "starting the stack ($image)"
    compose "$config_dir" up --detach --pull always
    wire_tailscale_serve "$config_dir"
    compose "$config_dir" ps
}

stop() {
    local config_dir

    config_dir="$(config_directory)"
    compose "$config_dir" down
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
    local config_dir
    config_dir="$(config_directory)"
    [[ -f "$config_dir/docker-compose.yml" ]] || fail "run pr0xteus setup first"

    if [[ "${PR0XTEUS_ROLLING:-}" == "1" ]]; then
        warn "pulling the rolling :latest image (recorded pin unchanged)"
        docker pull "$ROLLING_IMAGE"
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
    env_set "$config_dir" PR0XTEUS_CONTROLLER_IMAGE "$new_image"
    export PR0XTEUS_CONTROLLER_IMAGE="$new_image"
    compose "$config_dir" up --detach
    compose "$config_dir" ps

    # Refresh the wrapper itself from the new installer (self-update).
    say "refreshing the installer + wrapper"
    curl -fsSL "$INSTALLER_URL" | sudo bash

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
    local config_dir answer delete_data=0
    config_dir="$(config_directory)"

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

    say "removing the pr0xteus command (needs sudo)"
    sudo rm -f /usr/local/bin/pr0xteus

    if ((delete_data)); then
        rm -rf -- "$config_dir"
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
    status) status ;;
    logs) show_logs "${rest[@]}" ;;
    upgrade) upgrade ;;
    uninstall) uninstall ;;
    help | --help | -h) usage ;;
    *) fail "unknown command: $subcommand (try: pr0xteus help)" ;;
    esac
}

main "$@"
SCRIPT

	install -m 0755 "$WRAPPER_TEMPORARY_FILE" "$INSTALL_PATH"
	say "installed command at $INSTALL_PATH"
}

main() {
	local arg
	for arg in "$@"; do
		case "$arg" in
		--rolling) ROLLING=1 ;;
		*) fail "unknown argument: $arg (only --rolling is supported)" ;;
		esac
	done

	require_root
	resolve_target_user

	run_as_target_user docker info >/dev/null ||
		fail "the account $SUDO_USER cannot talk to Docker — is the daemon running and is the user in the docker group?"
	install -d -m 0700 -o "$TARGET_UID" -g "$TARGET_GID" "$TARGET_CONFIG_DIR"

	local previous_image image
	previous_image="$(env_pinned_image "$TARGET_CONFIG_DIR/.env")"
	image="$(resolve_controller_image)"

	say "pinning to $image"
	run_as_target_user docker pull "$image"

	say "writing local configuration into $TARGET_CONFIG_DIR"
	run_as_target_user docker run --rm \
		--user "$TARGET_UID:$TARGET_GID" \
		-v "$TARGET_CONFIG_DIR:/config" \
		"$image" config init \
		--config-dir /config \
		--host-config-dir "$TARGET_CONFIG_DIR" \
		--controller-image "$image"

	# config init preserves an existing .env, so re-pin the image line for the
	# upgrade case where the .env already existed.
	pin_env_image "$TARGET_CONFIG_DIR/.env" "$image"
	write_command

	if [[ -n "$previous_image" && "$previous_image" != "$image" ]]; then
		if run_as_target_user docker image rm "$previous_image" >/dev/null 2>&1; then
			say "removed the previous image $previous_image"
		else
			warn "kept the previous image $previous_image (still in use)"
		fi
	fi

	printf '\npr0xteus is installed (pinned to %s).\n\n' "$image"
	printf '  Config lives in:  %s\n' "$TARGET_CONFIG_DIR"
	printf '  Command lives in: %s\n\n' "$INSTALL_PATH"
	printf 'Put an authorized WireGuard .conf file in:\n'
	printf '  %s/secrets/wireguard/\n\n' "$TARGET_CONFIG_DIR"
	printf 'Then edit pools.yaml and egress-routing.yaml there (and the .env for\n'
	printf 'ports, tailnet access, or logging), and run:\n\n'
	printf '  pr0xteus start\n\n'
}

main "$@"
