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
    print(("ok    " if ok else "FALLA ") + name + ("" if ok else f"  [{str(detail)[-600:]}]"), flush=True)
    if not ok:
        FAILS.append(name)
    return ok


def dc(*args, stdin=None, check_rc=False):
    r = subprocess.run(["docker", "compose", "-f", COMPOSE, *args], capture_output=True, text=True, input=stdin, timeout=900)
    if check_rc and r.returncode != 0:
        print(r.stdout, r.stderr)
        raise SystemExit(f"docker compose {' '.join(args)} falló")
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
        st, body = page("/tareas/" + jid)
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
    check("el stack arranca y queda listo", wait_ready(), dc("logs")[1][-2000:])
    logs = dc("logs", "sqsolve")[1]
    m = re.search(r"código de instalación:\s+([A-Z0-9]{5}-[A-Z0-9]{5})", logs)
    if not check("el registro muestra el código de instalación", m, logs[-1500:]):
        return
    st, body = page("/instalar")
    st, loc, _ = http("POST", "/instalar", {"csrf": csrf(body), "code": m.group(1), "username": "admin", "password": "una contraseña larga de prueba",
                                            "password2": "una contraseña larga de prueba", "host": "127.0.0.1", "name": "CI", "preset": "diario"})
    check("instalación desde el panel", loc == "/instalar/kit", loc)
    st, body = page("/")
    tok = csrf(body)
    st, loc, _ = http("POST", "/bibliotecas/nueva", {"csrf": tok, "name": "Prueba de CI", "slug": "prueba_ci", "strategy": "resolve"})
    lib = loc.split("?")[0]
    for _ in range(60):
        st, body = page(lib)
        if 'data-state="awaiting_init"' in body:
            break
        time.sleep(0.5)
    st, loc, body = http("POST", lib + "/contrasena/ver", {"csrf": tok})
    pw = re.search(r'id="r-pass">([^<]+)<', body)
    if not check("la contraseña de la biblioteca se puede ver", pw, body[:400]):
        return
    pw = pw.group(1)
    rc, out = psql("prueba_ci", pw, "postgres", "CREATE DATABASE prueba_ci")
    check("una estación crea la base por el puerto publicado", rc == 0, out)
    rc, out = psql("prueba_ci", pw, "prueba_ci", 'CREATE TABLE "SM_Project"(id serial primary key, name text); '
                   'INSERT INTO "SM_Project"(name) SELECT \'p\'||g FROM generate_series(1,500) g')
    check("y crea sus tablas", rc == 0, out)
    active = False
    for _ in range(60):
        st, body = page(lib)
        if 'data-state="active"' in body:
            active = True
            break
        time.sleep(1)
    check("la biblioteca se activa sola", active)
    st, loc, _ = http("POST", lib + "/respaldar", {"csrf": tok})
    state, text = wait_job(loc)
    check("respaldo desde el panel", state == "succeeded", text)
    st, body = page(lib + "?tab=respaldos")
    bid = re.search(r'href="/respaldos/([0-9A-Z]{26})"', body)
    if bid:
        st, loc, _ = http("POST", f"/respaldos/{bid.group(1)}/verificar", {"csrf": tok})
        state, text = wait_job(loc)
        check("restauración de prueba dentro del contenedor", state == "succeeded", text)
        st, loc, _ = http("POST", f"/respaldos/{bid.group(1)}/restaurar", {"csrf": tok, "mode": "nueva", "slug": "prueba_ci_copia", "name": "Copia"})
        state, text = wait_job(loc)
        check("restaurar como biblioteca nueva", state == "succeeded", text)
    rc, out = dc("exec", "-T", "sqsolve", "sqsolve", "backup", "create", "prueba_ci", "--wait")
    check("la consola dentro del contenedor", rc == 0, out)
    rc, out = dc("exec", "-T", "sqsolve", "sqsolve", "doctor")
    check("doctor sin fallas", rc == 0, out)
    # The folders are 0700 and belong to the service user, not to the runner.
    find = ["find", os.path.join(WORK, "respaldos"), "-name", "*.backup"]
    if subprocess.run(["sudo", "-n", "true"], capture_output=True).returncode == 0:
        find = ["sudo", "-n"] + find
    files = subprocess.run(find, capture_output=True, text=True).stdout.split()
    check("los respaldos quedan en la carpeta del host", len(files) >= 2, files)

    dc("down")
    dc("up", "-d", check_rc=True)
    check("vuelve a quedar listo tras down y up", wait_ready(), dc("logs")[1][-2000:])
    rc, out = psql("prueba_ci", pw, "prueba_ci", 'SELECT count(*) FROM "SM_Project"')
    check("los datos sobreviven a down y up", rc == 0 and out == "500", out)
    st, body = page("/")
    check("la sesión del panel sigue válida", st == 200 and "Panel" in body, st)
    rc, out = dc("run", "--rm", "sqsolve-init")
    check("sqsolve-init se puede volver a correr", rc == 0 and "listo" in out, out)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        check("el recorrido terminó sin excepción", False, repr(e))
    if FAILS:
        print("\nFallas:", *FAILS, sep="\n - ")
        print(dc("logs")[1][-4000:])
        sys.exit(1)
    print("\nTodo bien.")
