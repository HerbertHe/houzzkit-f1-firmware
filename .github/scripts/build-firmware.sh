#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?Run this script with RUNNER_TEMP set to the output directory}"
sdk="$PWD/houzzkit-f1-bsp-k6199"
rootfs="$PWD/houzzkit-f1-rootfs-u2404"
restore_script="$PWD/.github/scripts/restore-rootfs.py"
defconfig=szzn_rk3568_jl-rzw-v1_ubuntu-lite-2404_defconfig

chip="$sdk/device/rockchip/.chips/rk3566_rk3568"
images="$sdk/device/rockchip/common/images"
toolchain="$sdk/prebuilts/gcc/linux-x86/aarch64/gcc-arm-10.3-2021.07-x86_64-aarch64-none-linux-gnu/bin/aarch64-none-linux-gnu-gcc"
pack_dir="$sdk/tools/linux/Linux_Pack_Firmware/rockdev"
library="$rootfs/ubuntu-rootfs/usr/lib/aarch64-linux-gnu/libLLVM-15.so.1"

fail() {
  printf '::error::%s\n' "$*" >&2
  exit 1
}

require_file() {
  [[ -f "$1" && -s "$1" ]] || fail "Firmware input is missing or empty: $1"
}

prepare_pack_tools() (
  # ArmSoM publishes the Linux Rockchip tools with an RK3568 packaging example.
  revision=9737320c3a906fc4bcf25edf222258e80fc00c5b
  checksum=06049f3eb612de5d1a3866bffe4b1d8d7dabaef4fd38b07decd79380f5af5a36
  archive_url="https://raw.githubusercontent.com/ArmSoM/rockchip-tools/$revision/downloads/firmware-pack-tools/rockdev-linux-directory-snapshot.zip"
  temp_dir=$(mktemp -d "$RUNNER_TEMP/rockdev.XXXXXXXX")
  trap 'rm -rf "$temp_dir"' EXIT
  archive="$temp_dir/rockdev.zip"
  curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 120 \
    "$archive_url" --output "$archive"
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check -
  for tool in afptool rkImageMaker; do
    unzip -p "$archive" "Linux_Pack_Firmware/rockdev/$tool" > "$temp_dir/$tool"
    require_file "$temp_dir/$tool"
    install -D -m 0755 "$temp_dir/$tool" "$pack_dir/$tool"
  done
)

# Check this board's static inputs before changing rootfs permissions or compiling.
for input in \
  "$chip/$defconfig" "$chip/parameter-ubuntu-fit.txt" "$chip/boot4recovery.its" \
  "$images/rk356x-recovery-arm64.cpio.gz" "$images/wipe_all-misc.img" \
  "$sdk/kernel/arch/arm64/configs/jl_v1_linux_defconfig" \
  "$sdk/kernel/arch/arm64/boot/dts/rockchip/rk3568-jl-rm01.dts" \
  "$sdk/kernel/ramdisk.img" "$sdk/rkbin/RKBOOT/RK3568MINIALL.ini" \
  "$sdk/u-boot/configs/rk3568_defconfig" "$sdk/rkbin/tools/mkimage" "$toolchain" \
  "$rootfs/emptydirlist.txt" "$rootfs/permission.txt" "$rootfs/symlinks.txt" \
  "$rootfs/ubuntu-rootfs/usr/lib/systemd/systemd"; do
  require_file "$input"
done
if [[ ! -s "$library" ]]; then
  require_file "$library.tar.gz"
fi

for tool in gcc python2 python3 dtc fakeroot hexdump zstd mke2fs e2fsck /usr/bin/time; do
  command -v "$tool" >/dev/null || fail "Host build tool is missing: $tool"
done
for header in openssl/ssl.h gmp.h mpc.h; do
  gcc -E -include "$header" - </dev/null >/dev/null || fail "Host header is missing: $header"
done
[[ -x "$toolchain" ]] || fail "Cross-compiler is not executable: $toolchain"
"$toolchain" --version
prepare_pack_tools

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
  ./build.sh all </dev/null

  manifest=output/update/Image/package-file
  require_file "$manifest"
  for input in MiniLoaderAll.bin parameter.txt uboot.img boot.img recovery.img misc.img rootfs.img; do
    require_file "output/firmware/$input"
    awk -v name="$input" '$2 == name { found = 1 } END { exit !found }' "$manifest" ||
      fail "Rockchip package manifest is missing: $input"
  done
  image=output/firmware/update.img
  require_file "$image"
  [[ "$(head -c 4 "$image")" == RKFW ]] || fail "Invalid Rockchip firmware header: $image"
  compressed="$RUNNER_TEMP/houzzkit-firmware.img.zst"
  zstd -T0 -6 "$image" -o "$compressed"
  zstd -t "$compressed"
)
