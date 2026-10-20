# SPDX-License-Identifier: AGPL-3.0-only
"""Failure scenarios against a real PostgreSQL 18 and a real sqsolve server.

Simulates Resolve stations over TCP and provokes what happens in a studio: a
station writing during a backup, a restore with Resolve open, sqsolve killed
in the middle of a backup, PostgreSQL going down, a backups folder without
permission. It only signals processes it started itself.

Needs Go and the PostgreSQL 18 server binaries (postgres, initdb, pg_isready,
psql, pg_dump, pg_restore) in PATH or in SQSOLVE_TEST_PG_BIN. Data goes to
SQSOLVE_SCENARIOS_DIR (default ~/.cache/sqsolve-scenarios), which is wiped.

Usage: python3 test/scenarios.py
"""
import http.cookiejar, os, re, shutil, signal, ssl, subprocess, sys, tempfile, time, urllib.parse, urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE_DIR = os.environ.get("SQSOLVE_SCENARIOS_DIR") or os.path.expanduser("~/.cache/sqsolve-scenarios")
BIN = BASE_DIR.rstrip("/") + ".bin"
PGPORT = 55433
PANEL = "https://127.0.0.1:9433"
# Unix socket paths are limited to about 100 bytes, so the socket lives in a
# short temporary directory.
SOCK = tempfile.mkdtemp(prefix="sqs")

ENV = dict(os.environ)
if ENV.get("SQSOLVE_TEST_PG_BIN"):
    ENV["PATH"] = ENV["SQSOLVE_TEST_PG_BIN"] + os.pathsep + ENV.get("PATH", "")
os.environ["PATH"] = ENV["PATH"]
ENV.update({
    "SQSOLVE_APP_DIR": f"{BASE_DIR}/app", "SQSOLVE_BACKUP_DIR": f"{BASE_DIR}/backups",
    "SQSOLVE_PG_DIR": f"{BASE_DIR}/pg", "SQSOLVE_PG_DATA_DIR": f"{BASE_DIR}/pg/18/docker",
    "SQSOLVE_PG_SOCKET_DIR": SOCK, "SQSOLVE_PG_PORT": str(PGPORT), "SQSOLVE_PG_LISTEN": "127.0.0.1",
    "SQSOLVE_LISTEN": "127.0.0.1:9433", "SQSOLVE_LOG_FORMAT": "text", "SQSOLVE_ADVERTISED_HOST": "127.0.0.1",
    "SQSOLVE_UID": str(os.getuid()), "SQSOLVE_GID": str(os.getgid()), "TZ": "America/Mexico_City",
    "SQSOLVE_PG_MEMORY_MB": "512",
})

RESULTS = []
def check(name, cond, detail=""):
    RESULTS.append((name, bool(cond), detail))
    print(("PASS " if cond else "FAIL ") + name + (f"  [{detail}]" if detail and not cond else ""), flush=True)
    return cond

def log(msg):
    print("     " + msg, flush=True)

# ---------- processes ----------
procs = {}
def start_pg():
    f = open(f"{BASE_DIR}/postgres.log", "a")
    procs["pg"] = subprocess.Popen(["postgres", "-c", f"config_file={BASE_DIR}/pg/sqsolve/postgresql.conf"], env=ENV, stdout=f, stderr=f)
    for _ in range(100):
        if subprocess.run(["pg_isready", "-h", SOCK, "-p", str(PGPORT)], capture_output=True).returncode == 0:
            return True
        time.sleep(0.1)
    return False

def stop_pg(sig=signal.SIGINT):
    p = procs.pop("pg", None)
    if p:
        p.send_signal(sig)
        p.wait(60)

def start_sq():
    f = open(f"{BASE_DIR}/sqsolve.log", "a")
    procs["sq"] = subprocess.Popen([BIN, "serve"], env=ENV, stdout=f, stderr=f)
    for _ in range(150):
        try:
            if get_raw("/readyz")[0] == 200:
                return True
        except Exception:
            pass
        time.sleep(0.2)
    return False

