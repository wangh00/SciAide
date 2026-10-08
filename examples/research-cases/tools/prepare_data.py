"""Derive replay inputs from the unchanged UCI archive; never generate observations."""
import csv
import hashlib
import io
import json
from pathlib import Path
import zipfile

ROOT = Path(__file__).resolve().parents[1]
FEATURES = [
    "fixed_acidity", "volatile_acidity", "citric_acid", "residual_sugar",
    "chlorides", "free_sulfur_dioxide", "total_sulfur_dioxide", "density",
    "pH", "sulphates", "alcohol",
]


def main():
    with zipfile.ZipFile(ROOT / "sources/wine-quality-uci.zip") as archive:
        for name in archive.namelist():
            # Preserve original bytes separately, including the semicolon delimiter.
            (ROOT / "sources" / name).write_bytes(archive.read(name))
        red_text = archive.read("winequality-red.csv").decode("utf-8")
        white_text = archive.read("winequality-white.csv").decode("utf-8")
    red = list(csv.DictReader(io.StringIO(red_text), delimiter=";"))
    white = list(csv.DictReader(io.StringIO(white_text), delimiter=";"))
    assert len(red) == 1599 and len(white) == 4898
    derived = []
    for index, row in enumerate(red, 1):
        values = {key.replace(" ", "_"): value for key, value in row.items()}
        # Target-free grouping: identical feature profiles always share the split.
        key = "|".join(values[feature] for feature in FEATURES)
        bucket = int(hashlib.sha256(key.encode("utf-8")).hexdigest()[:8], 16) % 5
        derived.append({"sample_id": f"red_{index:04d}",
                        "split": "test" if bucket == 0 else "train", **values})
    fields = ["sample_id", "split", *FEATURES, "quality"]
    for relative, rows in [
        ("02-data/materials/initial/wine-red-initial-1000.csv", derived[:1000]),
        ("02-data/materials/followup/wine-red-full-1599.csv", derived),
    ]:
        with (ROOT / relative).open("w", encoding="utf-8", newline="") as stream:
            writer = csv.DictWriter(stream, fieldnames=fields, lineterminator="\n")
            writer.writeheader()
            writer.writerows(rows)
    print(json.dumps({"original_red": len(red), "original_white": len(white),
                      "initial_rows": 1000, "full_rows": len(derived)}, indent=2))


if __name__ == "__main__":
    main()
