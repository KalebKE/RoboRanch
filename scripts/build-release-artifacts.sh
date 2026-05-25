#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version-tag> <output-dir>" >&2
  exit 2
fi

version="$1"
out_dir="$2"

case "$version" in
  v*) ;;
  *)
    echo "version must be a tag like v0.1.0" >&2
    exit 2
    ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"

targets=(
  "darwin arm64"
  "darwin amd64"
  "linux arm64"
  "linux amd64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<<"$target"
  package_dir="$(mktemp -d)"
  archive="roboranch_${version}_${goos}_${goarch}.tar.gz"

  (
    cd "$repo_root"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
      -trimpath \
      -ldflags "-s -w" \
      -o "$package_dir/roboranch" \
      ./cmd/roboranch
    cp README.md LICENSE "$package_dir/"
  )

  tar -C "$package_dir" -czf "$out_dir/$archive" roboranch README.md LICENSE
  rm -rf "$package_dir"
done

(
  cd "$out_dir"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 roboranch_"$version"_*.tar.gz > checksums.txt
  else
    sha256sum roboranch_"$version"_*.tar.gz > checksums.txt
  fi
)

