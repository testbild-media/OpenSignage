#!/usr/bin/env python3
"""Build the OpenSignage offline font bundle (fonts/).

Fetches the families listed in families.json from the official upstream
repository https://github.com/google/fonts (sparse, shallow clone into a
temporary directory outside this repository), verifies their licenses
(SIL OFL 1.1 or Apache 2.0 only) and writes

    fonts/<id>/<File>.woff2   subset, converted (families WITHOUT a Reserved Font Name)
    fonts/<id>/<File>.ttf     byte-identical upstream copy (families WITH a Reserved Font Name)
    fonts/<id>/OFL.txt | LICENSE.txt
    fonts/manifest.json
    fonts/LICENSES.md

Reserved Font Name rule (OFL 1.1 condition 3): a family whose license file or
font name table declares a Reserved Font Name is shipped UNMODIFIED, because
subsetting or converting it would create a Modified Version that may not use
that name. Only the file name is sanitised ("[", "]" and "," become "-").

The output is deterministic: the same upstream commit, the same
families.json and the same fontTools/brotli versions give identical bytes.

Usage:
    python tools/fonts/build_fonts.py                 # build from upstream main
    python tools/fonts/build_fonts.py --commit <sha>  # rebuild a pinned upstream state
    python tools/fonts/build_fonts.py --verify        # check fonts/ against its manifest

Requires: pip install fonttools brotli   (and git on PATH)
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import io
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parent.parent
CONFIG_PATH = HERE / "families.json"
DEFAULT_OUT = REPO_ROOT / "fonts"

MANIFEST_VERSION = 1
UPSTREAM_DEFAULT = "https://github.com/google/fonts"

# --------------------------------------------------------------------------
# Subsetting and coverage definitions
# --------------------------------------------------------------------------

# Code point ranges kept in every subset (only glyphs that exist in the font).
UNICODE_RANGES = [
    (0x0000, 0x00FF),  # Basic Latin + Latin-1 Supplement
    (0x0100, 0x024F),  # Latin Extended-A/B
    (0x0259, 0x0259),  # schwa
    (0x1E00, 0x1EFF),  # Latin Extended Additional
    (0x2000, 0x206F),  # General Punctuation
    (0x20A0, 0x20CF),  # Currency Symbols (incl. euro sign)
    (0x2100, 0x214F),  # Letterlike Symbols (incl. trade mark, numero)
    (0x2190, 0x2193),  # arrows left/up/right/down
    (0x2212, 0x2212),  # minus sign
    (0x2215, 0x2215),  # division slash
    (0xFEFF, 0xFEFF),  # zero width no-break space
    (0xFFFD, 0xFFFD),  # replacement character
    (0x0370, 0x03FF),  # Greek and Coptic
    (0x0400, 0x04FF),  # Cyrillic
]

SCRIPT_ORDER = ["latin", "latin-ext", "greek", "cyrillic"]

# A script counts as covered only if ALL of its test characters are present.
SCRIPT_TESTS = {
    "latin": set("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"),
    "latin-ext": set(
        "ŁłŐőŰűČčŠšŽžĘęĄąĆćŃńŚśŹźŻżĂăȘșȚțĐđĢģĶķĻļŅņŌōŪūĒēĪīĀāĖėĮįŲųĞğİıŞşŸŒœß"
    ),
    # U+0391-03A9 without the unassigned U+03A2, and U+03B1-03C9.
    "greek": {chr(c) for c in range(0x0391, 0x03AA) if c != 0x03A2}
    | {chr(c) for c in range(0x03B1, 0x03CA)},
    # U+0410-044F (A-Ya, a-ya) which contains the Bulgarian set (incl. Ъ ъ Щ щ).
    "cyrillic": {chr(c) for c in range(0x0410, 0x0450)},
}

CATEGORY_ORDER = ["sans", "serif", "display", "condensed", "script", "mono"]

CORE_STATIC_STYLES = ["Regular", "Italic", "Bold", "BoldItalic"]

# License id -> how to recognise and verify it upstream.
LICENSES = {
    "OFL-1.1": {
        "dir": "ofl",
        "file": "OFL.txt",
        "markers": ["SIL OPEN FONT LICENSE Version 1.1"],
        "metadata": "OFL",
        "label": "SIL OFL 1.1",
    },
    "Apache-2.0": {
        "dir": "apache",
        "file": "LICENSE.txt",
        "markers": ["Apache License", "Version 2.0"],
        "metadata": "APACHE2",
        "label": "Apache 2.0",
    },
}


class FamilyError(Exception):
    """A family failed verification and must be excluded."""


class FatalError(Exception):
    """The build cannot continue."""


# --------------------------------------------------------------------------
# Small helpers
# --------------------------------------------------------------------------


def log(msg: str = "") -> None:
    print(msg, flush=True)


def rmtree(path: Path) -> None:
    """shutil.rmtree that also removes read-only files (git objects on Windows)."""

    def _fix(func, p, _exc):
        try:
            os.chmod(p, stat.S_IWRITE)
            func(p)
        except OSError:
            pass

    if sys.version_info >= (3, 12):
        shutil.rmtree(path, onexc=_fix)
    else:
        shutil.rmtree(path, onerror=lambda f, p, e: _fix(f, p, e))


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def git(args: list[str], cwd: Path | None = None) -> str:
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0")
    proc = subprocess.run(
        ["git", *args],
        cwd=str(cwd) if cwd else None,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if proc.returncode != 0:
        raise FatalError(
            f"git {' '.join(args)} failed ({proc.returncode}):\n{proc.stderr.strip()}"
        )
    return proc.stdout.strip()


def sanitize_stem(stem: str) -> str:
    """Montserrat[wght] -> Montserrat-wght, Archivo[wdth,wght] -> Archivo-wdth-wght."""
    out = re.sub(r"[\[\],]+", "-", stem)
    out = re.sub(r"-{2,}", "-", out).strip("-")
    return out


def as_number(value: float) -> int | float:
    return int(value) if float(value).is_integer() else round(float(value), 3)


def normalize_text(data: bytes) -> bytes:
    """License texts are stored with LF line endings regardless of the clone's autocrlf."""
    return data.replace(b"\r\n", b"\n").replace(b"\r", b"\n")


