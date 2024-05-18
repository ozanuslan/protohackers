#! /usr/bin/env bash

set -euo pipefail

script_name="$(basename "$0")"
script_dir="$(dirname "$0")"

function usage() {
    cat <<USAGE >&2
Usage: $script_name [options] <problem>
Options:
    -p, --port <port>   Port to run the server on          (default: 1337)
    -b, --bind <ip>     IP address to bind the server to   (default: 0.0.0.0)
    -d, --dev           Run the server in development mode (default: false)
    -l, --list          List available problems
    -h, --help          Display this help message
USAGE
    exit 1
}

problem_name_mapping=(
    "0:smoke_test"
    "1:prime_time"
    "2:means_to_an_end"
    "3:budget_chat"
    "4:unusual_database_program"
    "5:mob_in_the_middle"
    "6:speed_daemon"
    "7:line_reversal"
    "8:insecure_sockets_layer"
    "9:job_centre"
    "10:voracious_code_storage"
    "11:pest_control"
)

function available_problems() {
    echo "Available problems:" >&2
    for mapping in "${problem_name_mapping[@]}"; do
        if [[ -d "$script_dir/../problems/${mapping#*:}" ]]; then
            echo "  - ${mapping%%:*}: ${mapping#*:}" >&2
        fi
    done
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
    -l | --list)
        available_problems
        exit 0
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

if [[ "$problem" =~ ^[0-9]+$ ]]; then
    for mapping in "${problem_name_mapping[@]}"; do
        if [[ "$problem" == "${mapping%%:*}" ]]; then
            problem="${mapping#*:}"
            break
        fi
    done
fi

if [[ "$problem" =~ ^[0-9]+$ ]]; then
    echo "Invalid problem index: $problem" >&2
    available_problems
    exit 1
fi

if [[ -z "${problem-}" ]]; then
    echo "Argument missing: <problem>" >&2
    usage
fi

problem_dir="$(realpath "$script_dir/../problems/$problem")"
if [[ ! -d "$problem_dir" ]]; then
    echo "Problem has no directory: $problem" >&2
    available_problems
    exit 1
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
