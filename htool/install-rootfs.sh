#!/usr/bin/env bash
set -euo pipefail

rootfs=${1:?rootfs tree is required}
binary=${2:?htoold Linux ARM64 binary is required}
pam_helper=${3:?htoold Linux ARM64 PAM helper is required}
project=$(dirname "$(realpath "$0")")
rootfs=$(realpath "$rootfs")
[[ "$rootfs" != / ]] || { echo "Use an offline rootfs" >&2; exit 1; }
mode=${4:-fresh}
[[ "$mode" == fresh || "$mode" == preserve-state ]] || exit 1

test -f "$rootfs/etc/passwd"
test -f "$rootfs/etc/ssh/sshd_config"
test -x "$rootfs/usr/sbin/useradd"
test -x "$rootfs/usr/sbin/chpasswd"
test -x "$rootfs/usr/bin/su"
test -x "$rootfs/usr/sbin/avahi-daemon"
test -f "$rootfs/etc/avahi/avahi-daemon.conf"
test -e "$rootfs/usr/lib/aarch64-linux-gnu/libpam.so.0"
test -f "$rootfs/usr/lib/aarch64-linux-gnu/security/pam_unix.so"
install -D -m 0755 "$binary" "$rootfs/usr/local/bin/htoold"
install -D -m 0750 "$pam_helper" "$rootfs/usr/local/libexec/htoold-pam"
install -D -m 0644 "$project/pam/htoold" "$rootfs/etc/pam.d/htoold"
install -D -m 0644 "$project/systemd/htool.sysusers.conf" "$rootfs/usr/lib/sysusers.d/htool.conf"
install -d -m 0700 "$rootfs/etc/htool"
install -d -m 0755 "$rootfs/etc/systemd/system/ssh.service.d"
if [[ "$mode" == fresh ]]; then
  rm -f -- "$rootfs"/etc/ssh/ssh_host_*
  cat > "$rootfs/etc/systemd/system/ssh.service.d/10-generate-host-keys.conf" <<'EOF'
[Service]
ExecStartPre=
ExecStartPre=/usr/bin/ssh-keygen -A
ExecStartPre=/usr/sbin/sshd -t
EOF
fi
# Announce addresses on the same link which accepts setup connections.
sed -i -E '/^[[:space:]]*(host-name|allow-interfaces)[[:space:]]*=/d; /^\[server\]$/a host-name=houzzkit\nallow-interfaces=eno1' \
  "$rootfs/etc/avahi/avahi-daemon.conf"
for unit in "$project"/systemd/*.{service,socket}; do
  install -D -m 0644 "$unit" "$rootfs/usr/lib/systemd/system/${unit##*/}"
done
systemd-sysusers --root="$rootfs" htool.conf
rm -f -- "$rootfs/usr/lib/systemd/system/htool-web.service"
if [[ -L "$rootfs/etc/systemd/system/multi-user.target.wants/htool-web.service" ]]; then
  rm -- "$rootfs/etc/systemd/system/multi-user.target.wants/htool-web.service"
fi
if [[ "$mode" == fresh ]]; then
  # The first-boot form is the only source of the initial root password.
  usermod --root "$rootfs" --lock root
  printf 'uninitialized\n' > "$rootfs/etc/htool/uninitialized"
  chmod 0600 "$rootfs/etc/htool/uninitialized"
fi
systemctl --root="$rootfs" enable avahi-daemon.service htool-account.socket htool-web.socket htool-redirect.socket htoold.service