def first_copyright_line(text: str) -> str | None:
    placeholder = re.compile(r"\[(yyyy|name of copyright owner)\]|<Copyright Holder>", re.I)
    for line in text.splitlines():
        s = " ".join(line.split())
        if s.startswith("Copyright") and not placeholder.search(s):
            return s.rstrip(" ,")
    return None


RFN_RE = re.compile(r"reserved\s+font\s+names?", re.I)


def license_header_block(text: str) -> str:
    """The copyright header of an OFL.txt: everything before the standard license
    announcement ("This Font Software is licensed under ..."). The license body
    itself mentions "Reserved Font Name" in its definitions, so it must not count."""
    idx = text.find("This Font Software is licensed under")
    if idx > 0:
        return " ".join(text[:idx].split())
    lines = []
    for line in text.splitlines():  # fallback: first paragraph
        if not line.strip():
            if lines:
                break
            continue
        lines.append(line.strip())
    return " ".join(lines)


def declares_rfn(text: str) -> bool:
    return bool(RFN_RE.search(text))


def sfnt_format(data: bytes) -> str:
    """'woff2' | 'truetype' | 'opentype' from the file signature."""
    sig = data[:4]
    if sig == b"wOF2":
        return "woff2"
    if sig in (b"\x00\x01\x00\x00", b"true"):
        return "truetype"
    if sig == b"OTTO":
        return "opentype"
    raise ValueError(f"unknown font signature {sig!r}")


def git_blob_sha1(data: bytes) -> str:
    """The object id git gives these bytes, to prove a copy equals the upstream blob."""
    return hashlib.sha1(b"blob %d\0" % len(data) + data).hexdigest()


# --------------------------------------------------------------------------
# JSON output with a compact, stable layout
# --------------------------------------------------------------------------


def _is_scalar(v) -> bool:
    return v is None or isinstance(v, (str, int, float, bool))


def _fmt(v, level: int = 0, in_list: bool = False) -> str:
    if isinstance(v, dict):
        if not v:
            return "{}"
        if in_list and all(_is_scalar(x) or (isinstance(x, list) and all(map(_is_scalar, x))) for x in v.values()):
            return "{ " + ", ".join(f"{json.dumps(k)}: {_fmt(x)}" for k, x in v.items()) + " }"
        pad = "  " * (level + 1)
        inner = ",\n".join(f"{pad}{json.dumps(k)}: {_fmt(x, level + 1)}" for k, x in v.items())
        return "{\n" + inner + "\n" + "  " * level + "}"
    if isinstance(v, list):
        if all(_is_scalar(x) for x in v):
            return "[" + ", ".join(_fmt(x) for x in v) + "]"
        pad = "  " * (level + 1)
        inner = ",\n".join(pad + _fmt(x, level + 1, in_list=True) for x in v)
        return "[\n" + inner + "\n" + "  " * level + "]"
    return json.dumps(v, ensure_ascii=False)


def dump_json(obj) -> bytes:
    return (_fmt(obj) + "\n").encode("utf-8")


# --------------------------------------------------------------------------
# Font processing (runs in worker processes)
# --------------------------------------------------------------------------


def subset_options():
    from fontTools import subset

    opts = subset.Options()
    opts.flavor = "woff2"
    opts.layout_features = ["*"]  # keep every OpenType layout feature (kern, liga, ...)
    opts.layout_scripts = ["*"]
    opts.hinting = False  # drop hinting instructions to save space
    opts.name_IDs = ["*"]  # keep the whole name table (copyright, license, axis names)
    opts.name_languages = ["*"]
    opts.name_legacy = True
    opts.notdef_outline = True
    opts.glyph_names = False
    opts.recalc_bounds = True
    opts.recalc_timestamp = False  # determinism: never stamp the build time
    opts.canonical_order = None
    return opts


def unicode_list() -> list[int]:
    out: list[int] = []
    for lo, hi in UNICODE_RANGES:
        out.extend(range(lo, hi + 1))
    return out


def measure_coverage(cmap: dict[int, str]) -> dict[str, bool]:
    return {
        script: all(ord(ch) in cmap for ch in SCRIPT_TESTS[script]) for script in SCRIPT_ORDER
    }


