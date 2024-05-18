#! /usr/bin/env bash

set -euo pipefail

challenge_name="${1-}"

if [ -z "$challenge_name" ]; then
    echo "Usage: $0 <challenge-name>"
    exit 1
fi

script_dir="$(dirname "$0")"
challenge_dir="$(realpath "$script_dir/../$challenge_name")"
dockerfile_path="$(realpath "$script_dir/../Dockerfile")"

if [ ! -d "$challenge_dir" ]; then
    echo "Challenge directory not found: $challenge_dir" >&2
    exit 1
fi

if [ ! -f "$dockerfile_path" ]; then
    echo "Dockerfile not found: $dockerfile_path" >&2
    exit 1
fi

img_name="protohackers_$challenge_name"

docker image prune -f --filter "label=$img_name" >&2 && echo "Pruned dangling images with label $img_name" >&2 || echo "No dangling images with label $img_name to prune" >&2

docker build -t "$img_name" -f "$dockerfile_path" --label "$img_name" "$challenge_dir" >&2

docker image prune -f --filter "label=auto_prune=true" >&2 && echo "Pruned dangling images with label auto_prune=true" >&2 || echo "No dangling images with label auto_prune=true to prune" >&2

echo "$img_name"
