#!/usr/bin/env python3
"""Build the Unraid plugin package and installer.

    python3 scripts/build.py --version 2026.10.23 --repo OWNER/REPO

Writes dist/lincstation-led-<version>-x86_64-1.txz, dist/lincstation-led.plg and dist/SHA256SUMS.
The Go binary is built from go/ unless --binary points at a ready-made static one.
"""
import argparse
import datetime
import hashlib
import io
import lzma
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NAME = "lincstation-led"


def build_binary(version, out):
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64")
    subprocess.run(
        ["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}", "-o", out, "."],
        cwd=os.path.join(ROOT, "go"), env=env, check=True)


def make_txz(version, binary, dist):
    stage = tempfile.mkdtemp()
    try:
        shutil.copytree(os.path.join(ROOT, "plugin", "src"), stage, dirs_exist_ok=True)
        sbin = os.path.join(stage, "usr", "local", "sbin")
        os.makedirs(sbin, exist_ok=True)
        shutil.copy(binary, os.path.join(sbin, NAME))
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tar:
            entries = []
            for root, dirs, files in os.walk(stage):
                dirs.sort()
                rel = os.path.relpath(root, stage)
                if rel != ".":
                    entries.append((root, rel))
                for f in sorted(files):
                    full = os.path.join(root, f)
                    entries.append((full, os.path.relpath(full, stage)))
            executable = {f"etc/rc.d/rc.{NAME}", f"usr/local/sbin/{NAME}"}
            for full, rel in entries:
                ti = tar.gettarinfo(full, arcname=rel)
                ti.uid = ti.gid = 0
                ti.uname = ti.gname = "root"
                ti.mtime = 1_700_000_000  # reproducible output
                ti.mode = 0o755 if (ti.isdir() or rel in executable) else 0o644
                if ti.isdir():
                    tar.addfile(ti)
                else:
                    with open(full, "rb") as fh:
                        tar.addfile(ti, fh)
        data = lzma.compress(buf.getvalue(), format=lzma.FORMAT_XZ, preset=9)
    finally:
        shutil.rmtree(stage, ignore_errors=True)
    path = os.path.join(dist, f"{NAME}-{version}-x86_64-1.txz")
    with open(path, "wb") as fh:
        fh.write(data)
    return path, hashlib.sha256(data).hexdigest()


PRE = """#!/bin/bash
# Stop anything left over from an older version or the standalone service.
[ -x /etc/rc.d/rc.lincstation-led ] && /etc/rc.d/rc.lincstation-led stop
pkill -x lincstation-led 2>/dev/null
exit 0
"""

POST = """#!/bin/bash
DIR=/boot/config/plugins/lincstation-led
mkdir -p "$DIR/state"
# Keep only the package that was just installed.
for old in "$DIR"/lincstation-led-*.txz; do
  case "$old" in *"-@VERSION@-x86_64-1.txz") ;; *) rm -f "$old" ;; esac
done
rm -f "$DIR/lincstation-led.cfg"
# Re-apply the saved colour and effect (this also runs at every boot, when Unraid reinstalls plugins).
/etc/rc.d/rc.lincstation-led restore
echo ""
echo "lincstation-led @VERSION@ installed. Open Settings > Utilities > LincStation LED."
exit 0
"""

REMOVE = """#!/bin/bash
removepkg lincstation-led 2>/dev/null
rm -f /boot/config/plugins/lincstation-led/lincstation-led-*.txz
echo "lincstation-led removed. Your saved colour is kept in /boot/config/plugins/lincstation-led/state."
exit 0
"""


def make_plg(version, repo, sha, changes, dist):
    base = f"https://github.com/{repo}"
    plg = f"""<?xml version='1.0' standalone='yes'?>
<!DOCTYPE PLUGIN [
<!ENTITY name "{NAME}">
<!ENTITY author "{repo.split('/')[0]}">
<!ENTITY version "{version}">
<!ENTITY repo "{base}">
<!ENTITY pkg "&name;-&version;-x86_64-1.txz">
<!ENTITY sha256 "{sha}">
]>
<PLUGIN name="&name;" author="&author;" version="&version;" launch="Settings/&name;" icon="lincstation-led.png" min="6.9.0" support="&repo;/issues" pluginURL="&repo;/releases/latest/download/&name;.plg">

<CHANGES>
###{version}
{changes}
</CHANGES>

<FILE Run="/bin/bash">
<INLINE><![CDATA[
{PRE}]]></INLINE>
</FILE>

<FILE Name="/boot/config/plugins/&name;/&pkg;" Run="upgradepkg --install-new --reinstall">
<URL>&repo;/releases/download/v&version;/&pkg;</URL>
<SHA256>&sha256;</SHA256>
</FILE>

<FILE Run="/bin/bash">
<INLINE><![CDATA[
{POST.replace("@VERSION@", version)}]]></INLINE>
</FILE>

<FILE Run="/bin/bash" Method="remove">
<INLINE><![CDATA[
{REMOVE}]]></INLINE>
</FILE>

</PLUGIN>
"""
    path = os.path.join(dist, f"{NAME}.plg")
    with open(path, "w") as fh:
        fh.write(plg)
    return path


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--version", default=datetime.date.today().strftime("%Y.%m.%d"), help="plugin version, e.g. 2026.10.23")
    ap.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", "OWNER/REPO"), help="GitHub owner/repo that hosts the releases")
    ap.add_argument("--binary", help="use this static binary instead of building go/")
    ap.add_argument("--changes", default="- See the release notes on GitHub.", help="text for the plugin changelog")
    ap.add_argument("--out", default=os.path.join(ROOT, "dist"))
    a = ap.parse_args()
    if a.repo == "OWNER/REPO":
        print("warning: --repo not set, the installer will point at OWNER/REPO", file=sys.stderr)
    os.makedirs(a.out, exist_ok=True)
    binary = a.binary
    if not binary:
        binary = os.path.join(a.out, NAME)
        build_binary(a.version, binary)
    pkg, sha = make_txz(a.version, binary, a.out)
    plg = make_plg(a.version, a.repo, sha, a.changes, a.out)
    with open(os.path.join(a.out, "SHA256SUMS"), "w") as fh:
        fh.write(f"{sha}  {os.path.basename(pkg)}\n")
    print(pkg)
    print(plg)
    print("sha256", sha)


if __name__ == "__main__":
    main()
