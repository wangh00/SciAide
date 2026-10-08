"""Build a verified local archive, or a public bundle without paper full texts."""
import argparse
import hashlib
from pathlib import Path
import zipfile

ROOT = Path(__file__).resolve().parents[1]
HASHES = ROOT / "SHA256SUMS.txt"
ZIP = ROOT.parent / "research-cases.zip"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--public", action="store_true", help="Exclude paper originals and derived full texts")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    output = args.output or (ROOT.parents[1] / ".tmp/release-case-pack/research-cases.zip" if args.public else ZIP)
    paths = sorted(path for path in ROOT.rglob("*") if path.is_file()
                   and "__pycache__" not in path.parts and path.suffix != ".pyc" and path != HASHES
                   and not (args.public and path.suffix in {".pdf", ".txt"}
                            and path.is_relative_to(ROOT / "01-literature/materials")))
    entries = [(digest(path.read_bytes()), path.relative_to(ROOT).as_posix()) for path in paths]
    hashes = "".join(f"{sha}  {name}\n" for sha, name in entries)
    if not args.public:
        HASHES.write_text(hashes, encoding="utf-8")
    output.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in paths:
            archive.write(path, "research-cases/" + path.relative_to(ROOT).as_posix())
        archive.writestr("research-cases/SHA256SUMS.txt", hashes)
    with zipfile.ZipFile(output) as archive:
        assert archive.testzip() is None
        assert len(archive.namelist()) == len(paths) + 1
        for sha, name in entries:
            assert digest(archive.read("research-cases/" + name)) == sha
        if args.public:
            assert not any(name.endswith((".pdf", ".pages.txt")) for name in archive.namelist())
    print(f"Verified {len(entries)} files plus checksum list; archive {output.stat().st_size} bytes")
    print(f"ZIP SHA256: {digest(output.read_bytes())}")


if __name__ == "__main__":
    main()
