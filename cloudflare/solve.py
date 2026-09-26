"""CloakBrowser 版 Cloudflare 解算器（二开，适配当前期刊采集框架）

来源：E:\\github_project\\captcha-solver-main\\cloudflare（2026-08-26 二开）。
原项目用 cloakbrowser（Playwright fork，带 stealth + humanize 人性化鼠标轨迹）
过 CF 全页 interstitial（Managed Challenge 的 Turnstile checkbox / JS Challenge
自动过），轮询 cookie jar 拿 cf_clearance。

为什么需要它：scrapling 内置浏览器（patchright / 原版 Playwright）在 CF 的
managed 质询档被深度识别——真人手点也过不了（会话层即判定自动化）。
cloakbrowser 的 humanize 在 page.mouse 层做 B-spline 人性化轨迹，是当前项目
过 managed 质询的最后手段。

二开适配点：
- 去掉对 captcha-solver-main 其它模块的依赖（common.browser / turnstile.solve 内联）；
- 同步 API（项目 Requester 是同步的，内部 asyncio.run 包装）；
- proxy 转字符串格式（项目 settings 里是 dict {http,https}）；
- 拿到 cf_clearance 后由调用方持久化（本项目写 cookies/wiley/{port}.json，
  文件内按 hostname 隔离）。

用法：
    from cloudflare import solve_cf_clearance
    result = solve_cf_clearance(url, proxy="http://127.0.0.1:7999", timeout_s=60)
    # result: {solved, cf_clearance, user_agent, cookies, elapsed, error?}
"""
from __future__ import annotations

import asyncio
import logging
import time

import cloakbrowser

logger = logging.getLogger(__name__)

# 质询页 JS 探针：challenge-form + _cf_chl_opt 在两种 interstitial 都出现，
# turnstile iframe 仅 Managed Challenge 出现。
_CF_MARKERS_JS = r"""() => {
  const html = document.documentElement.outerHTML;
  const t = (document.title || '').toLowerCase();
  const titleIsChallenge =
    /just a moment|attention required|checking your browser|verifying you are human/.test(t);
  if (titleIsChallenge) return true;

  // CF 通过后会在正常业务页暂留隐藏 iframe 和 __cf_chl 脚本。旧逻辑只要
  // querySelector 命中就一直判为质询，AOM 实测已经显示完整 LOI、HTTP 200、
  // cf_clearance 已下发，仍会被误等到超时。这里只把可见且有尺寸的节点视为
  // 活跃质询；原始脚本 marker 仅在页面没有实质业务内容时兜底。
  const visible = (el) => {
    if (!el) return false;
    const style = getComputedStyle(el);
    const rect = el.getBoundingClientRect();
    return style.display !== 'none' && style.visibility !== 'hidden' &&
      style.opacity !== '0' && rect.width >= 20 && rect.height >= 20;
  };
  const activeNode = [...document.querySelectorAll(
    '#challenge-form, form#challenge-form, #cf-wrapper, '
    + '.cf-browser-verification, #challenge-running, #trk_jschal_js, '
    + 'iframe[src*="challenges.cloudflare.com"]'
  )].some(visible);
  if (activeNode) return true;

  const bodyText = ((document.body && document.body.innerText) || '').trim();
  const bodyLower = bodyText.toLowerCase();
  // ScienceDirect 的 CPE00001/Turnstile 页面没有 challenge-form，iframe 的
  // src 也可能在运行时被改写，旧选择器因此把肉眼可见的机器人页判成 false。
  // 这些文案只出现在整页质询中，直接作为强标记。
  if (/are you a robot\?|please confirm you are a human by completing the captcha challenge|captcha challenge below/.test(bodyLower)) {
    return true;
  }
  const hasBusinessContent = !!document.querySelector(
    'main, [role="main"], article, .loi, .list-of-issues-detailed'
  ) && bodyText.length >= 500;
  return !hasBusinessContent && /window\._cf_chl_opt|__cf_chl_/.test(html);
}"""