def process_font(src: str, unmodified: bool = False) -> dict:
    """Subset one font to WOFF2 (or, with unmodified=True, pass its bytes through
    untouched) and gather metadata. Top-level for pickling."""
    import logging

    from fontTools import subset
    from fontTools.ttLib import TTFont

    # fontTools logs "meta NOT subset; ... dropped" for every table it cannot subset.
    logging.getLogger("fontTools").setLevel(logging.ERROR)

    name = Path(src).name
    try:
        opts = subset_options()
        if unmodified:
            data = Path(src).read_bytes()
            font = TTFont(io.BytesIO(data), lazy=True)
        else:
            font = subset.load_font(src, opts)

        # --- metadata from the original font -------------------------------
        os2 = font["OS/2"]
        head = font["head"]
        names = font["name"]
        italic = bool(os2.fsSelection & 1) or bool(head.macStyle & 2) or "Italic" in name
        axes = {}
        if "fvar" in font:
            axes = {a.axisTag: (a.minValue, a.maxValue) for a in font["fvar"].axes}
        if "wght" in axes:
            weight = [as_number(axes["wght"][0]), as_number(axes["wght"][1])]
        else:
            weight = [int(os2.usWeightClass)] * 2
        width = None
        if "wdth" in axes:
            width = [as_number(axes["wdth"][0]), as_number(axes["wdth"][1])]
        version_raw = names.getDebugName(5) or ""
        m = re.search(r"Version\s+([0-9][^;\s]*)", version_raw)
        version = m.group(1) if m else version_raw.strip()
        copyright_notice = " ".join((names.getDebugName(0) or "").split())

        # --- subset (skipped for unmodified files) -----------------------------
        if unmodified:
            check = font
        else:
            subsetter = subset.Subsetter(opts)
            subsetter.populate(unicodes=unicode_list())
            subsetter.subset(font)
            buf = io.BytesIO()
            subset.save_font(font, buf, opts)
            data = buf.getvalue()
            font.close()
            check = TTFont(io.BytesIO(data))

        # --- coverage measured from the file that will be shipped -------------
        cmap = check.getBestCmap() or {}
        coverage = measure_coverage(cmap)
        missing = {
            s: "".join(sorted(ch for ch in SCRIPT_TESTS[s] if ord(ch) not in cmap))
            for s in SCRIPT_ORDER
            if not coverage[s]
        }
        is_variable = "fvar" in check
        glyphs = len(check.getGlyphOrder())
        check.close()
    except Exception as exc:  # noqa: BLE001 - report which file failed
        raise RuntimeError(f"{name}: {type(exc).__name__}: {exc}") from exc

    return {
        "data": data,
        "format": sfnt_format(data),
        "modified": not unmodified,
        "italic": italic,
        "weight": weight,
        "width": width,
        "version": version,
        "copyright": copyright_notice,
        "coverage": coverage,
        "missing": missing,
        "variable": is_variable,
        "glyphs": glyphs,
    }


# --------------------------------------------------------------------------
# Upstream access and verification
# --------------------------------------------------------------------------


@dataclass
class FontJob:
    src: Path
    extra: bool = False  # optional static weight, dropped first when over budget


@dataclass
class FontResult:
    out_name: str  # file name without directory
    extra: bool
    info: dict
    sha256: str = ""
    size: int = 0


@dataclass
class FamilyPlan:
    cfg: dict
    license_bytes: bytes
    license_text: str
    license_file: str
    jobs: list[FontJob]
    results: list[FontResult] = field(default_factory=list)
    scripts: list[str] = field(default_factory=list)
    copyright: str = ""
    version: str = ""
    variable: bool = False
    rfn: bool = False  # declares a Reserved Font Name: fonts are shipped unmodified
    rfn_reason: str = ""


def fetch_upstream(paths: list[str], commit: str | None, url: str, workdir: Path) -> tuple[Path, str]:
    """Sparse, shallow, blob-less clone of google/fonts. Returns (checkout dir, commit sha)."""
    dest = workdir / "google-fonts"
    if commit:
        log(f"Fetching {url} at pinned commit {commit} (sparse, shallow) ...")
        dest.mkdir(parents=True)
        git(["init", "-q"], cwd=dest)
        git(["remote", "add", "origin", url], cwd=dest)
        git(["sparse-checkout", "set", *paths], cwd=dest)
        git(["fetch", "-q", "--depth", "1", "--filter=blob:none", "origin", commit], cwd=dest)
        git(["checkout", "-q", "--detach", "FETCH_HEAD"], cwd=dest)
    else:
        log(f"Cloning {url} (sparse, shallow) ...")
        git(["clone", "-q", "--depth", "1", "--filter=blob:none", "--sparse", url, str(dest)])
        git(["sparse-checkout", "set", *paths], cwd=dest)
    sha = git(["rev-parse", "HEAD"], cwd=dest)
    log(f"Upstream commit: {sha}")
    return dest, sha


def verify_license(fam: dict, fdir: Path) -> tuple[bytes, str]:
    spec = LICENSES.get(fam["license"])
    if spec is None:
        raise FamilyError(f"unknown license id {fam['license']!r} in families.json")
    top = fam["path"].split("/")[0]
    if top != spec["dir"]:
        raise FamilyError(
            f"path {fam['path']!r} is not under {spec['dir']}/ but license {fam['license']} was declared"
        )
    # Reject families that ship a license file of the other kind or none at all.
    lic_path = fdir / spec["file"]
    if not lic_path.is_file():
        raise FamilyError(f"{spec['file']} not found in upstream {fam['path']}")
    raw = lic_path.read_bytes()
    text = normalize_text(raw).decode("utf-8", errors="replace")
    flat = " ".join(text.split()).lower()
    for marker in spec["markers"]:
        if marker.lower() not in flat:
            raise FamilyError(f"{spec['file']} does not contain {marker!r}: not {fam['license']}")
    # Cross-check with the upstream METADATA.pb license field.
    meta = fdir / "METADATA.pb"
    if meta.is_file():
        m = re.search(r'^license:\s*"([^"]+)"', meta.read_text(encoding="utf-8", errors="replace"), re.M)
        if m and m.group(1) != spec["metadata"]:
            raise FamilyError(
                f"METADATA.pb says license {m.group(1)!r}, expected {spec['metadata']!r} for {fam['license']}"
            )
    else:
        log(f"  warning: {fam['path']}/METADATA.pb missing, license only checked via {spec['file']}")
    return normalize_text(raw), text


