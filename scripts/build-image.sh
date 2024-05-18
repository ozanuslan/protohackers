#! /usr/bin/env bash

set -euo pipefail

problem="${1-}"

if [ -z "$problem" ]; then
    echo "Usage: $0 <challenge-name>"
    exit 1
fi

script_dir="$(dirname "$0")"
problem_dir="$(realpath "$script_dir/../problems/$problem")"
dockerfile_path="$(realpath "$script_dir/../Dockerfile")"

if [ ! -d "$problem_dir" ]; then
    echo "Challenge directory not found: $problem_dir" >&2
    exit 1
fi

if [ ! -f "$dockerfile_path" ]; then
    echo "Dockerfile not found: $dockerfile_path" >&2
    exit 1
fi

img_name="protohackers_$problem"

docker image prune -f --filter "label=$img_name" >&2 && echo "Pruned dangling images with label $img_name" >&2 || echo "No dangling images with label $img_name to prune" >&2

docker build -t "$img_name" -f "$dockerfile_path" --label "$img_name" "$problem_dir" >&2

docker image prune -f --filter "label=auto_prune=true" >&2 && echo "Pruned dangling images with label auto_prune=true" >&2 || echo "No dangling images with label auto_prune=true to prune" >&2

echo "$img_name"