# Cloudflare 质询已经退出并不等于目标内容可用。ScienceDirect 会在已经下发
# cf_clearance 后落到黄色错误页；此时 DOM 中没有 challenge marker，旧逻辑会
# 把它误报为 solved。这里只负责识别终态，具体是否切 HTTP/1.1 由站点请求器决定。
_CONTENT_BLOCKED_JS = r"""() => {
  const text = ((document.body && document.body.innerText) || '').toLowerCase();
  if (text.includes('there was a problem providing the content you requested')) {
    return 'content_request_problem';
  }
  return '';
}"""

def _clearance(cookies: list) -> dict | None:
    """返回 cf_clearance cookie 完整记录或 None"""
    return next((c for c in cookies if c.get("name") == "cf_clearance"), None)


def _classify_page_state(*, clearance_obtained: bool, clear_streak: int,
                         challenge_present: bool, final_http_status,
                         terminal_error: str) -> dict[str, bool]:
    """把浏览器终态拆成质询、内容阻断和正常内容三个互不混淆的事实。"""
    challenge_solved = clearance_obtained and clear_streak >= 3
    content_blocked = (
        final_http_status == 429 or terminal_error == "content_request_problem"
    )
    solved = (
        not challenge_present
        and not content_blocked
        and isinstance(final_http_status, (int, float))
        and 200 <= final_http_status < 300
    )
    return {
        "challenge_solved": challenge_solved,
        "content_blocked": content_blocked,
        "solved": solved,
    }


async def _is_interstitial(page) -> bool:
    try:
        return bool(await page.evaluate(_CF_MARKERS_JS))
    except Exception:
        return False  # 导航中途；交给 cookie 轮询判定


async def _human_click_iframe(page, fr) -> bool:
    """humanized 页面级鼠标点击 iframe 内 checkbox。

    cloakbrowser 的 humanize 会 hook page.mouse.click（B-spline 轨迹 + 过冲），
    但不会 hook frame.click——frame 内点击是机械瞬间点击。所以取 iframe 的
    页面绝对坐标，用 humanized page.mouse 在 checkbox 偏移处点击。
    """
    # 优先取 checkbox/label 自身的精确页面坐标。Playwright 对 iframe 内
    # locator.bounding_box() 返回相对于主 frame viewport 的坐标，因此可以继续
    # 使用 humanized page.mouse；旧版固定点 iframe 左侧 +30px，ScienceDirect
    # 实测正好落在 checkbox 右边缘，虽然发出了 click 却没有勾选。
    selectors = (
        'input[type="checkbox"]',
        '[role="checkbox"]',
        '.ctp-checkbox-label',
        'label',
    )
    for selector in selectors:
        try:
            locator = fr.locator(selector).first
            if not await locator.is_visible():
                continue
            box = await locator.bounding_box()
            if not box or box.get("width", 0) < 4 or box.get("height", 0) < 4:
                continue
            await page.mouse.click(
                box["x"] + box["width"] / 2,
                box["y"] + box["height"] / 2,
            )
            return True
        except Exception:
            continue

    # widget 刚挂载时内部 DOM 可能尚不可查询。退回 iframe 坐标，但把点击点
    # 放在实测 checkbox 中心附近（左侧 +20px），不再使用落在边缘的 +30px。
    try:
        el = await fr.frame_element()
        box = await el.bounding_box()
    except Exception:
        return False
    if not box or box.get("width", 0) < 20 or box.get("height", 0) < 20:
        return False
    x = box["x"] + min(20, box["width"] / 2)
    y = box["y"] + box["height"] / 2
    await page.mouse.click(x, y)
    return True