def select_fonts(fam: dict, fdir: Path, extra_styles: list[str]) -> list[FontJob]:
    ttfs = sorted(p for p in fdir.iterdir() if p.is_file() and p.suffix.lower() in (".ttf", ".otf"))
    variable = [p for p in ttfs if "[" in p.name]
    if variable:
        return [FontJob(p) for p in variable]
    wanted = {s: False for s in CORE_STATIC_STYLES}
    for style in extra_styles:
        wanted[style] = True
        wanted[style + "Italic"] = True
    jobs = []
    for p in ttfs:
        if "-" not in p.stem:
            continue
        style = p.stem.rsplit("-", 1)[1]
        if style in wanted:
            jobs.append(FontJob(p, extra=wanted[style]))
    if not jobs:
        raise FamilyError(f"no usable .ttf/.otf files found in {fam['path']}")
    return jobs


def detect_rfn(fam: dict, license_text: str, jobs: list[FontJob]) -> str:
    """Return why this family must be shipped unmodified ('' if it declares no
    Reserved Font Name). Two sources count: the copyright header of the license
    file, and the copyright/license notices (name IDs 0 and 13) inside the fonts."""
    from fontTools.ttLib import TTFont

    if fam["license"] == "OFL-1.1" and declares_rfn(license_header_block(license_text)):
        return "license file"
    for job in jobs:
        font = TTFont(job.src, lazy=True)
        try:
            names = font["name"]
            notices = [names.getDebugName(0) or "", names.getDebugName(13) or ""]
        finally:
            font.close()
        if any(declares_rfn(n) for n in notices):
            return f"font name table ({job.src.name})"
    return ""


def upstream_blob_ids(src_root: Path, rel_dir: str) -> dict[str, str]:
    """{file name: git blob id} of the upstream directory at the checked out commit."""
    out = git(["ls-tree", "-r", "-z", "HEAD", "--", rel_dir + "/"], cwd=src_root)
    ids = {}
    for rec in out.split("\0"):
        if not rec:
            continue
        meta, path = rec.split("\t", 1)
        ids[path.rsplit("/", 1)[-1]] = meta.split()[2]
    return ids


def plan_family(fam: dict, src_root: Path, default_extras: list[str]) -> FamilyPlan:
    fdir = src_root / fam["path"]
    if not fdir.is_dir():
        raise FamilyError(f"upstream directory {fam['path']} not found")
    lic_bytes, lic_text = verify_license(fam, fdir)
    jobs = select_fonts(fam, fdir, fam.get("extraStaticStyles", default_extras))
    reason = detect_rfn(fam, lic_text, jobs)
    if reason:
        # Shipped unmodified: prove the working copy is byte-identical to the upstream
        # blobs (guards against autocrlf or any other tampering on the way).
        blobs = upstream_blob_ids(src_root, fam["path"])
        for job in jobs:
            expected = blobs.get(job.src.name)
            actual = git_blob_sha1(job.src.read_bytes())
            if expected != actual:
                raise FatalError(
                    f"{fam['id']}/{job.src.name}: checkout differs from the upstream git blob "
                    f"({actual} != {expected}); refusing to ship it as an unmodified font"
                )
    return FamilyPlan(
        cfg=fam,
        license_bytes=lic_bytes,
        license_text=lic_text,
        license_file=LICENSES[fam["license"]]["file"],
        jobs=jobs,
        rfn=bool(reason),
        rfn_reason=reason,
    )


# --------------------------------------------------------------------------
# Manifest and LICENSES.md
# --------------------------------------------------------------------------


def family_entries(plans: list[FamilyPlan], commit: str, url: str) -> list[dict]:
    entries = []
    for plan in plans:
        fam = plan.cfg
        files = []
        ordered = sorted(
            plan.results,
            key=lambda r: (r.info["italic"], r.info["weight"][0], r.info["weight"][1], r.out_name),
        )
        for r in ordered:
            entry = {
                "file": f"{fam['id']}/{r.out_name}",
                "format": r.info["format"],
                "modified": r.info["modified"],
                "style": "italic" if r.info["italic"] else "normal",
                "weight": r.info["weight"],
            }
            if r.info["width"]:
                entry["width"] = r.info["width"]
            entry["sha256"] = r.sha256
            entry["size"] = r.size
            files.append(entry)
        entries.append(
            {
                "id": fam["id"],
                "family": fam["family"],
                "category": fam["category"],
                "license": fam["license"],
                "licenseFile": f"{fam['id']}/{plan.license_file}",
                "source": f"{url}/tree/{commit}/{fam['path']}",
                "upstreamCommit": commit,
                "copyright": plan.copyright,
                "scripts": plan.scripts,
                "files": files,
            }
        )
    entries.sort(key=lambda e: (CATEGORY_ORDER.index(e["category"]), e["family"].lower(), e["id"]))
    return entries


