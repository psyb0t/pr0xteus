#!/bin/bash
set -euo pipefail

# pr0xteus installer. Two modes:
#
#   Per-user (no root) — installs into your home, for the current user only:
#     curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
#     command -> ~/.local/bin/pr0xteus, config -> ~/.config/pr0xteus
#
#   System-wide (root) — one shared stack any docker-group user can drive:
#     curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
#     command -> /usr/local/bin/pr0xteus, config -> /etc/pr0xteus
#
# The mode is chosen from who runs it (root -> system, otherwise per-user); pass
# --system or --user to force it. It pins the stack to the latest RELEASE tag
# (never :latest on your box); pass --rolling to pin the moving :latest instead.

readonly INSTALL_LOG_FILE="/tmp/pr0xteus-install.log"
readonly SYSTEM_INSTALL_PATH="/usr/local/bin/pr0xteus"
readonly SYSTEM_CONFIG_DIR="/etc/pr0xteus"
readonly WRAPPER_MARKER="pr0xteus-managed-command"
readonly CONFIG_DIRECTORY_NAME="pr0xteus"
# shellcheck disable=SC2016  # deliberately literal: $HOME/$PATH must land in the rc file unexpanded
readonly USER_PATH_SNIPPET='export PATH="$HOME/.local/bin:$PATH"'
readonly IMAGE_REPO="psyb0t/pr0xteus"
readonly ROLLING_IMAGE="psyb0t/pr0xteus:latest"
readonly GITHUB_API_LATEST="https://api.github.com/repos/psyb0t/pr0xteus/releases/latest"

ROLLING=0
MODE=""
INSTALL_PATH=""
TARGET_CONFIG_DIR=""
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

# resolve_mode fixes MODE (from --system/--user, else EUID) and the paths it
# implies, and rejects the nonsensical combinations up front.
resolve_mode() {
	if [[ -z "$MODE" ]]; then
		if ((EUID == 0)); then MODE="system"; else MODE="user"; fi
	fi

	case "$MODE" in
	system)
		((EUID == 0)) ||
			fail "the system-wide install needs root — re-run with sudo, or use --user for a per-user install"
		INSTALL_PATH="$SYSTEM_INSTALL_PATH"
		TARGET_CONFIG_DIR="$SYSTEM_CONFIG_DIR"
		;;
	user)
		((EUID != 0)) ||
			fail "the per-user install must not run as root — run it as your normal account, or use --system"
		INSTALL_PATH="$HOME/.local/bin/pr0xteus"
		TARGET_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/$CONFIG_DIRECTORY_NAME"
		;;
	*)
		fail "unknown mode: $MODE"
		;;
	esac
}

# prepare_config_dir creates the config directory with mode-appropriate
# ownership. System config is root-owned but group-readable by the docker group
# so any docker-group operator can drive the shared stack (docker-group access
# is already root-equivalent, so this exposes nothing new). Per-user config is
# private (0700) to the installing user.
prepare_config_dir() {
	if [[ "$MODE" == "system" ]]; then
		install -d -m 0750 "$TARGET_CONFIG_DIR"
		if getent group docker >/dev/null 2>&1; then
			chgrp docker "$TARGET_CONFIG_DIR"
		fi

		return
	fi

	install -d -m 0700 "$TARGET_CONFIG_DIR"
}

# apply_config_permissions re-asserts the sharing model after config init has
# written files. Only meaningful for the system install.
apply_config_permissions() {
	[[ "$MODE" == "system" ]] || return 0

	if getent group docker >/dev/null 2>&1; then
		chgrp -R docker "$TARGET_CONFIG_DIR" || true
	fi
	# Group may read/traverse but never write.
	chmod -R g-w "$TARGET_CONFIG_DIR" || true
	find "$TARGET_CONFIG_DIR" -type d -exec chmod g+rx {} + || true
	find "$TARGET_CONFIG_DIR" -type f -exec chmod g+r {} + || true
}

# warn_user_path tells a per-user installer, in the terminal, exactly how to put
# ~/.local/bin on PATH for both bash and zsh when it is not already there.
warn_user_path() {
	[[ "$MODE" == "user" ]] || return 0

	case ":$PATH:" in
	*":$HOME/.local/bin:"*) return 0 ;;
	esac

	warn "$HOME/.local/bin is not on your PATH — the pr0xteus command will not be found yet"
	printf '\nAdd it to your shell, then restart the shell (or source the file):\n\n'
	printf "  bash:  echo '%s' >> ~/.bashrc && source ~/.bashrc\n" "$USER_PATH_SNIPPET"
	printf "  zsh:   echo '%s' >> ~/.zshrc && source ~/.zshrc\n\n" "$USER_PATH_SNIPPET"
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

