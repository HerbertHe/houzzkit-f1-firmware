#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?Run this script with RUNNER_TEMP set to the output directory}"
flavor=${1:-ubuntu}
sdk="$PWD/houzzkit-f1-bsp-k6199"
armbian_profile="$PWD/houzzkit-f1-rootfs-armbian"
rootfs="$PWD/houzzkit-f1-rootfs-ubuntu"
restore_script="$PWD/.github/scripts/restore-rootfs.py"
defconfig=szzn_rk3568_jl-rzw-v1_ubuntu-lite-2404_defconfig
ubuntu_defconfig=$defconfig
rootfs_image="$sdk/rootfs/ubuntu-rootfs.img"

chip="$sdk/device/rockchip/.chips/rk3566_rk3568"
partition_template="$chip/parameter-ubuntu-fit.txt"
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
require_file "$RUNNER_TEMP/htoold-arm64"
require_file "$RUNNER_TEMP/htoold-pam-arm64"

for input in \
  "$chip/$defconfig" "$partition_template" "$chip/boot4recovery.its" \
  "$images/rk356x-recovery-arm64.cpio.gz" "$images/wipe_all-misc.img" \
  "$sdk/kernel/arch/arm64/configs/jl_v1_linux_defconfig" \
  "$sdk/kernel/arch/arm64/boot/dts/rockchip/rk3568-jl-rm01.dts" \
  "$sdk/kernel/ramdisk.img" "$sdk/rkbin/RKBOOT/RK3568MINIALL.ini" \
  "$sdk/u-boot/configs/rk3568_defconfig" "$sdk/rkbin/tools/mkimage" "$toolchain"; do
  require_file "$input"
done
case "$flavor" in
  ubuntu)
    for input in "$rootfs/emptydirlist.txt" "$rootfs/permission.txt" "$rootfs/symlinks.txt" \
      "$rootfs/ubuntu-rootfs/usr/lib/systemd/systemd"; do
      require_file "$input"
    done
    if [[ ! -s "$library" ]]; then
      require_file "$library.tar.gz"
    fi
    ;;
  armbian)
    for input in version.env config-houzzkit.conf prepare-rootfs.sh configure-rootfs.sh; do
      require_file "$armbian_profile/$input"
    done
    docker info >/dev/null || fail "Armbian rootfs requires Docker on the Linux build runner"
    ;;
  *) fail "Unknown firmware variant: $flavor" ;;
esac

for tool in gcc python2 python3 dtc fakeroot hexdump zstd mke2fs e2fsck /usr/bin/time; do
  command -v "$tool" >/dev/null || fail "Host build tool is missing: $tool"
done
for header in openssl/ssl.h gmp.h mpc.h; do
  gcc -E -include "$header" - </dev/null >/dev/null || fail "Host header is missing: $header"
done
[[ -x "$toolchain" ]] || fail "Cross-compiler is not executable: $toolchain"
"$toolchain" --version
prepare_pack_tools

if [[ "$flavor" == ubuntu ]]; then
  (cd "$rootfs" && sudo python3 "$restore_script")
  rootfs_tree="$rootfs/ubuntu-rootfs"
else
  staging=$(mktemp -d "$RUNNER_TEMP/f1-armbian.XXXXXXXX")
  arm_kernel=jl_v1_armbian_defconfig
  defconfig=szzn_rk3568_jl-rzw-v1_armbian-noble_defconfig
  rootfs_image="$sdk/rootfs/armbian-rootfs.img"
  kernel_config="$sdk/kernel/arch/arm64/configs/$arm_kernel"
  trap 'rm -f -- "$kernel_config" "$chip/$defconfig"; sudo rm -rf -- "$staging"' EXIT
  sed -e 's|^PREBUILT_ROOTFS_IMG=.*|PREBUILT_ROOTFS_IMG="rootfs/armbian-rootfs.img"|' \
    -e "s|^RK_KERNEL_CFG=.*|RK_KERNEL_CFG=\"$arm_kernel\"|" "$chip/$ubuntu_defconfig" > "$chip/$defconfig"
  cp "$sdk/kernel/arch/arm64/configs/jl_v1_linux_defconfig" "$kernel_config"
  "$sdk/kernel/scripts/config" --file "$kernel_config" -e MEMCG -e SWAP -e ZRAM -e CRYPTO_LZ4 \
    -e DEVTMPFS -e DEVTMPFS_MOUNT
  rootfs_tree="$staging/rootfs"
  root_partuuid=$(sed -n 's/^uuid:rootfs=//p' "$partition_template")
  [[ "$root_partuuid" =~ ^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$ ]] ||
    fail "Invalid rootfs PARTUUID in the F1 partition table"
  bash "$armbian_profile/prepare-rootfs.sh" "$rootfs_tree" "$rootfs/ubuntu-rootfs/usr/lib/firmware" "$root_partuuid"