def render_licenses_md(plans: list[FamilyPlan], commit: str, url: str) -> bytes:
    def cell(text: str) -> str:
        return text.replace("|", "\\|")

    rows = []
    rfn = []
    modified_apache = []
    for plan in sorted(plans, key=lambda p: (p.cfg["family"].lower(), p.cfg["id"])):
        fam = plan.cfg
        lic = LICENSES[fam["license"]]
        short = f"google/fonts@{commit[:10]}/{fam['path']}"
        modified = any(r.info["modified"] for r in plan.results)
        rows.append(
            "| {family} | {version} | {license} | {modified} | {source} | {copyright} |".format(
                family=cell(fam["family"]),
                version=cell(plan.version or "n/a"),
                license=f"[{lic['label']}]({fam['id']}/{plan.license_file})",
                modified="yes" if modified else "no",
                source=f"[{short}]({url}/tree/{commit}/{fam['path']})",
                copyright=cell(plan.copyright),
            )
        )
        if plan.rfn:
            rfn.append(fam["family"])
        if modified and fam["license"] == "Apache-2.0":
            modified_apache.append(fam["family"])

    lines = [
        "# Bundled fonts: third-party licenses",
        "",
        "<!-- Generated by tools/fonts/build_fonts.py. Do not edit by hand. -->",
        "",
        "OpenSignage ships these font families with every server and player so that no",
        "font is ever loaded from a CDN. Every family is licensed under the SIL Open Font",
        "License 1.1 or the Apache License 2.0 (verified against the license file and the",
        "`METADATA.pb` of the upstream repository when the bundle is built). The full",
        "license text of each family is shipped next to its font files.",
        "",
        "The Modified column says whether the shipped files differ from the upstream",
        "fonts:",
        "",
        "* **no**: byte-identical copies of the upstream `.ttf` files (only the file name",
        "  was changed where it contained `[`, `]` or `,`).",
        "* **yes**: the fonts were subset to Basic Latin, Latin-1, Latin Extended, Greek,",
        "  Cyrillic, punctuation, currency and a few symbols, hinting instructions were",
        "  removed, and the result was converted to WOFF2. The glyph designs are unchanged and",
        "  the original copyright and license notices are kept in each font's name table.",
        "",
        "| Family | Version | License | Modified | Upstream source | Copyright |",
        "| --- | --- | --- | --- | --- | --- |",
        *rows,
        "",
    ]
    if rfn:
        lines += [
            "## Reserved Font Names",
            "",
            "The license file or the name table of the following families declares a Reserved",
            "Font Name. Under OFL 1.1 condition 3 no Modified Version may use that name",
            "without written permission of the copyright holder, and subsetting or converting",
            "a font produces a Modified Version. These families are therefore shipped",
            "unmodified (Modified: no) under their original names.",
            "",
            ", ".join(rfn) + ".",
            "",
        ]
    if modified_apache:
        lines += [
            "## Changes to Apache 2.0 licensed fonts",
            "",
            ", ".join(modified_apache) + ": the shipped files are subset and converted to WOFF2",
            "from the upstream font as described above (notice of changes under Apache License",
            "2.0, section 4(b)).",
            "",
        ]
    return "\n".join(lines).encode("utf-8")


# --------------------------------------------------------------------------
# Build
# --------------------------------------------------------------------------


def load_config(path: Path) -> dict:
    cfg = json.loads(path.read_text(encoding="utf-8"))
    ids = set()
    for fam in cfg["families"]:
        for key in ("id", "family", "category", "path", "license"):
            if key not in fam:
                raise FatalError(f"families.json: entry {fam!r} lacks {key!r}")
        if fam["id"] in ids:
            raise FatalError(f"families.json: duplicate id {fam['id']!r}")
        ids.add(fam["id"])
        if fam["category"] not in CATEGORY_ORDER:
            raise FatalError(f"families.json: {fam['id']}: category must be one of {CATEGORY_ORDER}")
        if fam["license"] not in LICENSES:
            raise FatalError(f"families.json: {fam['id']}: license must be one of {list(LICENSES)}")
        for s in fam.get("mustCover", []):
            if s not in SCRIPT_ORDER:
                raise FatalError(f"families.json: {fam['id']}: unknown script {s!r}")
    fb = [f for f in cfg["families"] if f["id"] == cfg["fallback"]]
    if not fb:
        raise FatalError(f"families.json: fallback {cfg['fallback']!r} is not a listed family id")
    return cfg


def dir_size(path: Path) -> int:
    return sum(p.stat().st_size for p in path.rglob("*") if p.is_file())


def fmt_size(n: int) -> str:
    return f"{n / 1_000_000:.2f} MB" if n >= 1_000_000 else f"{n / 1000:.1f} KB"


