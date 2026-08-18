import os, time, subprocess, httpx, pyotp
from playwright.sync_api import sync_playwright
from apscheduler.schedulers.background import BackgroundScheduler

USERNAME       = os.environ["IBKR_USERNAME"]
PASSWORD       = os.environ["IBKR_PASSWORD"]
TOTP_SECRET    = os.environ["IBKR_TOTP_SECRET"]
CPG_URL        = os.environ.get("CPG_URL", "https://localhost:5000")

RESTART_COOLDOWN_SEC = 3600
POST_RESTART_WAIT_SEC = 30

_last_restart_time = 0.0
_previous_login_failed = False

def restart_gateway():
    global _last_restart_time
    print("Restarting CP Gateway process...")
    result = subprocess.run(
        ["pkill", "-f", "GatewayStart"],
        capture_output=True, text=True
    )
    if result.returncode not in (0, 1):
        raise Exception(f"pkill failed: {result.stderr.strip()}")
    _last_restart_time = time.time()
    print(f"CP Gateway killed. Waiting {POST_RESTART_WAIT_SEC}s for respawn loop before login...")
    time.sleep(POST_RESTART_WAIT_SEC)

def js_fill(page, selector, value):
    page.evaluate(f"""
        const el = document.querySelector('{selector}');
        const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
        setter.call(el, '{value}');
        el.dispatchEvent(new Event('input', {{bubbles: true}}));
        el.dispatchEvent(new Event('change', {{bubbles: true}}));
    """)

def js_select(page, selector, value):
    page.evaluate(f"""
        const el = document.querySelector('{selector}');
        const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value').set;
        setter.call(el, '{value}');
        el.dispatchEvent(new Event('change', {{bubbles: true}}));
    """)

def is_session_alive(cookies: dict) -> bool:
    try:
        r = httpx.get(
            f"{CPG_URL}/v1/api/iserver/auth/status",
            cookies=cookies,
            verify=False,
            timeout=10
        )
        if r.status_code != 200:
            print(f"Auth status: {r.status_code} — DEAD")
            return False
        data = r.json()
        print(f"Return json: {data}")
        authenticated = data.get("authenticated", False)
        connected     = data.get("connected", False)
        print(f"Auth status: authenticated={authenticated} connected={connected}")
        return authenticated and connected
    except Exception as e:
        print(f"Auth status error: {e}")
        return False

def login() -> dict:
    global _previous_login_failed

    if _previous_login_failed and (time.time() - _last_restart_time) > RESTART_COOLDOWN_SEC:
        restart_gateway()

    try:
        cookies = _do_login_flow()
        _previous_login_failed = False
        return cookies
    except Exception:
        _previous_login_failed = True
        raise

