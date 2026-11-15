#!/bin/sh
# Installs the itervox release binary for the itervox GitHub Action (#252):
# downloads itervox_<os>_<arch>.tar.gz and the release's checksums.txt,
# verifies the archive's SHA-256 against it, and puts the binary on the job's
# PATH. Refuses (exit 1) on any mismatch or missing checksum.
#
# Inputs: ITERVOX_VERSION ("latest", "0.3.1" or "v0.3.1"), ITERVOX_REPO
# (default vnovick/itervox), RUNNER_OS, RUNNER_ARCH, RUNNER_TEMP,
# GITHUB_PATH; GH_TOKEN for resolving "latest" (gh). Writes version=<tag>
# to $GITHUB_OUTPUT when set. Needs sh, curl, tar and sha256sum or shasum.
set -eu

repo=${ITERVOX_REPO:-vnovick/itervox}
version=${ITERVOX_VERSION:-latest}

if [ "$version" = latest ]; then
	version=$(gh api "repos/$repo/releases/latest" --jq .tag_name)
fi
case $version in
v*) tag=$version ;;
*) tag=v$version ;;
esac
case $tag in
v[0-9]*) ;;
*)
	echo "itervox action: invalid version '$tag'" >&2
	exit 1
	;;
esac

case ${RUNNER_OS:-Linux} in
Linux) os=linux ;;
macOS) os=darwin ;;
*)
	echo "itervox action: unsupported runner OS ${RUNNER_OS:-}" >&2
	exit 1
	;;
esac
case ${RUNNER_ARCH:-X64} in
X64) arch=amd64 ;;
ARM64) arch=arm64 ;;
*)
	echo "itervox action: unsupported runner architecture ${RUNNER_ARCH:-}" >&2
	exit 1
	;;
esac

asset="itervox_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$tag"
dir="${RUNNER_TEMP:-/tmp}/itervox-$tag"
mkdir -p "$dir/bin"
curl -fsSL --retry 3 -o "$dir/$asset" "$base/$asset"
curl -fsSL --retry 3 -o "$dir/checksums.txt" "$base/checksums.txt"

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$dir/checksums.txt")
if [ -z "$want" ]; then
	echo "itervox action: checksums.txt of $tag has no entry for $asset" >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$dir/$asset" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$dir/$asset" | awk '{ print $1 }')
fi
if [ "$got" != "$want" ]; then
	echo "itervox action: checksum mismatch for $asset ($got, want $want)" >&2
	exit 1
fi
echo "itervox action: $asset $tag checksum verified"

tar -xzf "$dir/$asset" -C "$dir/bin" itervox
chmod +x "$dir/bin/itervox"
if [ -n "${GITHUB_PATH:-}" ]; then
	echo "$dir/bin" >>"$GITHUB_PATH"
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	echo "version=$tag" >>"$GITHUB_OUTPUT"
fi
"$dir/bin/itervox" --version