def assemble(plans: list[FamilyPlan], cfg: dict, commit: str, url: str, budget: int) -> tuple[dict[str, bytes], list[FamilyPlan], list[str]]:
    """Return ({relative path -> bytes}, plans, dropped extras) honouring the size budget."""
    dropped: list[str] = []

    def build_files() -> dict[str, bytes]:
        files: dict[str, bytes] = {}
        for plan in plans:
            fid = plan.cfg["id"]
            for r in plan.results:
                files[f"{fid}/{r.out_name}"] = r.info["data"]
            files[f"{fid}/{plan.license_file}"] = plan.license_bytes
        entries = family_entries(plans, commit, url)
        manifest = {"version": MANIFEST_VERSION, "fallback": cfg_fallback_name(cfg), "families": entries}
        files["manifest.json"] = dump_json(manifest)
        files["LICENSES.md"] = render_licenses_md(plans, commit, url)
        return files

    while True:
        files = build_files()
        total = sum(len(b) for b in files.values())
        if total <= budget:
            return files, plans, dropped
        candidates = [
            (len(r.info["data"]), plan, r)
            for plan in plans
            for r in plan.results
            if r.extra
        ]
        if not candidates:
            raise FatalError(
                f"fonts/ would be {fmt_size(total)} which exceeds the budget of {fmt_size(budget)} and "
                "no optional static weights are left to drop. Remove families from families.json."
            )
        # Unmodified (Reserved Font Name) families are the big ones: drop their optional
        # weights first, largest file first; then the optional weights of the others.
        candidates.sort(key=lambda c: (not c[1].rfn, -c[0], c[1].cfg["id"], c[2].out_name))
        _, plan, r = candidates[0]
        plan.results.remove(r)
        dropped.append(f"{plan.cfg['id']}/{r.out_name}")


def cfg_fallback_name(cfg: dict) -> str:
    return next(f["family"] for f in cfg["families"] if f["id"] == cfg["fallback"])


