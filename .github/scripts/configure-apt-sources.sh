#!/usr/bin/env bash
set -euo pipefail

rootfs=${1:?root filesystem is required}
flavor=${2:?firmware variant is required}
apt="$rootfs/etc/apt"
sources="$apt/sources.list.d"
ubuntu_key=/usr/share/keyrings/ubuntu-archive-keyring.gpg
armbian_key=/usr/share/keyrings/armbian-archive-keyring.gpg
tuna=https://mirrors.tuna.tsinghua.edu.cn

test -s "$rootfs$ubuntu_key"
install -d -m 0755 "$sources" "$apt/preferences.d"
# Replace the distro-managed entries in the two known rootfs layouts.
rm -f -- "$apt/sources.list" "$sources/ubuntu.list"
cat > "$sources/ubuntu.sources" <<EOF
Types: deb
URIs: $tuna/ubuntu-ports
Suites: noble noble-updates noble-backports
Components: main restricted universe multiverse
Signed-By: $ubuntu_key

Types: deb
URIs: $tuna/ubuntu-ports
Suites: noble-security
Components: main restricted universe multiverse
Signed-By: $ubuntu_key
EOF
case "$flavor" in
  ubuntu) ;;
  armbian)
    test -s "$rootfs$armbian_key"
    # Keep the components and keyring selected by the pinned Armbian framework.
    test -s "$sources/armbian.sources"
    sed -i -E "s|^URIs:.*|URIs: $tuna/armbian|" "$sources/armbian.sources"
    grep -Fxq "URIs: $tuna/armbian" "$sources/armbian.sources"
    grep -Fxq "Signed-By: $armbian_key" "$sources/armbian.sources"
    rm -f -- "$sources/armbian.list"
    ;;
  *) exit 1 ;;
esac

# The Rockchip boot image provides the kernel, DTB and U-Boot as a matched set.
cat > "$apt/preferences.d/90-houzzkit-bsp" <<'EOF'
Package: linux-image* linux-modules* linux-dtb* linux-u-boot* u-boot* flash-kernel
Pin: version *
Pin-Priority: -1
EOF
