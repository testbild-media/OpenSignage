"""Creates a release tar.gz with correct Unix permissions (Windows filesystems lose them).

Usage: python scripts/pack.py <dir> <out.tar.gz>
Executables: bin/*, *.sh. Everything else 0644, directories 0755, owner root.
"""
import os
import sys
import tarfile

src, out = sys.argv[1], sys.argv[2]
base = os.path.basename(os.path.normpath(src))


def mode_for(rel: str, is_dir: bool) -> int:
    if is_dir:
        return 0o755
    parts = rel.replace("\\", "/").split("/")
    if (len(parts) >= 2 and parts[-2] == "bin") or rel.endswith(".sh"):
        return 0o755
    return 0o644


with tarfile.open(out, "w:gz", format=tarfile.PAX_FORMAT) as tar:
    for root, dirs, files in os.walk(src):
        dirs.sort()
        for name in sorted(dirs) + sorted(files):
            path = os.path.join(root, name)
            rel = os.path.relpath(path, src)
            info = tar.gettarinfo(path, arcname=f"{base}/{rel}".replace("\\", "/"))
            info.mode = mode_for(rel, os.path.isdir(path))
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            if info.isfile():
                with open(path, "rb") as f:
                    tar.addfile(info, f)
            else:
                tar.addfile(info)
print(out)