def stop_sq(sig=signal.SIGTERM):
    p = procs.pop("sq", None)
    if p:
        p.send_signal(sig)
        p.wait(150)

def cli(*args, stdin=None):
    r = subprocess.run([BIN, *args], env=ENV, capture_output=True, text=True, input=stdin, timeout=600)
    return r.returncode, r.stdout + r.stderr

# ---------- http ----------
sslctx = ssl.create_default_context(); sslctx.check_hostname = False; sslctx.verify_mode = ssl.CERT_NONE
jar = http.cookiejar.CookieJar()
class NoRedir(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k): return None
opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=sslctx), urllib.request.HTTPCookieProcessor(jar), NoRedir)

def raw(method, path, body=None, headers=None):
    r = urllib.request.Request(PANEL + path, data=body, method=method, headers=headers or {})
    try:
        resp = opener.open(r, timeout=120)
        return resp.status, resp.headers, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()

def get_raw(path):
    return raw("GET", path)

def page(path):
    for _ in range(6):
        st, h, b = raw("GET", path)
        if st in (302, 303):
            path = h["Location"]; continue
        return st, b.decode(errors="replace"), path
    return st, b.decode(errors="replace"), path

def post(path, data):
    st, h, b = raw("POST", path, urllib.parse.urlencode(data, doseq=True).encode(), {"Content-Type": "application/x-www-form-urlencoded"})
    return st, h.get("Location", ""), b.decode(errors="replace")

def post_multipart(path, fields, files):
    boundary = "----sqsolveTorture7MA4YWxkTrZu0gW"
    out = bytearray()
    for k, v in fields:
        out += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
    for k, name, data in files:
        out += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"; filename=\"{name}\"\r\nContent-Type: application/octet-stream\r\n\r\n".encode()
        out += data + b"\r\n"
    out += f"--{boundary}--\r\n".encode()
    st, h, b = raw("POST", path, bytes(out), {"Content-Type": f"multipart/form-data; boundary={boundary}"})
    return st, h.get("Location", ""), b.decode(errors="replace")

def csrf(body):
    m = re.search(r'name="csrf" value="([^"]+)"', body)
    return m.group(1) if m else ""

def flash(body):
    m = re.search(r'class="flash (\w+)"[^>]*>(.*?)</div>', body, re.S)
    return (m.group(1), re.sub(r"<[^>]+>", "", m.group(2)).strip()) if m else (None, "")

TOK = {"v": ""}
def refresh_token():
    st, b, _ = page("/")
    TOK["v"] = csrf(b)
    return TOK["v"]

def job_id(loc):
    m = re.search(r"/jobs/([0-9A-Z]{26})", loc)
    return m.group(1) if m else ""

def wait_job(jid, timeout=300):
    end = time.time() + timeout
    while time.time() < end:
        st, b, _ = page(f"/jobs/{jid}")
        m = re.search(r'data-job-state="(\w+)"', b)
        if m and m.group(1) in ("succeeded", "failed", "canceled", "interrupted"):
            return m.group(1), b
        time.sleep(0.5)
    return "timeout", ""

# ---------- simulated Resolve ----------
def psql(user, pw, db, sql, host="127.0.0.1"):
    env = dict(os.environ, PGPASSWORD=pw, PGCONNECT_TIMEOUT="5")
    r = subprocess.run(["psql", "-X", "-h", host, "-p", str(PGPORT), "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1", "-Atc", sql],
                       env=env, capture_output=True, text=True, timeout=600)
    return r.returncode, (r.stdout + r.stderr).strip()

