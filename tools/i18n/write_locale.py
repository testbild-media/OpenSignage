"""Helper: write one locale namespace for all languages from a JSON spec file.

Usage: python write_locale.py spec.json
spec: {"namespace": "player", "keys": [...], "langs": {"en": [...], ...}}
Existing keys in the namespace file are kept unless overwritten.
"""
import json
import os
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "locales")

spec = json.load(open(sys.argv[1], encoding="utf-8"))
ns, keys = spec["namespace"], spec["keys"]
for lang, vals in spec["langs"].items():
    assert len(vals) == len(keys), (lang, len(vals), len(keys))
    path = os.path.join(ROOT, lang, ns + ".json")
    data = {}
    if os.path.exists(path):
        data = json.load(open(path, encoding="utf-8"))
    data.update(dict(zip(keys, vals)))
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(data, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
print(ns, len(spec["langs"]), "languages,", len(keys), "keys")
