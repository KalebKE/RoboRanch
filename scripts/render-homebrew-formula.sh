#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version-tag> <checksums-file>" >&2
  exit 2
fi

tag="$1"
checksums="$2"
version="${tag#v}"

case "$tag" in
  v*) ;;
  *)
    echo "version must be a tag like v0.1.0" >&2
    exit 2
    ;;
esac

sha_for() {
  local goos="$1"
  local goarch="$2"
  local file="roboranch_${tag}_${goos}_${goarch}.tar.gz"
  awk -v file="$file" '$2 == file || $2 == "*" file {print $1}' "$checksums"
}

darwin_arm64_sha="$(sha_for darwin arm64)"
darwin_amd64_sha="$(sha_for darwin amd64)"
linux_arm64_sha="$(sha_for linux arm64)"
linux_amd64_sha="$(sha_for linux amd64)"

for value in "$darwin_arm64_sha" "$darwin_amd64_sha" "$linux_arm64_sha" "$linux_amd64_sha"; do
  if [ -z "$value" ]; then
    echo "missing checksum in $checksums" >&2
    exit 1
  fi
done

cat <<FORMULA
class Roboranch < Formula
  desc "Android emulator and device lease broker for local development and CI"
  homepage "https://github.com/KalebKE/RoboRanch"
  version "$version"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/KalebKE/RoboRanch/releases/download/$tag/roboranch_${tag}_darwin_arm64.tar.gz"
      sha256 "$darwin_arm64_sha"
    end

    on_intel do
      url "https://github.com/KalebKE/RoboRanch/releases/download/$tag/roboranch_${tag}_darwin_amd64.tar.gz"
      sha256 "$darwin_amd64_sha"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/KalebKE/RoboRanch/releases/download/$tag/roboranch_${tag}_linux_arm64.tar.gz"
      sha256 "$linux_arm64_sha"
    end

    on_intel do
      url "https://github.com/KalebKE/RoboRanch/releases/download/$tag/roboranch_${tag}_linux_amd64.tar.gz"
      sha256 "$linux_amd64_sha"
    end
  end

  def install
    bin.install "roboranch"
  end

  test do
    assert_match "Usage: roboranch", shell_output("#{bin}/roboranch --help 2>&1")
  end
end
FORMULA

