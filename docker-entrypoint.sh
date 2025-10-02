#!/usr/bin/env sh
set -eu

CONFIG_DIR=${CONFIG_DIR:-/config}
PYTHON_BIN=${PYTHON_BIN:-python3}
APP_PATH=${APP_PATH:-/app/lut_puller.py}

if [ "$#" -gt 0 ]; then
    exec "$PYTHON_BIN" "$APP_PATH" "$@"
fi

set -- "$CONFIG_DIR"/*.yaml "$CONFIG_DIR"/*.yml
FILES=""
for file in "$@"; do
    if [ -f "$file" ]; then
        FILES="$FILES $file"
    fi
done

if [ -z "$FILES" ]; then
    echo "No YAML configuration files found in $CONFIG_DIR" >&2
    exit 1
fi

# shellcheck disable=SC2086
exec "$PYTHON_BIN" "$APP_PATH" $FILES
