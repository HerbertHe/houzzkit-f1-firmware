#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?Run this script with RUNNER_TEMP set to the output directory}"
sdk="$PWD/houzzkit-f1-bsp-k6199"
rootfs="$PWD/houzzkit-f1-rootfs-u2404"
restore_script="$PWD/.github/scripts/restore-rootfs.py"
defconfig=szzn_rk3568_jl-rzw-v1_ubuntu-lite-2404_defconfig

(
  cd "$rootfs"
  sudo python3 "$restore_script"

  # Size from allocated blocks, with space for the ext4 journal and inode tables.
  read -r used_mib _ < <(sudo du -sm ubuntu-rootfs)
  image="$sdk/rootfs/ubuntu-rootfs.img"
  sudo mke2fs -q -t ext4 -F -m 0 -d ubuntu-rootfs \
    "$image" "$((used_mib + used_mib / 5 + 256))M"
  sudo chown "$(id -u):$(id -g)" "$image"
  e2fsck -fn "$image"
)

(
  cd "$sdk"
  ./build.sh "$defconfig"
  ./build.sh </dev/null
  image=output/firmware/update.img
  test -s "$image"
  zstd -T0 -6 "$image" -o "$RUNNER_TEMP/houzzkit-firmware.img.zst"
)
