"""Fetch reading copies from original sources; verify identity before saving.

Public bundles omit paper full texts. This script never redistributes them,
overwrites existing copies, or silently accepts a different paper version.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]


def check(data, paper):
    if not data.startswith(b"%PDF-"):
        raise ValueError(f"Not a PDF: {paper['title']}")
    if hashlib.sha256(data).hexdigest() != paper["sha256"]:
        raise ValueError(f"Source version/hash changed: {paper['title']}; inspect source manually")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--verify-only", action="store_true", help="Check existing originals without network or writes")
    args = parser.parse_args()
    manifest = json.loads((ROOT / "sources/source-manifest.json").read_text(encoding="utf-8"))
    for paper in manifest["papers"]:
        target = (ROOT / paper["path"]).resolve()
        if not target.is_relative_to(ROOT / "01-literature/materials"):
            raise ValueError("Paper path is outside literature materials")
        if target.exists():
            check(target.read_bytes(), paper)
            print(f"Verified existing: {target.name}")
            continue
        if args.verify_only:
            raise FileNotFoundError(target)
        url = paper["url"]
        if url.startswith("https://arxiv.org/pdf/"):
            version = paper.get("arxiv")
            if not version:
                match = re.search(r"arXiv:(\d+\.\d+v\d+)", paper["local_version"])
                version = match.group(1) if match else None
            if not version:
                raise ValueError("Missing pinned arXiv version")
            url = "https://arxiv.org/pdf/" + version
        with urlopen(Request(url, headers={"User-Agent": "SciAide-case-preparation/1.0"}), timeout=60) as response:
            data = response.read(20 * 1024 * 1024 + 1)
        if len(data) > 20 * 1024 * 1024:
            raise ValueError("Paper exceeds download limit")
        check(data, paper)
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("xb") as output:
            output.write(data)
        print(f"Downloaded and verified: {target.name}")


if __name__ == "__main__":
    main()
