#!/usr/bin/env python3
"""Point the modules imported from odigos-enterprise at this repo's modules by local path.

1. Renames github.com/odigos-io/odigos-enterprise/... module paths to their new place in odigos.
2. In every imported go.mod, requires odigos modules at the placeholder version and replaces them with
   relative paths (the convention the other odigos modules use).
3. Does the same for collector/enterprise/builder-config.yaml.
"""
import os
import re
import subprocess
import sys

ROOT = sys.argv[1]
IMPORTED = ["ui", "insights", "instrumentor/enterprise", "odiglet/enterprise", "collector/enterprise",
            "tests/enterprise", "scripts/enterprise", "tests-infrastructure"]
PLACEHOLDER = "v0.0.0-00010101000000-000000000000"
RENAMES = [
    ("github.com/odigos-io/odigos-enterprise/collector/", "github.com/odigos-io/odigos/collector/enterprise/"),
    ("github.com/odigos-io/odigos-enterprise/instrumentor", "github.com/odigos-io/odigos/instrumentor/enterprise"),
    ("github.com/odigos-io/odigos-enterprise/odiglet", "github.com/odigos-io/odigos/odiglet/enterprise"),
    ("github.com/odigos-io/odigos-enterprise/insights", "github.com/odigos-io/odigos/insights"),
    ("github.com/odigos-io/odigos-enterprise/ui", "github.com/odigos-io/odigos/ui"),
]
BOUNDARY = r'(?=[/"\s`)]|$)'
EDITABLE = re.compile(r'(\.go|/go\.mod|\.ya?ml)$')


def tracked(paths):
    out = subprocess.run(["git", "-C", ROOT, "ls-files", "--"] + paths, capture_output=True, text=True, check=True)
    return [p for p in out.stdout.splitlines() if EDITABLE.search(p) and "/vendor/" not in p]


def rename_paths():
    for rel in tracked(IMPORTED):
        p = os.path.join(ROOT, rel)
        try:
            s = open(p).read()
        except UnicodeDecodeError:
            continue
        n = s
        for old, new in RENAMES:
            n = re.sub(re.escape(old) + ("" if old.endswith("/") else BOUNDARY), new, n)
        if n != s:
            open(p, "w").write(n)


def module_dirs():
    mods = {}
    out = subprocess.run(["git", "-C", ROOT, "ls-files", "*go.mod"], capture_output=True, text=True, check=True)
    for rel in out.stdout.splitlines():
        if "/licenses/" in rel:
            continue
        first = open(os.path.join(ROOT, rel)).read().split("\n", 1)[0]
        m = re.match(r"module\s+(\S+)", first)
        if m and m.group(1).startswith("github.com/odigos-io/odigos/"):
            mods[m.group(1)] = os.path.dirname(rel)
    return mods


def relink_gomod(rel, mods):
    p = os.path.join(ROOT, rel)
    lines = open(p).read().split("\n")
    here = os.path.dirname(rel)
    self_mod = re.match(r"module\s+(\S+)", lines[0]).group(1)
    needed = set()
    out = []
    for line in lines:
        if re.match(r"^replace\s+github\.com/odigos-io/odigos/\S+\s+=>", line):
            continue
        m = re.match(r"^(\s*(?:require\s+)?)(github\.com/odigos-io/odigos/\S+)\s+v\S+(.*)$", line)
        if m and m.group(2) in mods and m.group(2) != self_mod:
            needed.add(m.group(2))
            line = f"{m.group(1)}{m.group(2)} {PLACEHOLDER}{m.group(3)}"
        out.append(line)
    while out and out[-1] == "":
        out.pop()
    for mod in sorted(needed):
        out += ["", f"replace {mod} => {os.path.relpath(mods[mod], here)}"]
    open(p, "w").write("\n".join(out) + "\n")


def relink_builder_config(mods):
    rel = "collector/enterprise/builder-config.yaml"
    p = os.path.join(ROOT, rel)
    s = open(p).read()
    dist_dir = "collector/enterprise/odigosotelcol"

    def repl(m):
        mod = m.group(2)
        target = mods.get(mod)
        suffix = mod[len("github.com/odigos-io/odigos/"):]
        if target is None and os.path.isfile(os.path.join(ROOT, suffix, "go.mod")):
            target = suffix
        if target is None:
            return m.group(0)
        return f"{m.group(1)}{os.path.relpath(target, dist_dir)}"

    s = re.sub(r"^(\s*-\s*\S+\s*=>\s*)(github\.com/odigos-io/odigos/\S+)\s+v\S+\s*$", repl, s, flags=re.M)
    open(p, "w").write(s)


if __name__ == "__main__":
    rename_paths()
    mods = module_dirs()
    out = subprocess.run(["git", "-C", ROOT, "ls-files", "--"] + IMPORTED, capture_output=True, text=True, check=True)
    for rel in out.stdout.splitlines():
        if rel.endswith("/go.mod") and "/licenses/" not in rel and not rel.startswith("collector/enterprise/odigosotelcol/"):
            relink_gomod(rel, mods)
    relink_builder_config(mods)
