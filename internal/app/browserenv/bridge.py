# SciAide wrapper: runtime paths, proxy snapshot and private result channel only.
# The supplied solve.py challenge algorithm is not modified.
import os, sys, json, pathlib, importlib.util
request = json.loads(os.environ.pop("SCIAIDE_BROWSER_REQUEST"))
# Chromium accepts socks5:// (remote DNS), not the requests-specific socks5h spelling.
if request.get("proxy", "").startswith("socks5h://"):
    request["proxy"] = "socks5://" + request["proxy"][10:]
root = pathlib.Path(request["root"]).resolve()
root.mkdir(parents=True, exist_ok=True)
for name in ("tmp", "cache", "playwright"):
    (root / name).mkdir(exist_ok=True)
os.environ.update({"CLOAKBROWSER_CACHE_DIR": str(root / "cache"),
                   "CLOAKBROWSER_AUTO_UPDATE": "false", "CLOAKBROWSER_GEOIP_AUTO_UPDATE": "false",
                   "PLAYWRIGHT_BROWSERS_PATH": str(root / "playwright"),
                   "TEMP": str(root / "tmp"), "TMP": str(root / "tmp"), "TMPDIR": str(root / "tmp"),
                   "XDG_CACHE_HOME": str(root / "cache"), "PYTHONDONTWRITEBYTECODE": "1"})
# Windows registry proxies must not override an explicit direct configuration.
import urllib.request
urllib.request.getproxies = lambda: ({"http": request["proxy"], "https": request["proxy"]} if request.get("proxy") else {})
try:
    import cloakbrowser.geoip as geoip
    # Keep optional database maintenance in the explicit install operation.
    geoip._maybe_trigger_update = lambda path: None
    if request["action"] == "install":
        from cloakbrowser.download import ensure_binary
        binary = str(ensure_binary())
        geoip._ensure_geoip_db()
        if not pathlib.Path(binary).resolve().is_relative_to(root):
            raise RuntimeError("browser binary escaped project runtime")
        result = {"ready": True, "binaryPath": binary}
    elif request["action"] == "status":
        from cloakbrowser.config import get_binary_path
        binary = pathlib.Path(get_binary_path()).resolve()
        result = {"ready": binary.is_file() and binary.is_relative_to(root), "binaryPath": str(binary)}
    else:
        spec = importlib.util.spec_from_file_location("sciaide_cf_solver", root / "solve.py")
        solver = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(solver)
        from cloakbrowser.geoip import resolve_proxy_exit_ip
        before = resolve_proxy_exit_ip(request.get("proxy") or None)
        if not before:
            raise RuntimeError("cannot verify browser egress IP")
        result = solver.solve_cf_clearance(request["url"], proxy=request.get("proxy") or None,
                                          timeout_s=60, max_clicks=8)
        after = resolve_proxy_exit_ip(request.get("proxy") or None)
        result["egressIP"] = before
        result["egressStable"] = bool(after and before == after)
    pathlib.Path(request["result"]).write_text(json.dumps(result, ensure_ascii=True), encoding="utf-8")
except Exception as exc:
    # Raw exceptions may include proxy credentials/cookies. Keep them out of stdout/audit.
    pathlib.Path(request["result"]).write_text(json.dumps({"error": type(exc).__name__}), encoding="utf-8")
    sys.exit(1)
