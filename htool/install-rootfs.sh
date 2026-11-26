#!/usr/bin/env bash
set -euo pipefail

rootfs=${1:?rootfs tree is required}
binary=${2:?htool Linux ARM64 binary is required}
project=$(dirname "$(realpath "$0")")

test -f "$rootfs/etc/passwd"
test -f "$rootfs/etc/ssh/sshd_config"
test -x "$rootfs/usr/sbin/useradd"
test -x "$rootfs/usr/sbin/chpasswd"
test -x "$rootfs/usr/bin/su"
test -x "$rootfs/usr/sbin/avahi-daemon"
test -f "$rootfs/etc/avahi/avahi-daemon.conf"
install -D -m 0755 "$binary" "$rootfs/usr/local/bin/htool"
install -D -m 0644 "$project/systemd/htool.sysusers.conf" "$rootfs/usr/lib/sysusers.d/htool.conf"
install -d -m 0700 "$rootfs/etc/htool"
install -d -m 0755 "$rootfs/etc/systemd/system/ssh.service.d"
rm -f -- "$rootfs"/etc/ssh/ssh_host_*
cat > "$rootfs/etc/systemd/system/ssh.service.d/10-generate-host-keys.conf" <<'EOF'
[Service]
ExecStartPre=
ExecStartPre=/usr/bin/ssh-keygen -A
ExecStartPre=/usr/sbin/sshd -t
EOF
# Announce addresses on the same link which accepts setup connections.
sed -i -E '/^[[:space:]]*(host-name|allow-interfaces)[[:space:]]*=/d; /^\[server\]$/a host-name=houzzkit\nallow-interfaces=eno1' \
  "$rootfs/etc/avahi/avahi-daemon.conf"
for unit in "$project"/systemd/*.{service,socket}; do
  install -D -m 0644 "$unit" "$rootfs/usr/lib/systemd/system/${unit##*/}"
done
systemd-sysusers --root="$rootfs" htool.conf
# The first-boot form is the only source of the initial root password.
usermod --root "$rootfs" --lock root
systemctl --root="$rootfs" enable avahi-daemon.service htool-account.socket htool-web.socket
