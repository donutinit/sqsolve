# SPDX-License-Identifier: AGPL-3.0-only
"""End-to-end check of the published image with deploy/compose.yaml.

Runs in CI after the image is built: installs through the panel, simulates a
Resolve station over TCP on the published port, backs up, verifies, restores,
restarts the stack and uses the console inside the container. Only the
standard library and the docker and psql commands are used.

Usage: compose_e2e.py <compose file> <work dir>
"""
import http.cookiejar
import json
import os
import re
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

COMPOSE, WORK = sys.argv[1], sys.argv[2]
PANEL = "https://127.0.0.1:9432"
FAILS = []


def check(name, ok, detail=""):
    print(("ok    " if ok else "FAIL  ") + name + ("" if ok else f"  [{str(detail)[-600:]}]"), flush=True)
    if not ok:
        FAILS.append(name)
    return ok


def dc(*args, stdin=None, check_rc=False):
    r = subprocess.run(["docker", "compose", "-f", COMPOSE, *args], capture_output=True, text=True, input=stdin, timeout=900)
    if check_rc and r.returncode != 0:
        print(r.stdout, r.stderr)
        raise SystemExit(f"docker compose {' '.join(args)} failed")
    return r.returncode, r.stdout + r.stderr


ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE
jar = http.cookiejar.CookieJar()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx), urllib.request.HTTPCookieProcessor(jar), NoRedirect)


def http(method, path, data=None):
    body = urllib.parse.urlencode(data, doseq=True).encode() if data is not None else None
    req = urllib.request.Request(PANEL + path, data=body, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
    try:
        resp = opener.open(req, timeout=60)
        return resp.status, resp.headers.get("Location", ""), resp.read().decode(errors="replace")
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Location", ""), e.read().decode(errors="replace")
    except OSError as e:
        return 0, "", str(e)


def page(path):
    for _ in range(6):
        st, loc, body = http("GET", path)
        if st in (302, 303):
            path = loc
            continue
        return st, body
    return st, body


def csrf(body):
    m = re.search(r'name="csrf" value="([^"]+)"', body)
    return m.group(1) if m else ""


def wait_ready(timeout=180):
    end = time.time() + timeout
    while time.time() < end:
        if http("GET", "/readyz")[0] == 200:
            return True
        time.sleep(2)
    return False


def wait_job(loc, timeout=300):
    jid = loc.rsplit("/", 1)[-1]
    end = time.time() + timeout
    while time.time() < end:
        st, body = page("/jobs/" + jid)
        m = re.search(r'data-job-state="(\w+)"', body)
        if m and m.group(1) in ("succeeded", "failed", "canceled", "interrupted"):
            return m.group(1), re.sub(r"<[^>]+>", " ", body)
        time.sleep(1)
    return "timeout", ""


def psql(user, password, db, sql):
    env = dict(os.environ, PGPASSWORD=password, PGCONNECT_TIMEOUT="10")
    r = subprocess.run(["psql", "-X", "-h", "127.0.0.1", "-p", "5432", "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1", "-Atc", sql],
                       env=env, capture_output=True, text=True, timeout=300)
    return r.returncode, (r.stdout + r.stderr).strip()


def main():
    check("the stack starts and becomes ready", wait_ready(), dc("logs")[1][-2000:])
    logs = dc("logs", "--no-log-prefix", "sqsolve")[1]
    m = re.search(r"installation code:.*?\b([A-Z0-9]{5}-[A-Z0-9]{5})\b", logs, re.S)
    if not check("the log shows the installation code", m, logs[-1500:]):
        return
    st, body = page("/setup")
    st, loc, _ = http("POST", "/setup", {"csrf": csrf(body), "code": m.group(1), "username": "admin", "password": "a long test password",
                                            "password2": "a long test password", "host": "127.0.0.1", "name": "CI", "preset": "daily"})
    check("setup from the panel", loc == "/setup/kit", loc)
    st, body = page("/")
    tok = csrf(body)
    st, loc, _ = http("POST", "/libraries/new", {"csrf": tok, "name": "CI test", "slug": "ci_test", "strategy": "resolve"})
    lib = loc.split("?")[0]
    for _ in range(60):
        st, body = page(lib)
        if 'data-state="awaiting_init"' in body:
            break
        time.sleep(0.5)
    st, loc, body = http("POST", lib + "/password/reveal", {"csrf": tok})
    pw = re.search(r'id="r-pass">([^<]+)<', body)
    if not check("the library password can be revealed", pw, body[:400]):
        return
    pw = pw.group(1)
    rc, out = psql("ci_test", pw, "postgres", "CREATE DATABASE ci_test")
    check("a workstation creates the database through the published port", rc == 0, out)
    rc, out = psql("ci_test", pw, "ci_test", 'CREATE TABLE "SM_Project"(id serial primary key, name text); '
                   'INSERT INTO "SM_Project"(name) SELECT \'p\'||g FROM generate_series(1,500) g')
    check("and creates its tables", rc == 0, out)
    active = False
    for _ in range(60):
        st, body = page(lib)
        if 'data-state="active"' in body:
            active = True
            break
        time.sleep(1)
    check("the library activates by itself", active)
    st, loc, _ = http("POST", lib + "/backup", {"csrf": tok})
    state, text = wait_job(loc)
    check("backup from the panel", state == "succeeded", text)
    st, body = page(lib + "?tab=backups")
    bid = re.search(r'href="/backups/([0-9A-Z]{26})"', body)
    if bid:
        st, loc, _ = http("POST", f"/backups/{bid.group(1)}/verify", {"csrf": tok})
        state, text = wait_job(loc)
        check("test restore inside the container", state == "succeeded", text)
        st, loc, _ = http("POST", f"/backups/{bid.group(1)}/restore", {"csrf": tok, "mode": "new", "slug": "ci_test_copy", "name": "Copy"})
        state, text = wait_job(loc)
        check("restore as a new library", state == "succeeded", text)
    rc, out = dc("exec", "-T", "sqsolve", "sqsolve", "backup", "create", "ci_test", "--wait")
    check("the console inside the container", rc == 0, out)
    rc, out = dc("exec", "-T", "sqsolve", "sqsolve", "doctor")
    check("doctor without failures", rc == 0, out)
    # The folders are 0700 and belong to the service user, not to the runner.
    find = ["find", os.path.join(WORK, "backups"), "-name", "*.backup"]
    if subprocess.run(["sudo", "-n", "true"], capture_output=True).returncode == 0:
        find = ["sudo", "-n"] + find
    files = subprocess.run(find, capture_output=True, text=True).stdout.split()
    check("backups end up in the host folder", len(files) >= 2, files)

    dc("down")
    dc("up", "-d", check_rc=True)
    check("ready again after down and up", wait_ready(), dc("logs")[1][-2000:])
    rc, out = psql("ci_test", pw, "ci_test", 'SELECT count(*) FROM "SM_Project"')
    check("data survives down and up", rc == 0 and out == "500", out)
    st, body = page("/")
    check("the panel session is still valid", st == 200 and "Dashboard" in body, st)
    rc, out = dc("run", "--rm", "sqsolve-init")
    check("sqsolve-init can run again", rc == 0 and "done" in out, out)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        check("the walkthrough finished without an exception", False, repr(e))
    if FAILS:
        print("\nFailures:", *FAILS, sep="\n - ")
        print(dc("logs")[1][-4000:])
        sys.exit(1)
    print("\nAll good.")
