#! /usr/bin/env bash

set -euo pipefail

script_name="$(basename "$0")"
script_dir="$(cd "$(dirname "$0")" && pwd)"

env_file="$(realpath "$script_dir/../.env")"

if [[ ! -f "$env_file" ]]; then
    echo "Environment file not found: $env_file" >&2
    exit 1
fi

. "$env_file"

function usage() {
    cat <<USAGE
Usage: $script_name [options] <problem>

Options:
    -p, --port <port>   Port to run the server on          (default: $PORT)
    -d, --dev           Run the server in development mode (default: false)
USAGE
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
    -p | --port)
        PORT="$2"
        shift 2
        ;;
    -d | --dev)
        dev_mode=true
        shift
        ;;
    -h | --help)
        usage
        ;;
    *)
        if [[ -z "${problem-}" ]]; then
            problem="$1"
        else
            echo "Unknown argument: $1" >&2
            usage
        fi
        shift
        ;;
    esac
done

problem="${problem-}"
dev_mode="${dev_mode-false}"

if [[ -z "${problem-}" ]]; then
    echo "Argument missing: <problem>" >&2
    usage
fi

problem_dir="$(realpath "$script_dir/../problems/$problem")"

if [[ ! -d "$problem_dir" ]]; then
    echo "Challenge directory does not exist: $problem_dir" >&2
    exit 1
fi

if [[ -z "$PORT" ]]; then
    echo "PORT is not set" >&2
    usage
fi

bind_ip=0.0.0.0
export IP="$bind_ip"
export PORT

if $dev_mode; then
    cd "$problem_dir"
    echo "Running in development mode" >&2
    cat <<CONFIG >&2
# Configuration
- PORT      : $PORT
CONFIG
    version="$(go version)"
    echo "Go version: $version" >&2
    go mod tidy
    go mod download
    go run .
    exit $?
else
    img_name="$("$script_dir/build-image.sh" "$problem")"
    echo "Running challenge image: $img_name" >&2
    cat <<CONFIG >&2
# Configuration
- PORT      : $PORT
CONFIG
    docker run --rm -p "$IP:$PORT:$PORT" -e PORT="$PORT" "$img_name"
fi
