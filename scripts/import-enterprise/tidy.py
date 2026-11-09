#!/usr/bin/env python3
"""go mod tidy every imported module, adding a local replace for each odigos module tidy cannot resolve."""
import os
import re
import subprocess
import sys

ROOT = sys.argv[1]
MODS = sys.argv[2:]
env = dict(os.environ, GOWORK="off", GOPRIVATE="github.com/odigos-io/*", GOFLAGS="-mod=mod")


def module_dirs():
    mods = {}
    out = subprocess.run(["git", "-C", ROOT, "ls-files", "*go.mod"], capture_output=True, text=True, check=True)
    for rel in out.stdout.splitlines():
        if "/licenses/" in rel:
            continue
        first = open(os.path.join(ROOT, rel)).read().split("\n", 1)[0]
        m = re.match(r"module\s+(\S+)", first)
        if m:
            mods[m.group(1)] = os.path.dirname(rel)
    return mods


mods = module_dirs()
failed = []
for d in MODS:
    for attempt in range(15):
        r = subprocess.run(["go", "mod", "tidy"], cwd=os.path.join(ROOT, d), env=env, capture_output=True, text=True)
        if r.returncode == 0:
            print(f"ok   {d}")
            break
        missing = set(re.findall(r"(github\.com/odigos-io/odigos/[^@\s:]+)@v0\.0\.0-00010101000000-000000000000", r.stderr))
        missing = {m for m in missing if m in mods}
        gomod = os.path.join(ROOT, d, "go.mod")
        text = open(gomod).read()
        new = [m for m in sorted(missing) if f"replace {m} =>" not in text]
        if not new:
            print(f"FAIL {d}\n{r.stderr[-1500:]}")
            failed.append(d)
            break
        text = text.rstrip("\n") + "\n" + "".join(f"\nreplace {m} => {os.path.relpath(mods[m], d)}\n" for m in new)
        open(gomod, "w").write(text)
sys.exit(1 if failed else 0)
