"""Publishes a signed release to a GitHub repository.

  python scripts/publish_release.py --repo OWNER/NAME --token-file <file> \
      --dir dist/release-1.2.0 [--init-readme] [--dry-run]
Creates the release v<version> (pre-release for versions with a suffix) and uploads exactly
what the in-app update needs: the three packages, release.json, SHA256SUMS and
SHA256SUMS.sig (the offline bundle is not uploaded). A repository without any commit cannot
carry a tag: --init-readme then creates a README.md as the first commit (no source code is
published). The token needs "Contents: read and write" on this repository only; it is read
from a file and never printed.
"""
import argparse
import base64
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.github.com"
UPLOADS = "https://uploads.github.com"

README = """# OpenSignage releases

This repository carries signed releases of OpenSignage for the in-app update
(Settings → Updates).
"""


class GitHub:
    def __init__(self, token: str, dry_run: bool):
        self.token, self.dry_run = token, dry_run

    def call(self, method: str, url: str, body=None, data: bytes | None = None, ctype="application/json"):
        if self.dry_run and method != "GET":
            print(f"  (dry run) {method} {url}")
            return {}
        if body is not None:
            data = json.dumps(body).encode()
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Accept", "application/vnd.github+json")
        req.add_header("X-GitHub-Api-Version", "2022-11-28")
        req.add_header("Authorization", f"Bearer {self.token}")
        if data is not None:
            req.add_header("Content-Type", ctype)
        try:
            with urllib.request.urlopen(req, timeout=600) as r:
                raw = r.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            msg = e.read().decode(errors="replace")[:500]
            raise SystemExit(f"{method} {url.split('?')[0]} -> HTTP {e.code}: {msg}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--dir", required=True, help="dist/release-<version> written by osrelease assemble")
    ap.add_argument("--init-readme", action="store_true", help="create README.md when the repository is empty")
    ap.add_argument("--dry-run", action="store_true")
    a = ap.parse_args()

    rel = json.load(open(os.path.join(a.dir, "release.json"), encoding="utf-8"))
    version = rel["version"]
    files = [f"opensignage-{version}-linux-{arch}.tar.gz" for arch in ("amd64", "arm64", "armv7")]
    files += ["release.json", "SHA256SUMS", "SHA256SUMS.sig"]
    for f in files:
        if not os.path.isfile(os.path.join(a.dir, f)):
            raise SystemExit(f"missing {f} in {a.dir}")
    gh = GitHub(open(a.token_file, encoding="utf-8").read().strip(), a.dry_run)

    repo = gh.call("GET", f"{API}/repos/{a.repo}")
    branch = repo.get("default_branch") or "main"
    empty = False
    try:
        gh.call("GET", f"{API}/repos/{a.repo}/commits?per_page=1")
    except SystemExit as e:
        if "HTTP 409" not in str(e):
            raise
        empty = True
    if empty:
        if not a.init_readme:
            raise SystemExit("the repository has no commit; run again with --init-readme")
        print("creating README.md as the first commit")
        gh.call("PUT", f"{API}/repos/{a.repo}/contents/README.md",
                {"message": "Repository for OpenSignage releases",
                 "content": base64.b64encode(README.encode()).decode(), "branch": branch})

    tag = f"v{version}"
    for r in gh.call("GET", f"{API}/repos/{a.repo}/releases?per_page=100"):
        if r.get("tag_name") == tag:
            raise SystemExit(f"release {tag} exists already: {r.get('html_url')}")
    pre = "-" in version
    print(f"creating release {tag} ({'pre-release' if pre else 'release'}) on {branch}")
    created = gh.call("POST", f"{API}/repos/{a.repo}/releases",
                      {"tag_name": tag, "target_commitish": branch, "name": f"OpenSignage {version}",
                       "body": rel.get("notes") or f"OpenSignage {version}", "prerelease": pre})
    rid = created.get("id", 0)
    for f in files:
        p = os.path.join(a.dir, f)
        size = os.path.getsize(p)
        print(f"  uploading {f} ({size / 1e6:.1f} MB)")
        with open(p, "rb") as fh:
            gh.call("POST", f"{UPLOADS}/repos/{a.repo}/releases/{rid}/assets?name={urllib.parse.quote(f)}",
                    data=fh.read(), ctype="application/octet-stream")
    if not a.dry_run:
        r = gh.call("GET", f"{API}/repos/{a.repo}/releases/{rid}")
        names = sorted(x["name"] for x in r.get("assets", []))
        print("published:", r.get("html_url"), names)
        if names != sorted(files):
            raise SystemExit("asset list differs from the expected files")


if __name__ == "__main__":
    sys.exit(main())
