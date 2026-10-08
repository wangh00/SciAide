"""Record source identity and derive page-marked text without editing original PDFs."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path

from pypdf import PdfReader
import pypdf

ROOT = Path(__file__).resolve().parents[1]
PAPERS = [
    {"path": "01-literature/materials/initial/breiman-2001-random-forests.pdf",
     "title": "Random Forests", "authors": "Leo Breiman", "publication_year": 2001,
     "local_version": "Author-hosted January 2001 manuscript; 33 PDF pages including a blank page",
     "url": "https://www.stat.berkeley.edu/~breiman/randomforest2001.pdf",
     "doi": "10.1023/A:1010933404324"},
    {"path": "01-literature/materials/initial/chen-guestrin-2016-xgboost.pdf",
     "title": "XGBoost: A Scalable Tree Boosting System",
     "authors": "Tianqi Chen; Carlos Guestrin", "publication_year": 2016,
     "local_version": "arXiv:1603.02754v3, 2016-06-10; 13 PDF pages",
     "url": "https://arxiv.org/pdf/1603.02754", "doi": "10.1145/2939672.2939785"},
    {"path": "01-literature/materials/followup/prokhorenkova-2018-catboost.pdf",
     "title": "CatBoost: unbiased boosting with categorical features",
     "authors": "Liudmila Prokhorenkova; Gleb Gusev; Aleksandr Vorobev; Anna Veronika Dorogush; Andrey Gulin",
     "publication_year": 2018,
     "local_version": "arXiv:1706.09516v5, 2019-01-20; 23 PDF pages; distinct from arXiv:1810.11363",
     "url": "https://arxiv.org/pdf/1706.09516", "arxiv": "1706.09516v5"},
]


def main():
    generated_at = datetime.now(timezone.utc).isoformat()
    for paper in PAPERS:
        pdf = ROOT / paper["path"]
        reader = PdfReader(pdf)
        text = [f"TITLE: {paper['title']}", f"ORIGINAL PDF: {pdf.name}",
                f"SOURCE: {paper['url']}", f"LOCAL VERSION: {paper['local_version']}",
                f"PDF SHA256: {hashlib.sha256(pdf.read_bytes()).hexdigest()}",
                f"EXTRACTION: pypdf {pypdf.__version__}; generated {generated_at}",
                "DERIVED MATERIAL: not OCR, not an independent study, not manually corrected.",
                "Page labels are 1-based PDF page indexes. Layout, equations and tables may be distorted.",
                "Use the unchanged original PDF for exact formulas/numbers; do not guess missing glyphs."]
        for index, page in enumerate(reader.pages, 1):
            text.extend([f"\n===== PDF PAGE {index} =====\n", page.extract_text() or "[No extractable text]"])
        txt = pdf.with_suffix(".pages.txt")
        txt.write_text("\n".join(text) + "\n", encoding="utf-8")
        paper.update({"sha256": hashlib.sha256(pdf.read_bytes()).hexdigest(),
                      "bytes": pdf.stat().st_size, "pages": len(reader.pages),
                      "derived_text_path": txt.relative_to(ROOT).as_posix()})
    manifest = {"prepared_at_utc": generated_at,
                "download_date_local": "2026-09-30 Asia/Singapore",
                "papers": PAPERS,
                "dataset": {"title": "Wine Quality", "authors": "Cortez, Cerdeira, Almeida, Matos, Reis (2009)",
                            "url": "https://archive.ics.uci.edu/dataset/186/wine+quality",
                            "download_url": "https://archive.ics.uci.edu/static/public/186/wine+quality.zip",
                            "doi": "10.24432/C56S3T", "license": "CC BY 4.0 (verified on UCI page)",
                            "license_url": "https://creativecommons.org/licenses/by/4.0/legalcode",
                            "intro_paper_doi": "10.1016/j.dss.2009.05.016",
                            "archive_path": "sources/wine-quality-uci.zip",
                            "archive_sha256": hashlib.sha256((ROOT / "sources/wine-quality-uci.zip").read_bytes()).hexdigest(),
                            "red_rows": 1599, "white_rows": 4898,
                            "transform": "Red only; semicolon to comma; headers spaces to underscores; add derived sample_id and fixed feature-hash split; initial is first 1000 original rows; full is all 1599. No measured value altered."}}
    (ROOT / "sources/source-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(manifest, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
