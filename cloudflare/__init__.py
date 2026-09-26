"""CloakBrowser 版 Cloudflare 解算器（二开）

基于 E:\\github_project\\captcha-solver-main\\cloudflare 二开，适配当前
期刊采集框架：同步 API、无外部依赖（仅 cloakbrowser）、proxy 字符串格式。

主要入口：
    from cloudflare import solve_cf_clearance
    r = solve_cf_clearance(url, proxy="http://127.0.0.1:7999", timeout_s=60)
"""
from .solve import solve_cf_clearance

__all__ = ["solve_cf_clearance"]
__version__ = "0.1.0"