def _do_login_flow() -> dict:
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=False, args=["--no-sandbox"])
        ctx = browser.new_context(ignore_https_errors=True)
        page = ctx.new_page()

        # Step 1: Load page
        page.goto(CPG_URL, wait_until="domcontentloaded")
        page.wait_for_selector('#xyz-field-username', state="visible", timeout=30000)
        print("Page loaded")

        # Step 2: Fill credentials
        js_fill(page, '#xyz-field-username', USERNAME)
        js_fill(page, '#xyz-field-password', PASSWORD)
        page.evaluate("document.querySelector('button.xyz-button-login').click()")
        page.wait_for_timeout(3000)
        print("Credentials submitted")

        # Step 3: Select Mobile Authenticator App from 2FA dropdown
        options = page.evaluate("""
            () => Array.from(document.querySelector('select.xyz-multipleselect').options)
                .map(o => ({value: o.value, text: o.text}))
        """)
        print(f"2FA dropdown options: {options}")
        js_select(page, 'select.xyz-multipleselect', '4')
        page.wait_for_timeout(2000)
        selected = page.evaluate("""
            () => {
                const el = document.querySelector('select.xyz-multipleselect');
                return {value: el.value, text: el.options[el.selectedIndex]?.text};
            }
        """)
        print(f"2FA method selected: {selected}")
        page.screenshot(path="/tmp/ibkr_2fa_selected.png")

        # Step 4: Wait for TOTP field
        try:
            page.wait_for_selector('#xyz-field-silver-response', state="visible", timeout=10000)
            print("TOTP field visible")
        except Exception:
            print(f"TOTP field never appeared — URL: {page.url}")
            browser.close()
            raise Exception("Login failed — TOTP field did not appear")

        # Step 5: Fill TOTP with timing guard
        totp = pyotp.TOTP(TOTP_SECRET)
        remaining = totp.interval - (time.time() % totp.interval)
        if remaining < 3:
            print(f"Code expiring in {remaining:.1f}s, waiting for next window...")
            time.sleep(remaining + 1)
        code = totp.now()
        print(f"TOTP code: {code} ({int(totp.interval - (time.time() % totp.interval))}s remaining)")

        js_fill(page, '#xyz-field-silver-response', code)
        page.wait_for_timeout(500)
        page.screenshot(path="/tmp/ibkr_totp_filled.png")

        # Step 6: Click visible Login button
        clicked = page.evaluate("""
            () => {
                const target = Array.from(document.querySelectorAll('button')).find(b => {
                    const r = b.getBoundingClientRect();
                    return b.textContent.trim() === 'Login' && r.width > 0 && r.height > 0;
                });
                if (target) { target.click(); return target.className; }
                return null;
            }
        """)
        print(f"Clicked submit: {clicked}")
        page.wait_for_timeout(1500)
        page.screenshot(path="/tmp/ibkr_after_click.png")
        error_text = page.evaluate("""
            () => {
                const el = document.querySelector('.xyz-error, .alert, [class*="error"]');
                return el ? el.textContent.trim() : null;
            }
        """)
        print(f"Error banner right after click: {error_text}")

        # Step 7: Wait for redirect away from login page
        try:
            page.wait_for_function(
                "() => !window.location.href.includes('/sso/Login')",
                timeout=10000
            )
            print(f"Redirected to: {page.url}")
        except Exception:
            print(f"No redirect — still on: {page.url}")
            page.screenshot(path="/tmp/ibkr_login_debug.png")
            browser.close()
            raise Exception("Login failed — page did not redirect after TOTP submit")

        # Dispatcher finishes establishing the session asynchronously after
        # the redirect fires, so grabbing cookies immediately can race it.
        page.wait_for_timeout(3000)
        page.screenshot(path="/tmp/ibkr_login_debug.png")
        cookies = {c["name"]: c["value"] for c in ctx.cookies()}
        browser.close()

        # Step 8: Verify session is authenticated
        time.sleep(15)
        if not is_session_alive(cookies):
            raise Exception("Login failed — session not authenticated after redirect")

        print(f"Login verified. Cookies: {list(cookies.keys())}")
        return cookies

def do_login(max_attempts: int = 3) -> dict:
    for attempt in range(1, max_attempts + 1):
        try:
            print(f"\n--- Login attempt {attempt}/{max_attempts} ---")
            return login()
        except Exception as e:
            print(f"Attempt {attempt} failed: {e}")
            if attempt < max_attempts:
                wait = attempt * 5
                print(f"Waiting {wait}s before retry...")
                time.sleep(wait)
    raise Exception(f"All {max_attempts} login attempts failed — manual intervention required")

def relogin_loop():
    fail_count = 0
    cookies = do_login()
    print("Startup login successful — tickle loop starting\n")

    def safe_tickle():
        nonlocal cookies, fail_count

        # Tickle first to keep session warm
        try:
            httpx.post(
                f"{CPG_URL}/v1/api/tickle",
                cookies=cookies,
                verify=False,
                timeout=10
            )
        except Exception as e:
            print(f"Tickle post error: {e}")

        # Then check if session is actually alive
        if is_session_alive(cookies):
            fail_count = 0
            print("Session alive")
        else:
            fail_count += 1
            print(f"Session dead (fail {fail_count}/3)")

            if fail_count >= 3:
                print("3 consecutive dead sessions — re-logging in...")
                try:
                    cookies = do_login()
                    fail_count = 0
                    print("Re-login successful")
                except Exception as e:
                    print(f"Re-login failed: {e} — will retry on next tick")

    scheduler = BackgroundScheduler()
    scheduler.add_job(safe_tickle, "interval", minutes=1)
    scheduler.start()

    while True:
        time.sleep(60)

if __name__ == "__main__":
    print("Starting IBKR CPG login bot...")
    print(f"CPG_URL: {CPG_URL}")
    relogin_loop()
