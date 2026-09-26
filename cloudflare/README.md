# cloudflare（CloakBrowser 版 CF 解算器）

二开自 `E:\github_project\captcha-solver-main\cloudflare`，适配当前期刊采集框架。

## 为什么存在

scrapling 内置浏览器（patchright / 原版 Playwright）在 CF **managed 质询档**
（交互式 turnstile）被会话层深度识别——真人手点也过不了。cloakbrowser
（Playwright fork）的 `humanize=True` 在 page.mouse 层做 B-spline 人性化
鼠标轨迹，直穿 turnstile 行为检测。实测 4.5-25s 解算成功（scrapling 是 90s
超时），解的 cookie 在 HTTP 层可复用。

## 依赖

仅 `cloakbrowser`（0.5.9，已装在 py3146 环境）。无其它外部依赖。

## 用法

```python
from cloudflare import solve_cf_clearance

r = solve_cf_clearance(
    "https://sms.onlinelibrary.wiley.com/loi/10970266",
    proxy="http://127.0.0.1:7999",   # 解算（和后续复用）必须同一出口 IP
    timeout_s=60,
)
# r: {solved, challenge_solved, clearance_obtained, content_blocked,
#     cf_clearance:{name,value,domain,...}, cookies, user_agent,
#     final_http_status, headers, proxy, elapsed, stop_reason?, error?}
```

返回的 `user_agent` / `cf_clearance` 必须原样用于 HTTP 层请求（cookie 绑 IP+UA）。

## 接入点

`journal_core/sites/wiley/requester.py::_solve_and_export`：
cloakbrowser 为主解算器，scrapling StealthySession 为兜底（non-interactive
场景）。cookie 持久化到 `cookies/wiley/{port}.json`（文件按代理端口隔离，
文件内部再按 hostname 隔离主域、`sms`、`myscp` 等落点域）。

## 已知限制

- `geoip` 已关闭（需下载 70MB GeoLite 库；实测缺失不影响解算）；
- `headless=True` 实测即可（原项目 headful 是 Linux Xvfb 环境的妥协）；
- cookie 严格绑定出口 IP：换代理端口必须重新解算。
- 低信誉 IP 可能在首次点击后已下发 `cf_clearance`，但 DOM 仍有
  第二层 Turnstile。解算器不能仅以 Cookie 存在为成功条件；必须继续
  低频点击，直到质询 DOM 稳定消失。返回值中的 `click_count` 可用于诊断。
- 默认最多点击 8 次；第 8 次后等待 5 秒仍有质询则返回
  `stop_reason=click_limit`。ScienceDirect 引擎收到后立即停止整轮采集，
  不继续重试或跳到下一篇，等待人工更换代理 IP。
- `challenge_solved` 只表示质询 DOM 已稳定退出且取得 `cf_clearance`；
  `solved` 只在最终页面为正常 2xx 内容时为真。若取得 clearance 后落到
  `There was a problem providing the content you requested` 或最终 HTTP 429，
  返回 `content_blocked=true, stop_reason=content_blocked, solved=false`。
- `http1=true` 会给 CloakBrowser 添加 `--disable-http2`。目前只有
  ScienceDirect 请求器会在默认会话 `content_blocked` 后使用该模式；Wiley
  以及其他站点不会自动切换协议。
