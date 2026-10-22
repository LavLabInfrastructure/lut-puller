#!/usr/bin/env sh
set -eu

CONFIG_DIR=${CONFIG_DIR:-/config}
APP_BIN=${APP_BIN:-/usr/local/bin/lut-puller}

if [ "$#" -gt 0 ]; then
    exec "$APP_BIN" "$@"
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
exec "$APP_BIN" $FILES