# pin_env_image rewrites PR0XTEUS_CONTROLLER_IMAGE in an existing .env. config
# init preserves an existing .env (writeFileIfAbsent), so an upgrade re-runs
# config init but must re-pin the image line here itself.
pin_env_image() {
	local env_file="$1" image="$2"

	[[ -f "$env_file" ]] || return 0
	if grep -q "^PR0XTEUS_CONTROLLER_IMAGE=" "$env_file"; then
		sed -i "s|^PR0XTEUS_CONTROLLER_IMAGE=.*|PR0XTEUS_CONTROLLER_IMAGE=${image}|" "$env_file"
	else
		printf 'PR0XTEUS_CONTROLLER_IMAGE=%s\n' "$image" >>"$env_file"
	fi
}

write_command() {
	if [[ -e "$INSTALL_PATH" ]] && ! grep -Fq "$WRAPPER_MARKER" "$INSTALL_PATH"; then
		fail "$INSTALL_PATH already exists and is not managed by this installer"
	fi

	install -d "$(dirname "$INSTALL_PATH")"
	WRAPPER_TEMPORARY_FILE="$(mktemp)"

	cat >"$WRAPPER_TEMPORARY_FILE" <<'SCRIPT'
#!/bin/bash
# pr0xteus-managed-command
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
  setup      Create the config (compose + config) without replacing your config
  start      Validate the config, pull the pinned images, and start the stack
  stop       Stop the pr0xteus stack
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
        $wrap bash -c 'printf "%s=%s\n" "$1" "$2" >>"$3"' _ "$key" "$value" "$config_dir/.env"
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
    local config_dir image wrap owner
    config_dir="$(config_directory)"
    wrap="$(root_wrap "$config_dir")"
    # System config (sudo-wrapped /etc) is root-owned then group-shared; per-user
    # config is owned by the current user so they can edit it.
    if [[ -n "$wrap" ]]; then owner="0:0"; else owner="$(id -u):$(id -g)"; fi

    $wrap mkdir -p "$config_dir"
    image="$(controller_image "$config_dir")"

    say "pulling $image"
    docker pull "$image"
    say "writing config into $config_dir"
    $wrap docker run --rm \
        --user "$owner" \
        -v "$config_dir:/config" \
        "$image" config init \
        --config-dir /config \
        --host-config-dir "$config_dir" \
        --controller-image "$image"

    # Keep the shared-stack model when setup re-runs against system config.
    if [[ -n "$wrap" ]] && getent group docker >/dev/null 2>&1; then
        $wrap chgrp -R docker "$config_dir" || true
        $wrap find "$config_dir" -type d -exec chmod g+rx {} + || true
        $wrap find "$config_dir" -type f -exec chmod g+r {} + || true
    fi

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

    cmd_path="$(command -v pr0xteus 2>/dev/null || printf '%s' "${BASH_SOURCE[0]}")"
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
		--system) MODE="system" ;;
		--user) MODE="user" ;;
		*) fail "unknown argument: $arg (supported: --system, --user, --rolling)" ;;
		esac
	done

	resolve_mode

	docker info >/dev/null 2>&1 ||
		fail "cannot talk to Docker — is the daemon running, and (per-user) is your account in the docker group?"

	say "installing pr0xteus ($MODE mode)"
	prepare_config_dir

	local previous_image image
	previous_image="$(env_pinned_image "$TARGET_CONFIG_DIR/.env")"
	image="$(resolve_controller_image)"

	say "pinning to $image"
	docker pull "$image"

	# --user makes config init write files owned by the installing identity: the
	# current user in per-user mode, root (EUID 0) in system mode. System files
	# are then group-shared by apply_config_permissions below.
	say "writing configuration into $TARGET_CONFIG_DIR"
	docker run --rm \
		--user "$(id -u):$(id -g)" \
		-v "$TARGET_CONFIG_DIR:/config" \
		"$image" config init \
		--config-dir /config \
		--host-config-dir "$TARGET_CONFIG_DIR" \
		--controller-image "$image"

	# config init preserves an existing .env, so re-pin the image line for the
	# upgrade case where the .env already existed.
	pin_env_image "$TARGET_CONFIG_DIR/.env" "$image"
	apply_config_permissions
	write_command
	warn_user_path

	if [[ -n "$previous_image" && "$previous_image" != "$image" ]]; then
		if docker image rm "$previous_image" >/dev/null 2>&1; then
			say "removed the previous image $previous_image"
		else
			warn "kept the previous image $previous_image (still in use)"
		fi
	fi

	printf '\npr0xteus is installed in %s mode (pinned to %s).\n\n' "$MODE" "$image"
	printf '  Config lives in:  %s\n' "$TARGET_CONFIG_DIR"
	printf '  Command lives in: %s\n\n' "$INSTALL_PATH"
	printf 'Put an authorized WireGuard .conf file in:\n'
	printf '  %s/secrets/wireguard/\n\n' "$TARGET_CONFIG_DIR"
	printf 'Then edit pools.yaml and egress-routing.yaml there (and the .env for\n'
	printf 'ports, tailnet access, or logging), and run:\n\n'
	printf '  pr0xteus start\n\n'
}

main "$@"