def idle_session(user, pw, db):
    env = dict(os.environ, PGPASSWORD=pw)
    p = subprocess.Popen(["psql", "-X", "-q", "-h", "127.0.0.1", "-p", str(PGPORT), "-U", user, "-d", db], env=env,
                         stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(1)
    return p

def superuser(sql, db="postgres"):
    pw = open(f"{BASE_DIR}/pg/sqsolve/superuser_password").read().strip()
    env = dict(os.environ, PGPASSWORD=pw)
    r = subprocess.run(["psql", "-X", "-h", SOCK, "-p", str(PGPORT), "-U", "postgres", "-d", db, "-Atc", sql], env=env, capture_output=True, text=True)
    return (r.stdout + r.stderr).strip()

RESOLVE_SCHEMA = """
CREATE TABLE "SM_Project"(id serial primary key, name text not null, blob bytea);
CREATE TABLE "SM_Timeline"(id serial primary key, project int references "SM_Project"(id), payload bytea);
CREATE TABLE "BtVideoInfo"(id bigserial primary key, clip text, meta jsonb);
CREATE TABLE "Sm2TiItem"(id bigserial primary key, data bytea);
"""
def resolve_create(user, pw, rows_mb=0):
    rc, out = psql(user, pw, "postgres", f"CREATE DATABASE {user}")
    if rc != 0:
        return rc, out
    sql = RESOLVE_SCHEMA + "".join(f'CREATE TABLE "SmAux{i:03d}"(id serial primary key, v text);' for i in range(120))
    sql += """INSERT INTO "SM_Project"(name, blob) SELECT 'Project '||g, convert_to(repeat(md5(g::text),40),'UTF8') FROM generate_series(1,60) g;
INSERT INTO "SM_Timeline"(project, payload) SELECT 1+(g%60), convert_to(md5(random()::text),'UTF8') FROM generate_series(1,600) g;
INSERT INTO "BtVideoInfo"(clip, meta) SELECT 'A00'||g||'.braw', jsonb_build_object('fps',24,'n',g) FROM generate_series(1,3000) g;"""
    if rows_mb:
        n = rows_mb * 100
        sql += f"""INSERT INTO "Sm2TiItem"(data) SELECT convert_to((SELECT string_agg(md5(random()::text||g||i),'') FROM generate_series(1,320) i),'UTF8') FROM generate_series(1,{n}) g;"""
    return psql(user, pw, user, sql)

def reveal(libpath):
    st, loc, b = post(libpath + "/password/reveal", {"csrf": TOK["v"]})
    pw = re.search(r'id="r-pass">([^<]+)<', b)
    user = re.search(r'id="r-user">([^<]+)<', b)
    return (user.group(1) if user else ""), (pw.group(1) if pw else ""), b

def create_library(name, slug=""):
    st, loc, b = post("/libraries/new", {"csrf": TOK["v"], "name": name, "slug": slug, "strategy": "resolve", "notes": ""})
    lp = loc.split("?")[0]
    for _ in range(100):
        st, b, _ = page(lp)
        m = re.search(r'data-state="(\w+)"', b)
        if m and m.group(1) != "provisioning":
            break
        time.sleep(0.2)
    return lp

def wait_active(libpath, timeout=60):
    end = time.time() + timeout
    while time.time() < end:
        st, b, _ = page(libpath)
        if 'data-state="active"' in b:
            return True
        time.sleep(1)
    return False

# ---------- scenarios ----------
def fresh():
    for p in list(procs):
        stop_sq() if p == "sq" else stop_pg()
    shutil.rmtree(BASE_DIR, ignore_errors=True)
    os.makedirs(BASE_DIR)
    r = subprocess.run(["go", "build", "-o", BIN, "./cmd/sqsolve"], cwd=REPO, capture_output=True, text=True)
    if not check("builds", r.returncode == 0, r.stderr):
        sys.exit(1)
    rc, out = cli("init")
    check("init on empty volumes", rc == 0, out)
    rc2, out2 = cli("init")
    check("init is idempotent", rc2 == 0 and "generad" not in out2, out2)
    pwfile = f"{BASE_DIR}/pg/sqsolve/superuser_password"
    r = subprocess.run(["initdb", "-D", ENV["SQSOLVE_PG_DATA_DIR"], "-U", "postgres", f"--pwfile={pwfile}", "--auth=scram-sha-256",
                        "--encoding=UTF8", "--locale-provider=builtin", "--builtin-locale=C.UTF-8"], capture_output=True, text=True)
    check("initdb like the official image", r.returncode == 0, r.stderr[-400:])
    check("PostgreSQL starts with the generated configuration", start_pg(), open(f"{BASE_DIR}/postgres.log").read()[-800:])
    check("sqsolve starts and is ready", start_sq(), open(f"{BASE_DIR}/sqsolve.log").read()[-1500:])
    m = re.search(r"installation code:\s+([A-Z0-9]{5}-[A-Z0-9]{5})", open(f"{BASE_DIR}/sqsolve.log").read())
    check("the log shows the installation code", m is not None)
    return m.group(1) if m else ""

def s_setup(code):
    st, b, _ = page("/")
    check("without an administrator it redirects to the setup wizard", "/setup" in _ or "Setup" in b, _)
    pre = csrf(b)
    form = {"csrf": pre, "code": "AAAAA-BBBBB", "username": "diego", "password": "tungsten light 3200K", "password2": "tungsten light 3200K",
            "host": "127.0.0.1", "name": "Studio", "preset": "daily"}
    st, loc, _ = post("/setup", form)
    st, b, _ = page(loc)
    k, msg = flash(b)
    check("a wrong code shows an error and does not install", k == "error" and "error" not in loc, f"{loc} {k} {msg}")
    form["code"] = code
    st, loc, _ = post("/setup", form)
    check("setup with the right code", loc == "/setup/kit", loc)
    st, b, _ = page("/setup/kit")
    check("recovery kit visible", st == 200 and "recovery" in b.lower(), str(st))
    refresh_token()

def s_library_flow():
    lp = create_library("Wedding Ana & Luis 💍 2026")
    check("library with a Unicode name", lp.startswith("/libraries/"), lp)
    st, b, _ = page(lp)
    slug = re.search(r'<span class="mono">([a-z0-9_]+)</span>', b)
    log("suggested identifier: " + (slug.group(1) if slug else "?"))
    user, pw, rb = reveal(lp)
    check("the password is shown (recent session)", bool(user and pw), rb[:300])
    rc, out = resolve_create(user, pw, rows_mb=60)
    check("Resolve (simulated) creates the database and its tables", rc == 0, out[-300:])
    check("the library activates by itself", wait_active(lp, 90))
    rc, out = psql(user, pw, "postgres", f"CREATE DATABASE {user}_otra")
    check("after activation, the role can no longer create databases", rc != 0 and "permission denied" in out, out)
    rc, out = psql(user, pw, user, 'SELECT count(*) FROM "SM_Project"')
    check("the workstation can still enter its library", rc == 0 and out == "60", out)
    return lp, user, pw

def s_isolation(lp1, user1, pw1):
    lp2 = create_library("Commercial Café")
    user2, pw2, _ = reveal(lp2)
    rc, out = resolve_create(user2, pw2)
    wait_active(lp2, 90)
    rc, out = psql(user2, pw2, user1, "SELECT 1")
    check("a library cannot enter another one", rc != 0, out)
    rc, out = psql(user1, "wrong-password", user1, "SELECT 1")
    check("wrong password rejected", rc != 0 and "password authentication failed" in out, out)
    rc, out = psql("sqsolve_admin", "x", "postgres", "SELECT 1")
    check("sqsolve_admin cannot enter over the network", rc != 0, out)
    rc, out = psql("postgres", "x", "postgres", "SELECT 1")
    check("postgres cannot enter over the network", rc != 0, out)
    return lp2, user2, pw2

def s_backup_under_load(lp, user, pw):
    writer = subprocess.Popen(["psql", "-X", "-q", "-h", "127.0.0.1", "-p", str(PGPORT), "-U", user, "-d", user, "-c",
                               "DO $$ BEGIN FOR i IN 1..400 LOOP INSERT INTO \"BtVideoInfo\"(clip, meta) VALUES ('live'||i, '{}'); PERFORM pg_sleep(0.02); COMMIT; END LOOP; END $$;"],
                              env=dict(os.environ, PGPASSWORD=pw), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    st, loc, _ = post(lp + "/backup", {"csrf": TOK["v"]})
    state, b = wait_job(job_id(loc), 300)
    writer.wait(60)
    check("backup with a workstation writing", state == "succeeded", state)
    st, b, _ = page(lp + "?tab=backups")
    bid = re.search(r'href="/backups/([0-9A-Z]{26})"', b)
    bid = bid.group(1) if bid else ""
    st, loc, _ = post(f"/backups/{bid}/verify", {"csrf": TOK["v"]})
    state, b = wait_job(job_id(loc), 300)
    check("test restore (L2) of the backup taken under load", state == "succeeded", re.sub(r"<[^>]+>", " ", b)[-600:])
    leftovers = superuser("SELECT string_agg(datname, ',') FROM pg_database WHERE datname LIKE 'sqsolve_verify_%'")
    check("verification leaves no temporary databases", leftovers == "", leftovers)
    return bid

def s_restore_replace(lp, user, pw, bid):
    psql(user, pw, user, 'INSERT INTO "SM_Project"(name) VALUES (\'after the backup\')')
    sess = idle_session(user, pw, user)
    st, loc, b = post(f"/backups/{bid}/restore", {"csrf": TOK["v"], "mode": "replace", "confirm": "not-it", "terminate": ""})
    st, b, _ = page(loc)
    k, msg = flash(b)
    check("replacing requires typing the identifier", k == "error", f"{k} {msg}")
    slug = re.search(r"<code>([a-z0-9_]+)</code>", b)
    st, loc, b = post(f"/backups/{bid}/restore", {"csrf": TOK["v"], "mode": "replace", "confirm": user, "terminate": "on"})
    state, jb = wait_job(job_id(loc), 400)
    check("replace with a workstation connected (closing sessions)", state == "succeeded", re.sub(r"<[^>]+>", " ", jb)[-800:])
    sess.poll()
    rc, out = psql(user, pw, user, "SELECT count(*) FROM \"SM_Project\" WHERE name = 'after the backup'")
    check("the library is as it was in the backup", rc == 0 and out == "0", out)
    rc, out = psql(user, pw, user, 'SELECT count(*) FROM "SmAux119"')
    check("the 124 tables are still there", rc == 0, out)
    prev = superuser(f"SELECT string_agg(datname, ',') FROM pg_database WHERE datname LIKE '{user}__prev_%'")
    check("the previous state is kept to revert", prev != "", prev)
    # Second replace must not leave the first previous state behind.
    time.sleep(61)
    st, loc, b = post(f"/backups/{bid}/restore", {"csrf": TOK["v"], "mode": "replace", "confirm": user, "terminate": "on"})
    state, jb = wait_job(job_id(loc), 400)
    prev2 = superuser(f"SELECT string_agg(datname, ',') FROM pg_database WHERE datname LIKE '{user}__prev_%'")
    check("a second replace leaves no orphaned previous states", state == "succeeded" and prev2 and "," not in prev2, f"{state} {prev2}")
    st, b, _ = page(lp + "/revert")
    st, loc, b = post(lp + "/revert", {"csrf": TOK["v"], "confirm": user})
    state, jb = wait_job(job_id(loc), 400)
    check("revert the last replace", state == "succeeded", re.sub(r"<[^>]+>", " ", jb)[-600:])
    try:
        sess.stdin.close(); sess.wait(5)
    except Exception:
        pass

def s_kill_during_backup(lp, user, pw):
    st, loc, _ = post(lp + "/backup", {"csrf": TOK["v"]})
    jid = job_id(loc)
    for _ in range(100):
        st, b, _ = page(f"/jobs/{jid}")
        if 'data-job-state="running"' in b:
            break
        time.sleep(0.1)
    time.sleep(0.4)
    stop_sq(signal.SIGKILL)
    partials = [os.path.join(d, f) for d, _, fs in os.walk(f"{BASE_DIR}/backups/libraries") for f in fs if f.endswith(".partial")]
    log(f".partial files after killing sqsolve: {len(partials)}")
    check("sqsolve starts again after dying mid-job", start_sq(), open(f"{BASE_DIR}/sqsolve.log").read()[-1500:])
    time.sleep(2)
    refresh_token()
    st, b, _ = page(f"/jobs/{jid}")
    m = re.search(r'data-job-state="(\w+)"', b)
    check("the interrupted job is marked as such", m and m.group(1) in ("interrupted", "failed"), m.group(1) if m else b[:200])
    partials = [os.path.join(d, f) for d, _, fs in os.walk(f"{BASE_DIR}/backups/libraries") for f in fs if f.endswith(".partial")]
    check("no .partial files remain after reconciling", not partials, str(partials))
    ghosts = superuser("SELECT count(*) FROM pg_stat_activity WHERE application_name = 'pg_dump'")
    check("no pg_dump outlives sqsolve", ghosts == "0", ghosts)
    st, loc, _ = post(lp + "/backup", {"csrf": TOK["v"]})
    state, _ = wait_job(job_id(loc), 300)
    check("the next backup works", state == "succeeded", state)

def s_pg_down(lp):
    st, loc, _ = post(lp + "/backup", {"csrf": TOK["v"]})
    jid = job_id(loc)
    for _ in range(100):
        st, b, _ = page(f"/jobs/{jid}")
        if 'data-job-state="running"' in b:
            break
        time.sleep(0.1)
    time.sleep(0.3)
    stop_pg(signal.SIGINT)
    state, b = wait_job(jid, 120)
    detail = re.sub(r"<[^>]+>", " ", b)
    check("with PostgreSQL down the backup fails and explains why", state in ("failed", "succeeded"), state)
    log("state: " + state)
    st, h, _ = get_raw("/readyz")
    check("readyz answers 503 without PostgreSQL", st == 503, str(st))
    st, b, _ = page("/")
    check("the panel opens even if PostgreSQL is down", st == 200, str(st))
    st, b, _ = page("/health")
    check("Health says PostgreSQL is not responding", st == 200 and "Not responding" in b, re.sub(r"<[^>]+>", " ", b)[:400])
    start_pg()
    ok = False
    for _ in range(60):
        if get_raw("/readyz")[0] == 200:
            ok = True; break
        time.sleep(1)
    check("recovers by itself when PostgreSQL returns", ok)
    st, loc, _ = post(lp + "/backup", {"csrf": TOK["v"]})
    state, b = wait_job(job_id(loc), 300)
    check("backup after PostgreSQL came back", state == "succeeded", re.sub(r"<[^>]+>", " ", b)[-500:])

def s_unwritable_backups(lp, slug):
    d = os.path.join(BASE_DIR, "backups", "libraries", slug, time.strftime("%Y", time.gmtime()), time.strftime("%m", time.gmtime()))
    os.chmod(d, 0o500)
    try:
        st, loc, _ = post(lp + "/backup", {"csrf": TOK["v"]})
        state, b = wait_job(job_id(loc), 120)
        text = re.sub(r"<[^>]+>", " ", b)
        check("without write permission the backup fails with a clear message", state == "failed" and "permission" in text.lower(), text[-500:])
    finally:
        os.chmod(d, 0o700)

def s_network_rules(lp, user, pw):
    st, loc, b = post(lp + "/networks", {"csrf": TOK["v"], "cidrs": "10.9.9.0/24"})
    rc, out = psql(user, pw, user, "SELECT 1")
    check("a network rule blocks other addresses", rc != 0, out)
    st, loc, b = post(lp + "/networks", {"csrf": TOK["v"], "cidrs": ""})
    rc, out = psql(user, pw, user, "SELECT 1")
    check("removing the rule restores access", rc == 0, out)
    st, loc, b = post(lp + "/networks", {"csrf": TOK["v"], "cidrs": "300.1.1.1/8"})
    st, b, _ = page(loc)
    check("an invalid network is rejected with a message", flash(b)[0] == "error", str(flash(b)))

def s_members(lp, user, pw):
    st, loc, b = post(lp + "/people", {"csrf": TOK["v"], "person": "maria"})
    st, b, _ = page(loc)
    check("per-person credential created", flash(b)[0] == "ok", str(flash(b)))
    cid = re.search(r'name="cred" value="([0-9A-Z]{26})"[^>]*>\s*<button[^>]*>Reveal', b) or re.findall(r'name="cred" value="([0-9A-Z]{26})"', b)
    creds = re.findall(r'name="cred" value="([0-9A-Z]{26})"', b)
    member_pw = ""
    for c in creds:
        st2, loc2, rb = post(lp + "/password/reveal", {"csrf": TOK["v"], "cred": c})
        u = re.search(r'id="r-user">([^<]+)<', rb)
        p = re.search(r'id="r-pass">([^<]+)<', rb)
        if u and u.group(1).endswith("__maria"):
            member_pw = p.group(1)
            break
    rc, out = psql(f"{user}__maria", member_pw, user, "CREATE TABLE \"SmNuevaDeMaria\"(id int); SELECT tableowner FROM pg_tables WHERE tablename='SmNuevaDeMaria'")
    check("the person connects and what they create belongs to the library", rc == 0 and out.endswith(user), out)

def s_import(lp, bid):
    st, h, data = raw("GET", f"/backups/{bid}/download")
    check("backup download", st == 200 and data[:5] == b"PGDMP", f"{st} {data[:10]}")
    st, b, _ = page("/backups/import?library=" + lp.rsplit("/", 1)[1])
    tok = csrf(b)
    libid = lp.rsplit("/", 1)[1]
    st, loc, _ = post_multipart("/backups/import", [("csrf", tok), ("library", libid)], [("upload", "uploaded.backup", data)])
    state, jb = wait_job(job_id(loc), 300)
    check("import by uploading the file from the panel", state == "succeeded", f"{loc} " + re.sub(r"<[^>]+>", " ", jb)[-400:])
    left = os.listdir(f"{BASE_DIR}/backups/_import")
    check("the upload is not left duplicated in _import", left == [], str(left))
    st, loc, _ = post_multipart("/backups/import", [("library", libid), ("csrf", tok)], [("upload", "x.backup", b"PGDMP")])
    st2, b2, _ = page("/")
    check("multipart without the CSRF part first is rejected", st == 403, str(st))
    shutil.copy(f"{BASE_DIR}/backups/" + [os.path.relpath(os.path.join(d, f), f"{BASE_DIR}/backups") for d, _, fs in os.walk(f"{BASE_DIR}/backups/libraries") for f in fs if f.endswith(".backup")][0],
                f"{BASE_DIR}/backups/_import/copied.backup")
    st, loc, _ = post_multipart("/backups/import", [("csrf", tok), ("library", libid), ("file", "copied.backup"), ("delete", "on")], [("upload", "", b"")])
    state, jb = wait_job(job_id(loc), 300)
    check("import a file copied to _import (and move it)", state == "succeeded" and not os.path.exists(f"{BASE_DIR}/backups/_import/copied.backup"), state)

def s_cli():
    rc, out = cli("doctor")
    check("doctor without failures", rc == 0, out)
    rc, out = cli("library", "list")
    check("library list", rc == 0 and "ACTIVE" in out.upper(), out)
    rc, out = cli("audit", "verify")
    check("audit log intact", rc == 0, out)
    rc, out = cli("admin", "create", "operator1", "--role", "operator", stdin="another long password 1234\n")
    check("admin create from the console", rc == 0, out)
    rc, out = cli("backup", "create", "commercial_cafe", "--wait")
    check("backup create --wait from the console", rc == 0, out)

def s_restart_all():
    stop_sq(); stop_pg()
    check("PostgreSQL starts again", start_pg())
    check("sqsolve starts again", start_sq())
    st, b, path = page("/")
    check("the session is still valid after restart", st == 200 and "/login" not in path, path)
    refresh_token()

def s_pages():
    bad = []
    st, b, _ = page("/libraries")
    libs = set(re.findall(r'href="(/libraries/[0-9A-Z]{26})"', b))
    paths = ["/", "/libraries", "/libraries?view=trash", "/libraries/new", "/backups", "/backups/import", "/jobs", "/activity",
             "/activity?tab=audit", "/health", "/settings", "/settings?tab=users", "/settings?tab=notifications", "/settings?tab=maintenance",
             "/settings?tab=about", "/account"]
    for l in libs:
        paths += [l + "?tab=" + t for t in ["connection", "backups", "schedule", "access", "activity", "settings", "danger"]]
        paths += [l + "/instructions"]
    for p in paths:
        st, b, _ = page(p)
        if st != 200 or "SQS-X99" in b:
            bad.append(f"{p}:{st}")
    check(f"{len(paths)} pages answer 200", not bad, ", ".join(bad))

def s_delete_purge(lp, user):
    st, b, _ = page(lp + "/delete")
    st, loc, _ = post(lp + "/delete", {"csrf": TOK["v"], "confirm": user})
    state, jb = wait_job(job_id(loc), 300)
    check("send to the trash (with a final backup)", state == "succeeded", re.sub(r"<[^>]+>", " ", jb)[-500:])
    st, loc, _ = post(lp + "/untrash", {"csrf": TOK["v"]})
    state, jb = wait_job(job_id(loc), 300)
    check("take out of the trash", state == "succeeded", re.sub(r"<[^>]+>", " ", jb)[-500:])
    st, loc, _ = post(lp + "/delete", {"csrf": TOK["v"], "confirm": user})
    wait_job(job_id(loc), 300)
    st, loc, _ = post(lp + "/purge", {"csrf": TOK["v"], "confirm": user})
    state, jb = wait_job(job_id(loc), 300)
    check("purgar definitivamente", state == "succeeded", re.sub(r"<[^>]+>", " ", jb)[-500:])
    left = superuser(f"SELECT count(*) FROM pg_database WHERE datname LIKE '{user}%'") + "/" + superuser(f"SELECT count(*) FROM pg_roles WHERE rolname LIKE '{user}%'")
    check("no databases or roles remain of the purged library", left == "0/0", left)

def guarded(fn, *args):
    try:
        return fn(*args)
    except Exception as e:
        import traceback
        check(f"{fn.__name__} finished without an exception", False, traceback.format_exc()[-600:])

def main():
    code = fresh()
    s_setup(code)
    lp, user, pw = s_library_flow()
    if not pw:
        print("no credentials; stopping"); return
    lp2, user2, pw2 = s_isolation(lp, user, pw)
    bid = guarded(s_backup_under_load, lp, user, pw)
    guarded(s_members, lp, user, pw)
    guarded(s_network_rules, lp, user, pw)
    if bid:
        guarded(s_import, lp, bid)
        guarded(s_restore_replace, lp, user, pw, bid)
    guarded(s_kill_during_backup, lp, user, pw)
    guarded(s_pg_down, lp)
    guarded(s_unwritable_backups, lp, user)
    guarded(s_cli)
    guarded(s_restart_all)
    guarded(s_pages)
    guarded(s_delete_purge, lp2, user2)
    stop_sq(); stop_pg()
    fails = [r for r in RESULTS if not r[1]]
    print(f"\n{len(RESULTS) - len(fails)}/{len(RESULTS)} passed")
    for n, _, d in fails:
        print(" -", n, "|", d[:300].replace("\n", " "))
    if fails:
        for name in ("sqsolve.log", "postgres.log"):
            print(f"\n--- {name} (final)")
            print(open(os.path.join(BASE_DIR, name)).read()[-3000:])
        sys.exit(1)

if __name__ == "__main__":
    try:
        main()
    finally:
        shutil.rmtree(SOCK, ignore_errors=True)
        for p in list(procs.values()):
            try:
                p.terminate(); p.wait(30)
            except Exception:
                pass