fi

# Capture the version from the same boot target that the BSP packs.
(cd "$sdk" && ./build.sh "$defconfig" && ./build.sh extboot </dev/null)
kernel_release=$(cat "$sdk/kernel/include/config/kernel.release")
[[ "$kernel_release" =~ ^[0-9][A-Za-z0-9._+-]*$ ]] || fail "Invalid BSP kernel release"

if [[ "$flavor" == armbian ]]; then
  modules="$sdk/kernel/extboot/lib/modules/$kernel_release"
  require_file "$modules/modules.order"
  sudo install -d "$rootfs_tree/usr/lib/modules/$kernel_release"
  sudo rsync -a --exclude=build --exclude=source "$modules/" "$rootfs_tree/usr/lib/modules/$kernel_release/"
  sudo depmod -b "$rootfs_tree" "$kernel_release"
  for feature in MEMCG SWAP ZRAM CRYPTO_LZ4; do
    grep -qx "CONFIG_$feature=y" "$sdk/kernel/.config" || fail "Missing Armbian kernel feature: $feature"
  done
fi

sudo bash "$PWD/.github/scripts/configure-apt-sources.sh" "$rootfs_tree" "$flavor"
if [[ "$flavor" == ubuntu ]]; then
  sudo unshare --mount --propagation private bash "$PWD/.github/scripts/install-ubuntu-compose.sh" "$rootfs_tree"
fi

case "$flavor" in
  ubuntu) distro_version=24.04 ;;
  armbian)
    source "$armbian_profile/version.env"
    distro_version=$ARMBIAN_VERSION
    ;;
esac
if [[ -n ${FIRMWARE_VERSION:-} ]]; then
  build_run_id=${GITHUB_RUN_ID:?GitHub Actions run ID is required for firmware metadata}
  [[ "$build_run_id" =~ ^[0-9]+$ ]] || fail "Invalid GitHub Actions run ID"
  read -r partition_template_sha256 _ < <(sha256sum "$partition_template")
  [[ "$partition_template_sha256" =~ ^[a-f0-9]{64}$ ]] || fail "Invalid partition template checksum"
  jq -n --arg version "$FIRMWARE_VERSION" --arg flavor "$flavor" \
    --arg distribution_version "$distro_version" --arg source_commit "$(git rev-parse HEAD)" \
    --arg build_run_id "$build_run_id" --arg kernel_release "$kernel_release" \
    --arg partition_template_sha256 "$partition_template_sha256" \
    '{version: $version, flavor: $flavor, distribution_version: $distribution_version,
      source_commit: $source_commit, board: "houzzkit-force1", build_run_id: $build_run_id,
      kernel_release: $kernel_release, partition_layout_id: "rockchip-gpt-single-rootfs-v1",
      partition_template_sha256: $partition_template_sha256}' > "$RUNNER_TEMP/houzzkit-firmware.json"
  sudo install -m 0644 "$RUNNER_TEMP/houzzkit-firmware.json" "$rootfs_tree/etc/houzzkit-firmware.json"
fi

sudo bash "$PWD/htool/install-rootfs.sh" "$rootfs_tree" \
  "$RUNNER_TEMP/htoold-arm64" "$RUNNER_TEMP/htoold-pam-arm64"

# Size from allocated blocks, with space for the ext4 journal and inode tables.
read -r used_mib _ < <(sudo du -sm "$rootfs_tree")
sudo mke2fs -q -t ext4 -F -m 0 -d "$rootfs_tree" \
  "$rootfs_image" "$((used_mib + used_mib / 5 + 256))M"
sudo chown "$(id -u):$(id -g)" "$rootfs_image"
e2fsck -fn "$rootfs_image"

(
  cd "$sdk"
  ./build.sh "$defconfig"
  ./build.sh all </dev/null
  [[ "$(cat kernel/include/config/kernel.release)" == "$kernel_release" ]] ||
    fail "Packaged kernel release differs from the prebuilt boot target"

  manifest=output/update/Image/package-file
  require_file "$manifest"
  for input in MiniLoaderAll.bin parameter.txt uboot.img boot.img recovery.img misc.img rootfs.img; do
    require_file "output/firmware/$input"
    awk -v name="$input" '$2 == name { found = 1 } END { exit !found }' "$manifest" ||
      fail "Rockchip package manifest is missing: $input"
  done
  image=output/firmware/update.img
  require_file "$image"
  # The BSP publishes this path as a symlink. zstd expects a regular file.
  image=$(realpath "$image")
  [[ "$(head -c 4 "$image")" == RKFW ]] || fail "Invalid Rockchip firmware header: $image"
  compressed="$RUNNER_TEMP/houzzkit-firmware.img.zst"
  zstd -T0 -6 "$image" -o "$compressed"
  zstd -t "$compressed"
)
