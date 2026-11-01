#!/usr/bin/env python3
"""Write the licenses of Mermaid's dependencies to mermaid.NOTICES.

Mermaid's browser bundle compiles its dependencies in, and the bundle keeps
few of their notices. Run this after updating mermaid.min.js.gz, on a
directory where mermaid@<version> was installed with --omit=dev:

    scripts/gen-mermaid-notices.py <dir> > internal/termbrowser/mermaid.NOTICES

It lists every installed package, a superset of what the bundle kept.
"""
import glob
import json
import os
import sys

root = sys.argv[1]
paths = glob.glob(root + "/node_modules/**/package.json", recursive=True)
packages = {}
for path in paths:
    try:
        meta = json.load(open(path))
    except ValueError:
        continue
    if "name" in meta and "version" in meta:
        packages[(meta["name"], meta["version"])] = (meta, os.path.dirname(path))

print("These packages are installed with the Mermaid version embedded in Larik.")
print("Its bundle compiles in the parts it uses. Each entry reproduces the")
print("package's license files, or what its package.json declares if it has none.")
for (name, version), (meta, directory) in sorted(packages.items()):
    if name == "mermaid":
        continue  # mermaid.LICENSE
    print("\n---------------- %s %s ----------------\n" % (name, version))
    files = sorted(
        f
        for f in os.listdir(directory)
        if f.upper().startswith(("LICEN", "NOTICE", "COPYING"))
        and os.path.isfile(os.path.join(directory, f))
    )
    for f in files:
        print(open(os.path.join(directory, f), errors="replace").read().rstrip())
    if not files:
        author = meta.get("author")
        if isinstance(author, dict):
            author = author.get("name")
        print("License: %s" % (meta.get("license") or "see the project"))
        if author:
            print("Author: %s" % author)
        if meta.get("homepage"):
            print("Homepage: %s" % meta["homepage"])