async def _click_turnstile_checkbox(page, attempts: int = 25) -> bool:
    """点击跨域 CF iframe 内的 checkbox；失败退回 frame 级 selector 点击。"""
    for _ in range(attempts):
        # 页面上可能同时存在多个 challenges.cloudflare.com frame。优先处理
        # 真正的 Turnstile widget，避免先点到隐藏的遥测/校验 frame 后误报成功。
        frames = sorted(
            page.frames,
            key=lambda fr: "turnstile" not in (fr.url or "").lower(),
        )
        for fr in frames:
            if "challenges.cloudflare.com" in (fr.url or ""):
                if await _human_click_iframe(page, fr):
                    return True
                # 仅点击真实控件；点击 frame 的 body 会无条件成功并产生
                # “clicked=True”假阳性，随后耗尽等待时间却没有任何验证动作。
                for sel in ("input[type=checkbox]", "[role=checkbox]", "label"):
                    try:
                        await fr.click(sel, timeout=2000)
                        return True
                    except Exception:
                        continue
        await asyncio.sleep(1)
    return False


def _launch_kwargs(proxy: str | None, *, http1: bool = False) -> dict:
    """生成 cloakbrowser.launch_async 参数。

    headless=True 实测即可过质询（原项目 headful 是 Xvfb 环境妥协，Windows 不需要）；
    humanize=True 是关键（B-spline 人性化鼠标轨迹，绕过 turnstile 行为检测）；
    geoip 不开：它需要下载 ~70MB GeoLite 数据库（首次初始化 5 分钟+），且实测
    缺失时照样解算成功（时区/locale 与代理 IP 不一致只降反检测分，不影响拿 cookie）。
    """
    #There was a problem providing the content you requested

    kwargs = {
        "headless": True,
        "humanize": True,
        "stealth_args": True,
        "geoip": True,
        "human_preset": "careful",
        # "args":["--fingerprint-noise=false","--fingerprint-windows-font-metrics"],
        **({"proxy": proxy} if proxy else {}),
    }
    if http1:
        kwargs["args"] = ["--disable-http2"]
    return kwargs


