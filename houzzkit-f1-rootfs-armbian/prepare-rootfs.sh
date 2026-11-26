#!/usr/bin/env bash
set -euo pipefail

profile=$(dirname "$(realpath "$0")")
rootfs=$1
firmware=$2
root_partuuid=$3
source "$profile/version.env"

framework=$(mktemp -d "$RUNNER_TEMP/armbian-build.XXXXXXXX")
trap 'sudo rm -rf -- "$framework"' EXIT
git -C "$framework" init -q
git -C "$framework" remote add origin https://github.com/armbian/build.git
git -C "$framework" fetch --quiet --depth=1 origin "$ARMBIAN_REVISION"
git -C "$framework" checkout --quiet --detach FETCH_HEAD
[[ "$(git -C "$framework" rev-parse HEAD)" == "$ARMBIAN_REVISION" ]]
mkdir -p "$framework/userpatches"
cp "$profile/config-houzzkit.conf" "$framework/userpatches/"
(
  cd "$framework"
  ./compile.sh rootfs houzzkit ARCH=arm64 RELEASE=noble BUILD_MINIMAL=yes BUILD_DESKTOP=no \
    PREFER_DOCKER=yes ROOTFS_COMPRESSION_RATIO=5
)

shopt -s nullglob
archives=("$framework"/cache/rootfs/rootfs-arm64-noble-*.tar.zst)
if (( ${#archives[@]} != 1 )); then
  echo "Expected one Armbian arm64 Noble rootfs archive, found ${#archives[@]}" >&2
  exit 1
fi
zstd -t "${archives[0]}"
sudo mkdir -p "$rootfs"
sudo tar --zstd --numeric-owner --xattrs --xattrs-include='*' --acls -xpf "${archives[0]}" -C "$rootfs"
sudo bash "$profile/configure-rootfs.sh" "$rootfs" "$firmware" "$ARMBIAN_VERSION" "$root_partuuid"
