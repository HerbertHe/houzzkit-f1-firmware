#!/usr/bin/env bash
set -euo pipefail

rootfs=$(realpath "${1:?Ubuntu rootfs is required}")
[[ "$rootfs" != / && -d "$rootfs/var/lib/dpkg" ]] || exit 1

# The Linux runner already installs qemu-user-static and binfmt-support.
update-binfmts --enable qemu-aarch64
if ! chroot "$rootfs" /usr/bin/true; then
  echo 'Cannot run ARM64 programs in the Ubuntu rootfs' >&2
  exit 1
fi

stage=$(mktemp -d "$rootfs/.f1-compose.XXXXXXXX")
inside="/${stage##*/}"
chmod 0755 "$stage"
replace_resolver=false
replace_policy=false

cleanup() {
  if [[ "$replace_resolver" == true ]]; then
    rm -f -- "$rootfs/etc/resolv.conf"
    if [[ -e "$stage/resolv.conf" || -L "$stage/resolv.conf" ]]; then
      mv -T -- "$stage/resolv.conf" "$rootfs/etc/resolv.conf"
    fi
  fi
  if [[ "$replace_policy" == true ]]; then
    rm -f -- "$rootfs/usr/sbin/policy-rc.d"
    if [[ -e "$stage/policy-rc.d" || -L "$stage/policy-rc.d" ]]; then
      mv -T -- "$stage/policy-rc.d" "$rootfs/usr/sbin/policy-rc.d"
    fi
  fi
  rm -rf -- "$stage"
}
trap cleanup EXIT

# Mounts are confined to the caller's private mount namespace.
mount --bind /dev "$rootfs/dev"
mount --bind /proc "$rootfs/proc"

if [[ -e "$rootfs/etc/resolv.conf" || -L "$rootfs/etc/resolv.conf" ]]; then
  mv -T -- "$rootfs/etc/resolv.conf" "$stage/resolv.conf"
fi
replace_resolver=true
install -m 0644 /etc/resolv.conf "$rootfs/etc/resolv.conf"

if [[ -e "$rootfs/usr/sbin/policy-rc.d" || -L "$rootfs/usr/sbin/policy-rc.d" ]]; then
  mv -T -- "$rootfs/usr/sbin/policy-rc.d" "$stage/policy-rc.d"
fi
replace_policy=true
printf '#!/bin/sh\nexit 101\n' > "$rootfs/usr/sbin/policy-rc.d"
chmod 0755 "$rootfs/usr/sbin/policy-rc.d"

# Only the temporary build source uses the upstream mirror. The firmware
# retains the TUNA configuration and uses the Ubuntu archive signing key.
sed -E 's|^URIs:.*|URIs: https://ports.ubuntu.com/ubuntu-ports/|' \
  "$rootfs/etc/apt/sources.list.d/ubuntu.sources" > "$stage/ubuntu.sources"
mkdir -m 0755 "$stage/lists" "$stage/archives"
options=(
  -o "Dir::Etc::sourcelist=$inside/ubuntu.sources"
  -o 'Dir::Etc::sourceparts=-'
  -o "Dir::State::lists=$inside/lists"
  -o "Dir::Cache::archives=$inside/archives"
  -o Dir::Cache::pkgcache=
  -o Dir::Cache::srcpkgcache=
  -o Dpkg::Use-Pty=0
  -o Acquire::Retries=3
)
export DEBIAN_FRONTEND=noninteractive
chroot "$rootfs" /usr/bin/apt-get "${options[@]}" update
chroot "$rootfs" /usr/bin/apt-get "${options[@]}" install -y --no-install-recommends docker-compose-v2
chroot "$rootfs" /usr/bin/docker compose version
