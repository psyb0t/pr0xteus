#!/bin/bash
set -euo pipefail

# pr0xteus installs a pinned release into either the current user's home or a
# shared system directory. PR0XTEUS_ENV=dev follows the same path with local
# images and a project-local command/config directory.

readonly INSTALL_LOG_FILE="/tmp/pr0xteus-install.log"
readonly SYSTEM_INSTALL_PATH="/usr/local/bin/pr0xteus"
readonly SYSTEM_CONFIG_DIR="/etc/pr0xteus"
readonly WRAPPER_MARKER="pr0xteus-managed-command"
readonly CONFIG_DIRECTORY_NAME="pr0xteus"
# shellcheck disable=SC2016  # must reach the rc file unexpanded
readonly USER_PATH_SNIPPET='export PATH="$HOME/.local/bin:$PATH"'
readonly IMAGE_REPO="psyb0t/pr0xteus"
readonly ROLLING_IMAGE="psyb0t/pr0xteus:latest"
readonly GITHUB_API_LATEST="https://api.github.com/repos/psyb0t/pr0xteus/releases/latest"
readonly WRAPPER_URL="https://raw.githubusercontent.com/psyb0t/pr0xteus/main/scripts/pr0xteus.sh"
readonly DEVELOPMENT_ENVIRONMENT="dev"
readonly DEVELOPMENT_CONTROLLER_IMAGE="psyb0t/pr0xteus:dev"

ROLLING=0
MODE=""
INSTALL_PATH=""
TARGET_CONFIG_DIR=""
WRAPPER_TEMPORARY_FILE=""

say() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
fail() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

trap 'printf "error: install failed — see %s\n" "$INSTALL_LOG_FILE" >&2' ERR
trap 'rm -f "$WRAPPER_TEMPORARY_FILE"' EXIT
exec > >(tee -a "$INSTALL_LOG_FILE") 2>&1

development_enabled() {
	case "${PR0XTEUS_ENV:-}" in
	"") return 1 ;;
	"$DEVELOPMENT_ENVIRONMENT") return 0 ;;
	*) fail "PR0XTEUS_ENV must be dev when set" ;;
	esac
}

