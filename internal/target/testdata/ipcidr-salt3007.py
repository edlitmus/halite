# Captures internal/target/testdata/ipcidr-salt3007.json: what Salt's own
# ipcidr matcher (the one behind `-S` and `S@`) answers for a grid of
# address grains and target expressions. Issue 614.
#
# It calls salt.matchers.ipcidr_match.match directly with the grains in
# opts, which is what a Salt node does with its own grains when it decides
# whether a published job is for it. Captured 2026-11-05 on macOS with
# Salt 3007.1 from PyPI in a virtualenv on CPython 3.10.22 -- 3.10
# because it is the Python Salt's 3007 onedir ships, and the answers
# depend on it: the matcher compares str() of the parsed target, and
# how CPython prints an IPv4-mapped address has changed between
# releases.
#
#   uv venv --python 3.10 salt3007
#   uv pip install --python salt3007/bin/python salt==3007.1 \
#       looseversion packaging pyyaml jinja2 msgpack distro psutil \
#       requests tornado markupsafe jmespath
#   salt3007/bin/python -I ipcidr-salt3007.py > ipcidr-salt3007.json
#
# "error" is Salt's answer for a target it cannot parse: it logs
# "Invalid IP/CIDR target" and matches nothing.
import json, sys, platform
import salt.version
import salt.matchers.ipcidr_match as m

nodes = {
    "zoned": {"ipv4": ["10.0.0.1"], "ipv6": ["fe80::1%eth0"]},
    "mapped_dotted": {"ipv4": ["10.0.0.1"], "ipv6": ["::ffff:10.0.0.2"]},
    "mapped_hex": {"ipv4": ["10.0.0.1"], "ipv6": ["::ffff:a00:2"]},
    "plain": {"ipv4": ["10.0.0.1"], "ipv6": ["fe80::1", "2001:db8::5"]},
    "mapped_only": {"ipv4": [], "ipv6": ["::ffff:10.0.0.2"]},
    "no_ipv6": {"ipv4": ["10.0.0.1"]},
    # Canonical forms a grain could be spelled in: one zero group (not
    # compressed), and two equal runs of zeros (the first compressed).
    "canonical": {"ipv4": [], "ipv6": ["2001:db8:0:1:1:1:1:1", "2001:db8::1:0:0:1"]},
    "uncompressed": {"ipv4": ["10.0.0.1"], "ipv6": ["2001:0db8:0000:0000:0000:0000:0000:0005"]},
}
exprs = [
    "fe80::/10", "fe80::1", "fe80::1%eth0", "fe80::1%eth1", "fe80::/10%eth0",
    "::ffff:10.0.0.1", "::ffff:10.0.0.2", "::ffff:a00:2", "::ffff:0:0/96",
    "10.0.0.1", "10.0.0.2", "10.0.0.0/24",
    "2001:db8::5", "2001:0db8::0005", "2001:db8::/32",
    "10.0.0.1/24", "fe80::1/10", "::/0", "0.0.0.0/0",
    "2001:db8::1:1:1:1:1", "2001:db8:0:0:1:0:0:1", "2001:db8:0:0:1::1",
]
out = {"salt": salt.version.__version__, "python": platform.python_version(), "results": []}
for nname, grains in nodes.items():
    for e in exprs:
        try:
            r = m.match(e, opts={"grains": grains})
            r = "error" if r == [] else bool(r)
        except Exception as ex:
            r = "raise:" + type(ex).__name__
        out["results"].append({"node": nname, "grains": grains, "expr": e, "match": r})
json.dump(out, sys.stdout, indent=1)
