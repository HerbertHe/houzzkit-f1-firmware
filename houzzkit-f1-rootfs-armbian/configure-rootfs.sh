#!/usr/bin/env bash
set -euo pipefail

rootfs=$1
firmware=$2
version=$3
root_partuuid=$4
readelf -h "$rootfs/usr/lib/systemd/systemd" | grep 'AArch64' >/dev/null
for package in avahi-daemon docker.io docker-compose-v2 openssh-server systemd-resolved; do
  [[ "$(dpkg-query --admindir="$rootfs/var/lib/dpkg" -W -f='${Status}' "$package")" == 'install ok installed' ]]
done

install -d "$rootfs/usr/lib/firmware" "$rootfs/etc/systemd/network" \
  "$rootfs/etc/ssh/sshd_config.d" \
  "$rootfs/etc/docker" "$rootfs/etc/sysctl.d"
rsync -a "$firmware/" "$rootfs/usr/lib/firmware/"

printf 'houzzkit\n' > "$rootfs/etc/hostname"
printf 'VERSION=%s\nBOARD=houzzkit-force1\nBOARD_NAME="Houzzkit Force1"\nARCH=arm64\nDISTRIBUTION_CODENAME=noble\n' \
  "$version" > "$rootfs/etc/armbian-release"
printf '127.0.0.1 localhost\n127.0.1.1 houzzkit\n::1 localhost ip6-localhost ip6-loopback\n' > "$rootfs/etc/hosts"
printf 'PARTUUID=%s / ext4 defaults 0 1\n' "$root_partuuid" > "$rootfs/etc/fstab"
ln -sfn /run/systemd/resolve/stub-resolv.conf "$rootfs/etc/resolv.conf"
truncate -s 0 "$rootfs/etc/machine-id"
rm -f "$rootfs/root/.not_logged_in_yet" "$rootfs/usr/sbin/policy-rc.d"
# Keep the direct-connect address while accepting a DHCP lease on eno1.
cat > "$rootfs/etc/systemd/network/10-houzzkit-eno1.network" <<'EOF'
[Match]
Name=eno1

[Network]
DHCP=ipv4
Address=192.168.55.1/24

[DHCPv4]
UseRoutes=no
UseDNS=no
EOF
cat > "$rootfs/etc/systemd/network/20-houzzkit-ethernet.network" <<'EOF'
[Match]
Name=en* eth*

[Network]
DHCP=yes
EOF
cat > "$rootfs/etc/ssh/sshd_config.d/10-houzzkit.conf" <<'EOF'
PermitRootLogin no
PubkeyAuthentication yes
PasswordAuthentication yes
KbdInteractiveAuthentication no
UsePAM yes
EOF
cat > "$rootfs/etc/docker/daemon.json" <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": {"max-size": "10m", "max-file": "3"}
}
EOF
printf 'vm.swappiness=10\n' > "$rootfs/etc/sysctl.d/99-houzzkit-memory.conf"
cat > "$rootfs/etc/systemd/system/houzzkit-zram.service" <<'EOF'
[Unit]
Description=Houzzkit LZ4 compressed swap

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/zramctl /dev/zram0 --algorithm lz4 --size 2G
ExecStart=/usr/sbin/mkswap /dev/zram0
ExecStart=/usr/sbin/swapon --priority 100 /dev/zram0
ExecStop=/usr/sbin/swapoff /dev/zram0
ExecStop=/usr/sbin/zramctl --reset /dev/zram0

[Install]
WantedBy=multi-user.target
EOF
systemctl --root="$rootfs" disable ssh.socket
systemctl --root="$rootfs" set-default multi-user.target
systemctl --root="$rootfs" enable ssh.service systemd-networkd.service systemd-resolved.service \
  avahi-daemon.service docker.service containerd.service \
  houzzkit-zram.service getty@tty1.service
