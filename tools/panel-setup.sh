#!/usr/bin/env bash
# Recreate the live test panel in /tmp.
#
# Separate from setup.sh because it is only needed when something has to be
# looked at in a browser, and because its config mirrors the operator's real
# server — three domains, Reality owning 443/2053/8443, an HTTP fallback on
# 6038 — so that what is tested here behaves like what is deployed there.
#
# Everything lands in /tmp on purpose: it is throwaway state, and keeping it
# out of the home directory means a stale test config can never be mistaken
# for the real /etc/nginx-panel/config.json.

set -uo pipefail
P=/tmp/panel
mkdir -p "$P"/{fake,sites,templates}

# ── the launcher ──
# Every SHAHRAG_* variable here is a test-only override. They exist so the
# panel can run without touching /etc or /var on this machine.
cat > "$P/start.sh" <<'LAUNCH'
#!/bin/bash
exec env \
 SHAHRAG_CONFIG=/tmp/panel/config.json \
 SHAHRAG_BANS_FILE=/tmp/panel/bans.json \
 SHAHRAG_BAN_LOG=/tmp/panel/bans.log \
 SHAHRAG_HONEYPOT_LOG=/tmp/panel/honeypot.log \
 SHAHRAG_ACCESS_LOG=/tmp/panel/access.log \
 SHAHRAG_STATS_FILE=/tmp/panel/stats.json \
 SHAHRAG_TEMPLATE_DIR=/tmp/panel/templates \
 SHAHRAG_NGINX_CONF=/tmp/panel/nginx.conf \
 SHAHRAG_HOST=0.0.0.0 \
 /tmp/shahrag serve
LAUNCH
chmod +x "$P/start.sh"

# ── the configuration ──
python3 - <<'PY'
import json, hashlib, binascii, os
salt = binascii.unhexlify("a9acc2d0c67069392c300f77fc5487b1")
pw = hashlib.pbkdf2_hmac("sha256", b"secret123", salt, 200000).hex()
cfg = {
 "domains": {d: {"cert": "/tmp/panel/c.pem", "key": "/tmp/panel/k.pem"}
             for d in ["sugerdood.com", "freeline.dpdns.org", "kannb.sugerdood.com"]},
 "services": {
   "xray":    {"local_port": 4628,  "listen_port": 0, "path": "/worldx",
               "bindings": [{"domain": "sugerdood.com", "subdomain": "vpn"}]},
   "sub":     {"local_port": 2096,  "listen_port": 0, "path": "/sub",
               "bindings": [{"domain": "sugerdood.com", "subdomain": "sub"}]},
   "adguard": {"local_port": 3000,  "listen_port": 0, "path": "/dns",
               "bindings": [{"domain": "sugerdood.com", "subdomain": "dns"}]},
   "panel":   {"local_port": 42159, "listen_port": 0, "path": "/Xp3IYReUB55CmT4J9RwS1t",
               "bindings": [{"domain": "kannb.sugerdood.com", "subdomain": ""}]},
 },
 "listen_ports": [80, 443, 2053, 8443],
 "fake_site": {"mode": "default"},
 # Reality owns 443 here exactly as it does on the real server: that is the
 # configuration in which the r54 decoy-domain bug only appeared.
 "reality": {"enabled": True, "http_port": 6038, "resolvers": ["1.1.1.1", "8.8.8.8"],
   "services": {
     "SugerDoodR": {"sni": "cdn.example.com", "local_port": 49026, "ports": [2053, 443]},
     "Test2":      {"sni": "t2.example.com",  "local_port": 40956, "ports": [8443]},
     "Test3":      {"sni": "t3.example.com",  "local_port": 49631, "ports": [443]},
     "pass":       {"sni": "p.example.com",   "local_port": 0, "ports": [443],
                    "target": "$passthrough"}}},
 "nginx_settings": {"cache_enabled": False, "worker_connections": 0},
 "nginx": {"output_path": "/tmp/panel/gateway.conf",
           "stream_output_path": "/tmp/panel/stream.conf",
           "ssl_protocols": "TLSv1.2 TLSv1.3", "ssl_ciphers": "HIGH:!aNULL:!MD5",
           "fake_dir": "/tmp/panel/fake"},
 "real_sites": {"sites_dir": "/tmp/panel/sites"},
 "shahrag": {
   "panel": {"enabled": False, "local_port": 42199, "path": "testpanel",
             "service_name": "Shahrag", "installed": True},
   "auth": {"username": "admin", "password_hash": "pbkdf2_sha256$200000$"+salt.hex()+"$"+pw,
            "session_secret": "test-secret-value-abcdefgh",
            "allowed_ips": [], "ip_whitelist_enabled": False},
   "ui": {"theme": "midnight", "language": "fa"},
   "security": {"rate_limit_enabled": False, "rate_limit_per_minute": 100000,
                "session_timeout_minutes": 0, "csrf_enabled": False, "lock_minutes": 60},
   "acme": {}, "last_backup": "0001-01-01T00:00:00Z"},
}
json.dump(cfg, open("/tmp/panel/config.json", "w"), indent=1)
print("   config written")
PY

