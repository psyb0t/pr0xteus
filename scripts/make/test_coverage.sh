#!/bin/bash

set -euo pipefail

trap 'printf "test coverage setup failed at %s:%d (exit %d)\\n" "${BASH_SOURCE[0]}" "$LINENO" "$?" >&2' ERR

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPOSITORY_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPOSITORY_DIR}"
# Servicepack owns coverage; this project only prepares its required cell image.
bash scripts/build_cell.sh
exec bash scripts/make/servicepack/test_coverage.sh
