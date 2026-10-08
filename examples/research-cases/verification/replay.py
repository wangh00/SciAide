"""Independent local calculations, NOT evidence of a SciAide/model E2E pass.

Run with Python + numpy. Reads only case-pack files and writes reference-results.json.
Do not upload this script/results to the task under test.
"""
from collections import Counter
import csv
import hashlib
import importlib.util
import json
from pathlib import Path
import platform
import sys

import numpy as np

ROOT = Path(__file__).resolve().parents[1]
FEATURES = ["fixed_acidity", "volatile_acidity", "citric_acid", "residual_sugar",
            "chlorides", "free_sulfur_dioxide", "total_sulfur_dioxide", "density",
            "pH", "sulphates", "alcohol"]
SOURCE = ROOT / "03-method-reference/materials/ridge_reference.py"
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("case_ridge_reference", SOURCE)
reference = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reference)  # Local QA only; not how SciAide adopts uploaded code.


def load(path, delimiter=","):
    with path.open(encoding="utf-8", newline="") as stream:
        return list(csv.DictReader(stream, delimiter=delimiter))


def metrics(y, prediction):
    error = prediction - y
    return {"mae": float(np.abs(error).mean()),
            "rmse": float(np.sqrt((error ** 2).mean())),
            "r2": float(1 - (error ** 2).sum() / ((y - y.mean()) ** 2).sum())}


def analyze(rows):
    x = np.array([[float(row[key]) for key in FEATURES] for row in rows])
    y = np.array([float(row["quality"]) for row in rows])
    train = np.array([row["split"] == "train" for row in rows])
    test = ~train
    keys = [tuple(row[key] for key in FEATURES) for row in rows]
    overlap = set(key for key, yes in zip(keys, train) if yes) & set(
        key for key, yes in zip(keys, test) if yes)
    assert not overlap and np.isfinite(x).all() and np.isfinite(y).all()
    mean, scale = x[train].mean(0), x[train].std(0, ddof=0)
    scale[scale == 0] = 1
    z_train, z_test = (x[train] - mean) / scale, (x[test] - mean) / scale
    a_train = np.column_stack([np.ones(train.sum()), z_train])
    a_test = np.column_stack([np.ones(test.sum()), z_test])
    coef = np.linalg.lstsq(a_train, y[train], rcond=None)[0]
    ols = a_test @ coef
    # Second formulation independently checks the OLS solver.
    coef_normal = np.linalg.solve(a_train.T @ a_train, a_train.T @ y[train])
    assert np.allclose(ols, a_test @ coef_normal, atol=1e-9, rtol=1e-9)
    baseline = np.full(test.sum(), y[train].mean())
    model = reference.fit_ridge(x[train], y[train], alpha=10)
    ridge = reference.predict_ridge(model, x[test])
    # Augmented least squares checks the reference's normal-equation implementation.
    penalty = np.column_stack([np.zeros(len(FEATURES)), np.sqrt(10) * np.eye(len(FEATURES))])
    augmented_coef = np.linalg.lstsq(np.vstack([a_train, penalty]),
                                    np.concatenate([y[train], np.zeros(len(FEATURES))]),
                                    rcond=None)[0]
    assert np.allclose(ridge, a_test @ augmented_coef, atol=1e-9, rtol=1e-9)
    full_rows = [tuple(row[key] for key in [*FEATURES, "quality"]) for row in rows]
    return {
        "rows": len(rows), "feature_count": len(FEATURES),
        "train_rows": int(train.sum()), "test_rows": int(test.sum()),
        "missing_cells": sum(value == "" for row in rows for value in row.values()),
        "duplicate_full_rows_excluding_sample_id_and_split": len(rows) - len(set(full_rows)),
        "distinct_feature_profiles": len(set(keys)), "train_test_feature_overlap": len(overlap),
        "quality_distribution": dict(sorted(Counter(row["quality"] for row in rows).items())),
        "quality_mean": float(y.mean()), "alcohol_mean": float(x[:, -1].mean()),
        "pearson_alcohol_quality": float(np.corrcoef(x[:, -1], y)[0, 1]),
        "test_prediction_rules": "continuous; no clipping/rounding; all rows retained",
        "metrics": {"train_mean": metrics(y[test], baseline),
                    "ols": metrics(y[test], ols), "ridge_alpha10": metrics(y[test], ridge)},
        "ridge_minus_ols_rmse": float(metrics(y[test], ridge)["rmse"] - metrics(y[test], ols)["rmse"]),
        "standardized_ols_coefficients": dict(zip(FEATURES, map(float, coef[1:]))),
    }


def main():
    initial_path = ROOT / "02-data/materials/initial/wine-red-initial-1000.csv"
    full_path = ROOT / "02-data/materials/followup/wine-red-full-1599.csv"
    initial, full = load(initial_path), load(full_path)
    raw = load(ROOT / "sources/winequality-red.csv", delimiter=";")
    assert initial == full[:1000] and len(full) == len(raw) == 1599
    assert len(load(ROOT / "sources/winequality-white.csv", delimiter=";")) == 4898
    for i, (row, original) in enumerate(zip(full, raw), 1):
        assert row["sample_id"] == f"red_{i:04d}"
        for key in [*FEATURES, "quality"]:
            assert row[key] == original[key.replace("_", " ")]
        bucket = int(hashlib.sha256("|".join(row[key] for key in FEATURES).encode()).hexdigest()[:8], 16) % 5
        assert row["split"] == ("test" if bucket == 0 else "train")
    results = {"verification_scope": "Local independent calculations only; SciAide E2E not run",
               "python": platform.python_version(), "numpy": np.__version__,
               "method_reference_sha256": hashlib.sha256(SOURCE.read_bytes()).hexdigest(),
               "initial": analyze(initial), "full": analyze(full)}
    # Matched holdout: report full-data refits on the original test records too.
    # This is a continuity diagnostic, not an independent final model-selection test.
    x = np.array([[float(row[key]) for key in FEATURES] for row in full])
    y = np.array([float(row["quality"]) for row in full])
    train = np.array([row["split"] == "train" for row in full])
    common_test = np.array([i < 1000 and row["split"] == "test" for i, row in enumerate(full)])
    mean, scale = x[train].mean(0), x[train].std(0, ddof=0)
    scale[scale == 0] = 1
    a_train = np.column_stack([np.ones(train.sum()), (x[train] - mean) / scale])
    a_test = np.column_stack([np.ones(common_test.sum()), (x[common_test] - mean) / scale])
    ols = a_test @ np.linalg.lstsq(a_train, y[train], rcond=None)[0]
    ridge = reference.predict_ridge(reference.fit_ridge(x[train], y[train], alpha=10), x[common_test])
    results["full_refit_on_initial_test"] = {
        "test_rows": int(common_test.sum()), "test_ids_unchanged": True,
        "metrics": {"train_mean": metrics(y[common_test], np.full(common_test.sum(), y[train].mean())),
                    "ols": metrics(y[common_test], ols), "ridge_alpha10": metrics(y[common_test], ridge)}}
    for label, rows in [("initial", initial), ("full", full)]:
        assert results[label] == analyze(rows)  # Clean refit reproduces all results.
    path = ROOT / "verification/reference-results.json"
    path.write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(results, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
