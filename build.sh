#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$repo_root"

if [[ $# -gt 1 ]]; then
    echo "Usage: bash ./build.sh [amd64|arm64]" >&2
    exit 1
fi

target_arch="${1:-amd64}"
case "$target_arch" in
    amd64|arm64)
        ;;
    *)
        echo "Unsupported Linux architecture: $target_arch" >&2
        echo "Supported architectures: amd64, arm64" >&2
        exit 1
        ;;
esac

required_frontend_files=(
    "frontend/index.html"
    "frontend/static/app.js"
    "frontend/static/vendor/vue/dist/vue.js"
    "frontend/static/vendor/element-ui/lib/index.js"
    "frontend/static/vendor/element-ui/lib/theme-chalk/index.css"
    "frontend/static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.woff"
    "frontend/static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.ttf"
)

for relative_path in "${required_frontend_files[@]}"; do
    if [[ ! -f "$relative_path" ]]; then
        echo "Required frontend file is missing: $relative_path" >&2
        exit 1
    fi
done

if ! command -v node >/dev/null 2>&1; then
    echo "Node.js is required to check frontend/static/app.js before building." >&2
    exit 1
fi
if ! command -v go >/dev/null 2>&1; then
    echo "Go is required to build ./cmd/ncpt." >&2
    exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
    hash_command=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
    hash_command=(shasum -a 256)
else
    echo "sha256sum or shasum is required to calculate the artifact hash." >&2
    exit 1
fi

echo "Checking frontend/static/app.js ..."
node --check "frontend/static/app.js"

go_cache="$repo_root/.tmp-go-build-cache"
output_dir="$repo_root/dist"
output_path="$output_dir/ncpt-linux-$target_arch"
mkdir -p "$go_cache" "$output_dir"

echo "Building linux/$target_arch ..."
CGO_ENABLED=0 \
GOOS=linux \
GOARCH="$target_arch" \
GOCACHE="$go_cache" \
go build -trimpath -o "$output_path" ./cmd/ncpt

artifact_size="$(wc -c < "$output_path" | tr -d '[:space:]')"
artifact_hash="$("${hash_command[@]}" "$output_path" | awk '{print toupper($1)}')"

echo
echo "Build succeeded."
echo "Output : $output_path"
echo "Size   : $artifact_size bytes"
echo "SHA256 : $artifact_hash"
