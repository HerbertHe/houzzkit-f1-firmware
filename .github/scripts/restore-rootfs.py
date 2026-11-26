"""Restore the Ubuntu image metadata on a Linux build host."""

import os
import re
import subprocess
from pathlib import Path

ROOT = Path("ubuntu-rootfs")


def account_ids(account_file):
    return {
        fields[0]: int(fields[2])
        for line in (ROOT / "etc" / account_file).read_text().splitlines()
        if line and not line.startswith("#")
        for fields in [line.split(":")]
    }


def target_id(name, accounts):
    if name.isdecimal():
        return int(name)
    return accounts[name]


def main():
    users = account_ids("passwd")
    groups = account_ids("group")
    # The source manifest still uses the source builder's name and the old
    # names on two chrony paths. Use the accounts present in the target image.
    users.setdefault("lawis", 0)
    groups.setdefault("lawis", 0)
    users.setdefault("uuidd", users["_chrony"])
    groups.setdefault("uuidd", groups["_chrony"])

    for entry in Path("emptydirlist.txt").read_text().splitlines():
        Path(entry).mkdir(parents=True, exist_ok=True)

    # Git tracks the library as an archive and excludes the login databases.
    library = ROOT / "usr/lib/aarch64-linux-gnu/libLLVM-15.so.1"
    archive = library.with_suffix(".1.tar.gz")
    if archive.is_file():
        with library.open("wb") as output:
            subprocess.run(["tar", "-xOf", str(archive), library.name], stdout=output, check=True)
        archive.unlink()
    for name in ("lastlog", "wtmp", "btmp"):
        (ROOT / "var/log" / name).touch(exist_ok=True)

    # Unlisted symlinks and newly created directories must not inherit the CI UID.
    subprocess.run(["chown", "-hR", "0:0", str(ROOT)], check=True)
    accounts = {"owner": users, "group": groups}
    permissions = Path("permission.txt").read_text()
    # The upstream gitignore deliberately excludes the source builder's shell history.
    permissions = re.sub(r"^# file: ubuntu-rootfs/root/\.bash_history\n.*?\n\n",
                         "", permissions, flags=re.MULTILINE | re.DOTALL)
    permissions = re.sub(
        r"^# (owner|group): (.+)$",
        lambda match: f"# {match[1]}: {target_id(match[2], accounts[match[1]])}",
        permissions,
        flags=re.MULTILINE,
    )
    subprocess.run(["setfacl", "--restore=-"], input=permissions, text=True, check=True)

    fields = Path("symlinks.txt").read_bytes().split(b"\0")
    # The upstream find format adds a newline after each NUL-terminated record.
    if fields.pop() not in (b"", b"\n") or len(fields) % 5:
        raise ValueError("Invalid symlinks.txt")
    for index in range(0, len(fields), 5):
        _, user, group, path, _ = map(os.fsdecode, fields[index:index + 5])
        if not Path(path).is_symlink():
            raise ValueError(f"Expected a symlink: {path}")
        os.lchown(path, target_id(user, users), target_id(group, groups))


if __name__ == "__main__":
    main()