def swap_into_place(staging: Path, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    new_names = {p.name for p in staging.iterdir()}
    # Remove stale family folders; leave unrelated top-level files alone.
    for p in list(out.iterdir()):
        if p.is_dir():
            rmtree(p)
        elif p.name in ("manifest.json", "LICENSES.md"):
            p.unlink()
    for name in sorted(new_names):
        shutil.move(str(staging / name), str(out / name))


def build(args: argparse.Namespace) -> int:
    cfg = load_config(Path(args.config))
    url = cfg.get("upstream", UPSTREAM_DEFAULT).rstrip("/")
    budget = int(cfg.get("budgetBytes", 60_000_000))
    families = cfg["families"]
    if args.only:
        wanted = set(args.only.split(","))
        unknown = wanted - {f["id"] for f in families}
        if unknown:
            raise FatalError(f"--only: unknown family ids {sorted(unknown)}")
        families = [f for f in families if f["id"] in wanted]
        if Path(args.out).resolve() == DEFAULT_OUT.resolve():
            raise FatalError("--only produces a partial bundle; pass --out <other dir>")

    tmp_root: Path | None = None
    try:
        if args.source_dir:
            src_root = Path(args.source_dir).resolve()
            commit = git(["rev-parse", "HEAD"], cwd=src_root)
            log(f"Using existing checkout {src_root} at {commit}")
        else:
            tmp_root = Path(tempfile.mkdtemp(prefix="opensignage-fonts-"))
            paths = sorted({f["path"] for f in families})
            src_root, commit = fetch_upstream(paths, args.commit, url, tmp_root)

        # ---- verify licenses, choose files -----------------------------------
        plans: list[FamilyPlan] = []
        excluded: list[tuple[dict, str]] = []
        for fam in families:
            try:
                plans.append(plan_family(fam, src_root, cfg.get("extraStaticStyles", [])))
            except FamilyError as exc:
                banner = f"!!! EXCLUDED {fam['family']} ({fam['path']}): {exc}"
                log("!" * len(banner))
                log(banner)
                log("!" * len(banner))
                if fam.get("required"):
                    raise FatalError(f"required family {fam['family']} failed verification: {exc}") from exc
                excluded.append((fam, str(exc)))

        # ---- subset (parallel, order preserved) --------------------------------
        rfn_plans = [p for p in plans if p.rfn]
        if rfn_plans:
            log("Reserved Font Name declared, shipped UNMODIFIED (byte-identical upstream files):")
            for p in rfn_plans:
                log(f"  {p.cfg['family']}  [{p.rfn_reason}]")
        all_jobs = [(plan, job) for plan in plans for job in plan.jobs]
        jobs_n = args.jobs or min(os.cpu_count() or 2, 8)
        log(f"Processing {len(all_jobs)} font files for {len(plans)} families with {jobs_n} workers ...")
        with concurrent.futures.ProcessPoolExecutor(max_workers=jobs_n) as pool:
            infos = list(
                pool.map(
                    process_font,
                    [str(job.src) for _, job in all_jobs],
                    [plan.rfn for plan, _ in all_jobs],
                    chunksize=1,
                )
            )

        used_names: dict[str, set[str]] = {}
        for (plan, job), info in zip(all_jobs, infos):
            # Unmodified files keep their upstream name and extension; only [ ] , are replaced.
            suffix = job.src.suffix if plan.rfn else ".woff2"
            out_name = sanitize_stem(job.src.stem) + suffix
            seen = used_names.setdefault(plan.cfg["id"], set())
            if out_name in seen:
                raise FatalError(f"{plan.cfg['id']}: duplicate output file name {out_name}")
            seen.add(out_name)
            r = FontResult(out_name=out_name, extra=job.extra, info=info)
            r.sha256 = sha256_hex(info["data"])
            r.size = len(info["data"])
            plan.results.append(r)

        # ---- per family: coverage, copyright, version -------------------------
        for plan in plans:
            fam = plan.cfg
            scripts = [s for s in SCRIPT_ORDER if all(r.info["coverage"][s] for r in plan.results)]
            plan.scripts = scripts
            normal = [r for r in plan.results if not r.info["italic"]] or plan.results
            first = sorted(normal, key=lambda r: (r.info["weight"][0], r.out_name))[0]
            plan.version = first.info["version"]
            plan.variable = all(r.info["variable"] for r in plan.results)
            plan.copyright = first_copyright_line(plan.license_text) or first.info["copyright"]
            if not plan.copyright:
                raise FatalError(f"{fam['id']}: no copyright line found in license file or name table")
            missing = [s for s in fam.get("mustCover", []) if s not in scripts]
            if missing:
                detail = {
                    s: r.info["missing"].get(s, "")
                    for r in plan.results
                    for s in missing
                    if s in r.info["missing"]
                }
                raise FatalError(
                    f"{fam['family']} must cover {fam.get('mustCover')} but lacks {missing} (missing chars: {detail})"
                )

        # ---- budget, then write -------------------------------------------------
        files, plans, dropped = assemble(plans, cfg, commit, url, budget)
        if dropped:
            log(f"Over budget: dropped {len(dropped)} optional static weights (largest first)")
            for d in dropped:
                log(f"  dropped {d}")

        out = Path(args.out).resolve()
        staging = out.parent / (out.name + ".build-tmp")
        if staging.exists():
            rmtree(staging)
        try:
            for rel, data in sorted(files.items()):
                target = staging / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(data)
            total = dir_size(staging)
            swap_into_place(staging, out)
        finally:
            if staging.exists():
                rmtree(staging)

        report(plans, excluded, total, budget, commit, out)
        if excluded and not args.allow_exclusions:
            log("")
            log(f"Exit status 3: {len(excluded)} famil(y/ies) were excluded. Remove them from families.json "
                "(or fix the cause) so the build is clean.")
            return 3
        return 0
    finally:
        if tmp_root and tmp_root.exists():
            rmtree(tmp_root)


def report(plans: list[FamilyPlan], excluded, total: int, budget: int, commit: str, out: Path) -> None:
    log("")
    log(f"{'family':<20} {'cat':<10} {'files':>5} {'size':>10}  {'mod':<4} scripts")
    log("-" * 86)
    rows = sorted(plans, key=lambda p: -sum(r.size for r in p.results))
    for plan in rows:
        size = sum(r.size for r in plan.results)
        kind = "var" if plan.variable else "static"
        log(
            f"{plan.cfg['id']:<20} {plan.cfg['category']:<10} {len(plan.results):>5} {fmt_size(size):>10}  "
            f"{'no' if plan.rfn else 'yes':<4} {','.join(plan.scripts) or '-'} ({kind})"
        )
    log("-" * 86)
    n_files = sum(len(p.results) for p in plans)
    unmod = sum(r.size for p in plans if p.rfn for r in p.results)
    log(f"{len(plans)} families, {n_files} font files, fonts/ total {fmt_size(total)} "
        f"({total / budget * 100:.1f}% of the {fmt_size(budget)} budget)")
    log(f"{len([p for p in plans if p.rfn])} Reserved Font Name families shipped unmodified "
        f"({fmt_size(unmod)}), {len([p for p in plans if not p.rfn])} subset to WOFF2 "
        f"({fmt_size(sum(r.size for p in plans if not p.rfn for r in p.results))})")
    lacking = [(p.cfg["family"], [s for s in ("greek", "cyrillic") if s not in p.scripts]) for p in plans]
    lacking = [(n, m) for n, m in lacking if m]
    if lacking:
        log("Families without full Greek and/or Cyrillic coverage:")
        for n, m in lacking:
            log(f"  {n}: no {', '.join(m)}")
    for fam, why in excluded:
        log(f"EXCLUDED {fam['family']}: {why}")
    log(f"Upstream commit {commit}; wrote {out}")


# --------------------------------------------------------------------------
# Verify an existing bundle
# --------------------------------------------------------------------------


def verify(args: argparse.Namespace) -> int:
    from fontTools.ttLib import TTFont

    out = Path(args.out).resolve()
    cfg = load_config(Path(args.config))
    budget = int(cfg.get("budgetBytes", 60_000_000))
    problems: list[str] = []

    def bad(msg: str) -> None:
        problems.append(msg)

    manifest_path = out / "manifest.json"
    if not manifest_path.is_file():
        log(f"FAIL: {manifest_path} not found")
        return 1
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("version") != MANIFEST_VERSION:
        bad(f"manifest version {manifest.get('version')!r} != {MANIFEST_VERSION}")

    families = manifest.get("families", [])
    expected_order = sorted(families, key=lambda e: (CATEGORY_ORDER.index(e["category"]), e["family"].lower(), e["id"]))
    if [f["id"] for f in families] != [f["id"] for f in expected_order]:
        bad("families are not sorted by category then name")

    declared: set[str] = {"manifest.json", "LICENSES.md"}
    n_files = 0
    for fam in families:
        fid = fam["id"]
        if fam["category"] not in CATEGORY_ORDER:
            bad(f"{fid}: bad category {fam['category']!r}")
        lic = LICENSES.get(fam["license"])
        rfn_declared = False  # Reserved Font Name in the license header or a font name table
        if not lic:
            bad(f"{fid}: unsupported license {fam['license']!r}")
        else:
            lp = out / fam["licenseFile"]
            declared.add(fam["licenseFile"])
            if not lp.is_file():
                bad(f"{fid}: license file {fam['licenseFile']} missing")
            else:
                lic_text = lp.read_text(encoding="utf-8", errors="replace")
                flat = " ".join(lic_text.split()).lower()
                for marker in lic["markers"]:
                    if marker.lower() not in flat:
                        bad(f"{fid}: {fam['licenseFile']} lacks {marker!r}")
                if fam["license"] == "OFL-1.1" and declares_rfn(license_header_block(lic_text)):
                    rfn_declared = True
        if not fam.get("copyright", "").startswith("Copyright"):
            bad(f"{fid}: copyright {fam.get('copyright')!r} does not start with 'Copyright'")
        covered = set(SCRIPT_ORDER)
        any_modified = False
        for f in fam["files"]:
            n_files += 1
            rel = f["file"]
            declared.add(rel)
            if re.search(r"[\[\],]", rel):
                bad(f"{rel}: file name contains [ ] or ,")
            p = out / rel
            if not p.is_file():
                bad(f"{rel}: missing")
                continue
            data = p.read_bytes()
            if sha256_hex(data) != f["sha256"]:
                bad(f"{rel}: sha256 mismatch")
            if len(data) != f["size"]:
                bad(f"{rel}: size {len(data)} != manifest {f['size']}")
            try:
                actual_format = sfnt_format(data)
            except ValueError as exc:
                bad(f"{rel}: {exc}")
                actual_format = None
            if f.get("format") != actual_format:
                bad(f"{rel}: format {f.get('format')!r} != actual {actual_format!r}")
            expected_ext = {"woff2": ".woff2", "truetype": ".ttf", "opentype": ".otf"}.get(actual_format)
            if expected_ext and not rel.endswith(expected_ext):
                bad(f"{rel}: extension does not match format {actual_format}")
            if not isinstance(f.get("modified"), bool) or f["modified"] != (actual_format == "woff2"):
                bad(f"{rel}: 'modified' {f.get('modified')!r} is inconsistent with format {actual_format}")
            any_modified = any_modified or bool(f.get("modified"))
            try:
                font = TTFont(io.BytesIO(data))
                names = font["name"]
                if any(declares_rfn(names.getDebugName(i) or "") for i in (0, 13)):
                    rfn_declared = True
                cmap = font.getBestCmap() or {}
                cov = measure_coverage(cmap)
                covered &= {s for s in SCRIPT_ORDER if cov[s]}
                os2 = font["OS/2"]
                if "fvar" in font:
                    ax = {a.axisTag: (a.minValue, a.maxValue) for a in font["fvar"].axes}
                    wmin, wmax = ax.get("wght", (os2.usWeightClass,) * 2)
                    exp_w = [as_number(wmin), as_number(wmax)]
                else:
                    exp_w = [int(os2.usWeightClass)] * 2
                if exp_w != f["weight"]:
                    bad(f"{rel}: weight {f['weight']} != font {exp_w}")
                font.close()
            except Exception as exc:  # noqa: BLE001
                bad(f"{rel}: cannot open with fontTools: {exc}")
        if sorted(covered, key=SCRIPT_ORDER.index) != fam["scripts"]:
            bad(f"{fid}: scripts {fam['scripts']} != measured {sorted(covered, key=SCRIPT_ORDER.index)}")
        if rfn_declared and any_modified:
            bad(f"{fid}: declares a Reserved Font Name but ships modified (subset/converted) files")

    fb = [f for f in families if f["family"] == manifest.get("fallback")]
    if not fb:
        bad(f"fallback {manifest.get('fallback')!r} is not in the manifest")
    elif set(fb[0]["scripts"]) != set(SCRIPT_ORDER):
        bad(f"fallback {fb[0]['family']} must cover all of {SCRIPT_ORDER}, has {fb[0]['scripts']}")
    for must in ("Montserrat",):
        if not any(f["family"] == must for f in families):
            bad(f"required family {must} missing")

    actual = {p.relative_to(out).as_posix() for p in out.rglob("*") if p.is_file()}
    for stray in sorted(actual - declared):
        bad(f"{stray}: file on disk is not declared in the manifest")
    total = dir_size(out)
    if total > budget:
        bad(f"fonts/ is {fmt_size(total)}, over the budget of {fmt_size(budget)}")

    log(f"{len(families)} families, {n_files} font files, total {fmt_size(total)}")
    if problems:
        log(f"FAIL: {len(problems)} problem(s)")
        for p in problems:
            log(f"  - {p}")
        return 1
    log("OK: every manifest file exists, sha256/size/format match, every font opens with fontTools, "
        "scripts and licenses check out, no Reserved Font Name family is modified")
    return 0


# --------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:  # noqa: BLE001
            pass
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--config", default=str(CONFIG_PATH), help="families.json (default: next to this script)")
    ap.add_argument("--out", default=str(DEFAULT_OUT), help="output directory (default: <repo>/fonts)")
    ap.add_argument("--commit", help="pin the upstream google/fonts commit (default: latest main)")
    ap.add_argument("--source-dir", help="use an existing google/fonts checkout instead of cloning")
    ap.add_argument("--only", help="comma separated family ids (partial build, needs --out)")
    ap.add_argument("--jobs", type=int, default=0, help="parallel workers (default: min(cpus, 8))")
    ap.add_argument("--allow-exclusions", action="store_true", help="exit 0 even if families were excluded")
    ap.add_argument("--verify", action="store_true", help="verify an existing bundle against its manifest and exit")
    args = ap.parse_args(argv)
    try:
        return verify(args) if args.verify else build(args)
    except FatalError as exc:
        log(f"FATAL: {exc}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
