#! /usr/bin/env bash

set -euo pipefail

script_name="$(basename "$0")"
script_dir="$(cd "$(dirname "$0")" && pwd)"

function usage() {
    cat <<USAGE >&2
Usage: $script_name [options] <problem>
Options:
    -p, --port <port>   Port to run the server on          (default: 1337)
    -b, --bind <ip>     IP address to bind the server to   (default: 0.0.0.0)
    -d, --dev           Run the server in development mode (default: false)
    -h, --help          Display this help message
USAGE
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
    -p | --port)
        port="$2"
        shift 2
        ;;
    -b | --bind-ip)
        bind_ip="$2"
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
port="${port-1337}"
bind_ip="${bind_ip-0.0.0.0}"
dev_mode="${dev_mode-false}"

if [[ -z "${problem-}" ]]; then
    echo "Argument missing: <problem>" >&2
    usage
fi

problem_dir="$script_dir/../problems/$problem"
if [[ ! -d "$problem_dir" ]]; then
    echo "Problem directory does not exist: $problem_dir" >&2
    exit 1
fi
problem_dir="$(realpath "$problem_dir")"

if [[ -z "$port" ]]; then
    echo "port is not set" >&2
    usage
fi

export IP="$bind_ip"
export PORT="$port"

cat <<CONFIG >&2
# Configuration
- IP   : $IP
- PORT : $PORT
CONFIG

if $dev_mode; then
    if ! command -v go &>/dev/null; then
        echo "Go is not installed" >&2
        exit 1
    fi
    cd "$problem_dir"

    echo "Running in development mode" >&2
    echo "Go version: $(go version)" >&2

    go mod tidy
    go mod download
    go run .
else
    img_name="$("$script_dir/build-image.sh" "$problem")"
    echo "Running problem image: $img_name" >&2
    docker run --rm -p "$IP:$PORT:$PORT" -e PORT="$PORT" "$img_name"
fi