# ── sample traffic, so the statistics pages are not empty ──
python3 - <<'PY'
import random
paths = ["/worldx/525e5773", "/sub/abc", "/dns/",
         "/Xp3IYReUB55CmT4J9RwS1t/api/health", "/wp-json/batch/v1"]
codes = [200]*40 + [204, 301, 404, 404, 502]
with open("/tmp/panel/access.log", "w") as f:
    for _ in range(3000):
        f.write('%s - - [21/Sep/2026:20:00:00 +0000] "GET %s HTTP/1.1" %d %d "-" "curl/8"\n' % (
            "10.0.%d.%d" % (random.randint(0, 4), random.randint(1, 254)),
            random.choice(paths), random.choice(codes), random.randint(200, 40000)))
print("   3000 access lines")

# An error log containing the RAW BYTES that produced the unreadable text the
# operator photographed. Kept byte for byte so the r55 sanitiser is exercised
# against the real thing rather than a sanitised imitation.
lines = [
 b'2026/09/17 05:37:05 [error] 2822582#2822582: *9382430 SSL_do_handshake() failed '
 b'(SSL: error:0A00006C:SSL routines::bad key share) while SSL handshaking, '
 b'client: 127.0.0.1, server: 0.0.0.0:6038\n',
 b'2026/09/17 05:54:26 [error] 2822582#2822582: *9397796 broken header: '
 b'"\x16\x03\x01}L\x00S\x00_,$^\x00B\x00\x00P\x00\x00d\x00DMxt\x00\x00uh a:\x00\x00'
 b'\x006\x00\x00O\x00x\x00\x00Q\x00\x00A&/@0@+@," while reading PROXY protocol, '
 b'client: 85.217.149.1, server: 0.0.0.0:6038\n',
 b'2026/09/17 06:12:26 [error] 2822582#2822582: *9411496 broken header: '
 b'"\x16\x03\x01]\x9aM\\&\xaa\x07@<t\x00\x1b;"g8ES i8\x00L)\x00&\x00 \x00w_K;\x00PV3'
 b'\x00\x00\x00p\x000?\x00f" while reading PROXY protocol, '
 b'client: 85.217.149.28, server: 0.0.0.0:6038\n',
]
for i in range(6):
    lines.append((
      '2026/09/17 07:%02d:11 [error] 2822582#2822582: *94115%02d upstream timed out '
      '(110: Connection timed out) while connecting to upstream, client: 10.0.0.%d, '
      'server: sub.sugerdood.com, request: "GET /sub/x HTTP/1.1", '
      'upstream: "http://127.0.0.1:2096/x", host: "sub.sugerdood.com"\n'
      % (i*7, i, i+2)).encode())
open("/tmp/panel/error.log", "wb").writelines(lines)
print("   error log with real raw-byte lines")
PY

# The logs page reads nginx's real paths, so the fixture is copied there.
if sudo -n true 2>/dev/null; then
  sudo -n mkdir -p /var/log/nginx
  sudo -n cp /tmp/panel/error.log /var/log/nginx/error.log
  sudo -n cp /tmp/panel/access.log /var/log/nginx/access.log
  sudo -n chmod 644 /var/log/nginx/error.log /var/log/nginx/access.log
  echo "   installed into /var/log/nginx"
fi

echo "   panel ready — admin / secret123 at /testpanel/"