resolve_mode() {
	if [[ -z "$MODE" ]]; then
		if ((EUID == 0)); then MODE="system"; else MODE="user"; fi
	fi

	case "$MODE" in
	system)
		((EUID == 0)) || fail "the system-wide install needs root — re-run with sudo, or use --user"
		INSTALL_PATH="$SYSTEM_INSTALL_PATH"
		TARGET_CONFIG_DIR="$SYSTEM_CONFIG_DIR"
		;;
	user)
		((EUID != 0)) || fail "the per-user install must not run as root — run it as your normal account, or use --system"
		INSTALL_PATH="$HOME/.local/bin/pr0xteus"
		TARGET_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/$CONFIG_DIRECTORY_NAME"
		;;
	*) fail "unknown mode: $MODE" ;;
	esac

	if [[ -n "${PR0XTEUS_HOME:-}" ]]; then
		[[ "$PR0XTEUS_HOME" = /* ]] || fail "PR0XTEUS_HOME must be an absolute path"
		TARGET_CONFIG_DIR="$PR0XTEUS_HOME"
	fi

	if development_enabled; then
		INSTALL_PATH="$TARGET_CONFIG_DIR/pr0xteus"
	fi
}

prepare_config_dir() {
	if [[ "$MODE" == "system" ]] && ! development_enabled; then
		install -d -m 0750 "$TARGET_CONFIG_DIR"
		if getent group docker >/dev/null 2>&1; then chgrp docker "$TARGET_CONFIG_DIR"; fi

		return
	fi

	install -d -m 0700 "$TARGET_CONFIG_DIR"
}

apply_config_permissions() {
	[[ "$MODE" == "system" ]] && ! development_enabled || return 0
	if getent group docker >/dev/null 2>&1; then chgrp -R docker "$TARGET_CONFIG_DIR"; fi
	chmod -R g-w "$TARGET_CONFIG_DIR"
	find "$TARGET_CONFIG_DIR" -type d -exec chmod g+rx {} +
	find "$TARGET_CONFIG_DIR" -type f -exec chmod g+r {} +
}

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
	tag="$(curl -fsSL "$GITHUB_API_LATEST" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
	[[ -n "$tag" ]] || fail "could not resolve the latest pr0xteus release tag"
	printf '%s\n' "$tag"
}

resolve_controller_image() {
	if development_enabled; then
		printf '%s\n' "$DEVELOPMENT_CONTROLLER_IMAGE"

		return
	fi
	if ((ROLLING)); then
		printf '%s\n' "$ROLLING_IMAGE"

		return
	fi
	printf '%s:%s\n' "$IMAGE_REPO" "$(resolve_latest_tag)"
}

prepare_controller_image() {
	local image="$1" source_dir
	if ! development_enabled; then
		docker pull "$image"

		return
	fi

	source_dir="${PR0XTEUS_SOURCE_DIR:-}"
	[[ "$source_dir" = /* && -f "$source_dir/Dockerfile" && -f "$source_dir/cell/Dockerfile" ]] ||
		fail "PR0XTEUS_ENV=dev requires PR0XTEUS_SOURCE_DIR with controller and cell Dockerfiles"
	(
		cd "$source_dir"
		PR0XTEUS_DEV_CONTROLLER_IMAGE="$DEVELOPMENT_CONTROLLER_IMAGE" bash scripts/build_controller.sh
		PR0XTEUS_CELL_TAG="${IMAGE_REPO}:cell-dev" bash scripts/build_cell.sh
	)
}

env_pinned_image() {
	local env_file="$1"
	[[ -f "$env_file" ]] || return 0
	sed -n 's/^PR0XTEUS_CONTROLLER_IMAGE=//p' "$env_file" | head -n1
}

pin_env_value() {
	local env_file="$1" key="$2" value="$3"
	[[ -f "$env_file" ]] || return 0
	if grep -q "^${key}=" "$env_file"; then
		sed -i "s|^${key}=.*|${key}=${value}|" "$env_file"
	else
		printf '%s=%s\n' "$key" "$value" >>"$env_file"
	fi
}

installer_runtime_user() {
	local user="${SUDO_USER:-}"
	if [[ -n "$user" ]]; then
		printf '%s:%s\n' "$(id -u "$user")" "$(id -g "$user")"

		return
	fi
	printf '%s:%s\n' "$(id -u)" "$(id -g)"
}

wrapper_source() {
	if development_enabled; then
		local source_dir="${PR0XTEUS_SOURCE_DIR:-}"
		local local_wrapper="$source_dir/scripts/pr0xteus.sh"
		[[ "$source_dir" = /* && -f "$local_wrapper" ]] ||
			fail "PR0XTEUS_ENV=dev requires PR0XTEUS_SOURCE_DIR/scripts/pr0xteus.sh"
		printf '%s\n' "$local_wrapper"

		return
	fi
	printf '%s\n' "$WRAPPER_URL"
}

write_command() {
	if [[ -e "$INSTALL_PATH" ]] && ! grep -Fq "$WRAPPER_MARKER" "$INSTALL_PATH"; then
		fail "$INSTALL_PATH already exists and is not managed by this installer"
	fi

	install -d "$(dirname "$INSTALL_PATH")"
	WRAPPER_TEMPORARY_FILE="$(mktemp)"
	if development_enabled; then
		install -m 0755 "$(wrapper_source)" "$WRAPPER_TEMPORARY_FILE"
	else
		curl -fsSL "$(wrapper_source)" >"$WRAPPER_TEMPORARY_FILE"
	fi
	bash -n "$WRAPPER_TEMPORARY_FILE"
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

	local previous_image image runtime_user
	previous_image="$(env_pinned_image "$TARGET_CONFIG_DIR/.env")"
	image="$(resolve_controller_image)"
	runtime_user="$(installer_runtime_user)"

	say "pinning to $image"
	prepare_controller_image "$image"
	say "writing configuration into $TARGET_CONFIG_DIR"
	docker run --rm \
		--user "$(id -u):$(id -g)" \
		-v "$TARGET_CONFIG_DIR:/config" \
		"$image" config init \
		--config-dir /config \
		--host-config-dir "$TARGET_CONFIG_DIR" \
		--controller-image "$image" \
		--runtime-user "$runtime_user" \
		--refresh-runtime-templates

	pin_env_value "$TARGET_CONFIG_DIR/.env" PR0XTEUS_CONTROLLER_IMAGE "$image"
	pin_env_value "$TARGET_CONFIG_DIR/.env" PR0XTEUS_RUNTIME_USER "$runtime_user"
	apply_config_permissions
	write_command
	if ! development_enabled; then warn_user_path; fi

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
	printf '  %s start\n\n' "$INSTALL_PATH"
}

main "$@"