async def _solve_async(url: str, proxy: str | None, timeout_s: int,
                       max_clicks: int, http1: bool = False,
                       initial_cookies: list[dict] | None = None) -> dict:
    t0 = time.monotonic()
    browser = await cloakbrowser.launch_async(**_launch_kwargs(proxy, http1=http1))
    try:
        page = await browser.new_page()
        try:
            # 某些站点除 Cloudflare 外还有自己的业务会话门槛。允许站点插件把
            # 已持久化的第一方会话 Cookie 注入临时浏览器，避免“CF 已通过，
            # 业务层却因全新匿名会话落到二次验证页”。默认空，不改变现有站点。
            if initial_cookies:
                await page.context.add_cookies(initial_cookies)
            navigation = await page.goto(url, wait_until="domcontentloaded", timeout=45000)

            # Managed: 人性化点击 iframe checkbox（JS Challenge 下是 no-op）
            click_count = 0
            try:
                if await _click_turnstile_checkbox(page, attempts=8):
                    click_count += 1
            except Exception:
                pass
            last_click_at = time.monotonic() if click_count else 0.0

            # 低信誉 IP 会先下发 cf_clearance，再显示第二层
            # Turnstile。不能只看 Cookie 就返回；必须继续低频点击，
            # 并要求质询 DOM 连续 3 次轮询消失，避免层间切换误判。
            clg, cookies = None, []
            deadline = time.monotonic() + timeout_s
            next_click_at = time.monotonic() + 4
            clear_streak = 0
            challenge_present = True
            logged_second_stage = False
            click_limit_reached = False
            while time.monotonic() < deadline:
                cookies = await page.context.cookies()
                clg = _clearance(cookies)
                challenge_present = await _is_interstitial(page)
                if clg and not challenge_present:
                    clear_streak += 1
                    if clear_streak >= 3:
                        break
                else:
                    clear_streak = 0
                if clg and challenge_present and not logged_second_stage:
                    logger.info("cf_clearance 已下发但质询仍存在，继续处理二次点击")
                    logged_second_stage = True
                # 某些站点会在首击过早或验证失败后刷新 widget。旧逻辑只点一次，
                # 随后一直空等到超时；挑战仍存在时低频补点即可，避免高频点击
                # 干扰正在进行的验证。
                now = time.monotonic()
                # 第 max_clicks 次点击后再留 5 秒给验证跳转；仍在质询
                # 就判定当前 IP 不可用，不再无限点击。
                if (challenge_present and click_count >= max_clicks
                        and last_click_at and now - last_click_at >= 5):
                    click_limit_reached = True
                    break
                if (now >= next_click_at and challenge_present
                        and click_count < max_clicks):
                    try:
                        if await _click_turnstile_checkbox(page, attempts=1):
                            click_count += 1
                            last_click_at = time.monotonic()
                    except Exception:
                        pass
                    next_click_at = now + 5
                await asyncio.sleep(1)

            ua = await page.evaluate("() => navigator.userAgent")
            lang = await page.evaluate("() => navigator.language")
            try:
                final_http_status = await page.evaluate(
                    "() => performance.getEntriesByType('navigation')[0]?.responseStatus ?? null"
                )
            except Exception:
                final_http_status = None
            try:
                terminal_error = str(await page.evaluate(_CONTENT_BLOCKED_JS) or "")
            except Exception:
                terminal_error = ""

            clearance_obtained = bool(clg)
            state = _classify_page_state(
                clearance_obtained=clearance_obtained,
                clear_streak=clear_streak,
                challenge_present=challenge_present,
                final_http_status=final_http_status,
                terminal_error=terminal_error,
            )
            challenge_solved = state["challenge_solved"]
            content_blocked = state["content_blocked"]
            # solved 专指真正落到正常内容响应；不能再以“有 clearance 且质询
            # DOM 消失”代替。challenge_solved 与 solved 因此可以一真一假。
            solved = state["solved"]
            result = {
                "solved": solved,
                "challenge_solved": challenge_solved,
                "clearance_obtained": clearance_obtained,
                "content_blocked": content_blocked,
                "cf_clearance": clg,
                "cookies": cookies,
                "user_agent": ua,
                "headers": {"User-Agent": ua, "Accept-Language": lang},
                "proxy": proxy,
                "method": "interstitial_http1" if http1 else "interstitial",
                "http1": http1,
                "click_count": click_count,
                "max_clicks": max_clicks,
                "click_limit_reached": click_limit_reached,
                "challenge_present": challenge_present,
                "initial_status": getattr(navigation, "status", None),
                "final_http_status": final_http_status,
                "final_url": page.url,
                "final_title": await page.title(),
                "terminal_error": terminal_error or None,
                "elapsed": round(time.monotonic() - t0, 1),
            }
            if click_limit_reached:
                result["stop_reason"] = "click_limit"
                result["error"] = (
                    f"challenge still present after {click_count}/{max_clicks} clicks; "
                    "proxy IP rotation required"
                )
            elif content_blocked:
                result["stop_reason"] = "content_blocked"
                result["error"] = (
                    f"challenge exited but content blocked: "
                    f"HTTP {final_http_status or 'unknown'}"
                    + (f" ({terminal_error})" if terminal_error else "")
                )
            elif not clg:
                result["error"] = "cf_clearance not set (challenge unsolved)"
            elif not solved:
                result["error"] = (
                    "cf_clearance set but challenge still present "
                    f"after {click_count} click(s)"
                )
            # input(f"Press Enter to continue...\n{result}")
            return result
        finally:
            await page.close()
    finally:
        await browser.close()


def solve_cf_clearance(url: str, proxy: str | None = None,
                       timeout_s: int = 60, max_clicks: int = 8,
                       http1: bool = False,
                       initial_cookies: list[dict] | None = None) -> dict:
    """同步入口：过 CF interstitial 拿 cf_clearance（供同步 Requester 调用）。"""
    return asyncio.run(
        _solve_async(
            url, proxy, timeout_s, max(1, int(max_clicks)), http1=http1,
            initial_cookies=initial_cookies,
        )
    )
