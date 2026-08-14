#!/usr/bin/env bash
# Repair cert/key paths in /etc/nginx-panel/config.json that were
# incorrectly saved without a leading slash, then regenerate nginx
# config and reload.
set -euo pipefail

CONFIG=/etc/nginx-panel/config.json

if [ ! -f "$CONFIG" ]; then
    echo "Config not found at $CONFIG"
    exit 1
fi

# Backup
cp "$CONFIG" "${CONFIG}.bak.$(date +%s)"

# Ensure output paths are set (old configs may have them empty)
python3 - "$CONFIG" <<'PY2'
import json, sys
p=sys.argv[1]
c=json.load(open(p))
n=c.get("nginx",{})
if not n.get("output_path"): n["output_path"]="/etc/nginx/conf.d/gateway.conf"
if not n.get("stream_output_path"): n["stream_output_path"]="/etc/nginx/stream-gateway.conf"
if not n.get("fake_dir"): n["fake_dir"]="/var/www/mysite"
c["nginx"]=n
json.dump(c, open(p,"w"), indent=2)
PY2

# Remove stale broken gateway so nginx can start immediately
rm -f /etc/nginx/conf.d/gateway.conf

# Use python3 to fix every "cert"/"key" string that looks like a
# relative path (doesn't start with /) by prepending /.
python3 - "$CONFIG" <<'PY'
import json, sys
p = sys.argv[1]
with open(p) as f:
    c = json.load(f)
fixed = 0
def fix(v):
    global fixed
    if isinstance(v, str) and v and not v.startswith("/") and (v.endswith(".pem") or "/" in v):
        fixed += 1
        return "/" + v
    return v
for name, d in c.get("domains", {}).items():
    d["cert"] = fix(d.get("cert", ""))
    d["key"]  = fix(d.get("key", ""))
    c["domains"][name] = d
panel = c.get("shahrag", {}).get("panel", {})
panel["cert"] = fix(panel.get("cert", ""))
panel["key"]  = fix(panel.get("key", ""))
c["shahrag"]["panel"] = panel
with open(p, "w") as f:
    json.dump(c, f, indent=2)
print(f"Fixed {fixed} paths.")
PY

echo "Regenerating nginx config..."
shahrag generate

echo "Testing nginx..."
nginx -t

echo "Reloading nginx..."
systemctl reload nginx

echo "Restarting shahrag..."
systemctl restart shahrag

echo "Done."
