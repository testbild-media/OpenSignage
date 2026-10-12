"""Resolves git merge conflicts in locale JSON files by taking the union of keys.

Usage (during a conflicted merge): python tools/i18n/merge_json.py <file>...
For keys present in both versions the version from HEAD ("ours") wins.
"""
import json
import subprocess
import sys

for path in sys.argv[1:]:
    def show(stage):
        try:
            out = subprocess.run(["git", "show", f":{stage}:{path}"], capture_output=True, check=True).stdout
            return json.loads(out.decode("utf-8"))
        except subprocess.CalledProcessError:
            return {}
    theirs, ours = show(3), show(2)
    merged = {**theirs, **ours}
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(merged, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
    subprocess.run(["git", "add", path], check=True)
    print(path, len(ours), "+", len(theirs), "->", len(merged))
