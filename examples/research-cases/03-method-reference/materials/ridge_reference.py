"""Single-file method reference for SciAide adaptation, not an executable workflow.

Authored for this case pack, not official code of the supplied papers.
Requires numpy. No file access, network calls, imports of user scripts or main entry.

Model: min ||y - intercept - Z @ beta||^2 + alpha * ||beta||^2.
Z uses training-only mean and population standard deviation (ddof=0).
The intercept is unpenalized. alpha=10 is fixed before inspecting test metrics;
the objective is NOT divided by sample count. Do not tune on the test set.
"""
import numpy as np


def fit_ridge(x_train, y_train, alpha=10.0):
    """Return parameters of standardized ridge regression with a free intercept."""
    x = np.asarray(x_train, dtype=float)
    y = np.asarray(y_train, dtype=float)
    if x.ndim != 2 or y.ndim != 1 or len(x) != len(y) or not len(y):
        raise ValueError("Expected nonempty X[n,p] and y[n]")
    if not np.isfinite(x).all() or not np.isfinite(y).all() or alpha <= 0:
        raise ValueError("Finite inputs and positive alpha required")
    mean = x.mean(axis=0)
    scale = x.std(axis=0, ddof=0)
    scale = np.where(scale == 0.0, 1.0, scale)
    z = (x - mean) / scale
    intercept = float(y.mean())
    beta = np.linalg.solve(z.T @ z + alpha * np.eye(z.shape[1]),
                           z.T @ (y - intercept))
    return {"mean": mean, "scale": scale, "intercept": intercept,
            "beta": beta, "alpha": float(alpha)}


def predict_ridge(model, x):
    """Return continuous scores without clipping or rounding."""
    x = np.asarray(x, dtype=float)
    z = (x - model["mean"]) / model["scale"]
    return model["intercept"] + z @ model["beta"]
