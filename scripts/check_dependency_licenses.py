#!/usr/bin/env python3
"""Require the reviewed license inventory to match the locked module graph."""
import json
import pathlib
import re
import subprocess
import sys


def main():
    expected = {}
    for row in pathlib.Path("docs/dependencies.md").read_text().splitlines():
        if not row.startswith("| "):
            continue
        fields = [part.strip() for part in row.split("|")[1:-1]]
        if len(fields) == 4 and fields[1].startswith("v"):
            expected[fields[0]] = fields[1]
    raw = subprocess.check_output(["go", "list", "-m", "-json", "all"]).decode()
    actual = {}
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        module, end = decoder.raw_decode(raw)
        raw = raw[end:]
        if module.get("Main"):
            continue
        if module.get("Replace"):
            raise ValueError("module replacement requires a fresh provenance/license review")
        actual[module["Path"]] = module["Version"]
    if actual != expected:
        for name in sorted(set(actual) | set(expected)):
            if actual.get(name) != expected.get(name):
                print("license inventory mismatch:", name, file=sys.stderr)
        return 1
    notices = pathlib.Path("THIRD_PARTY_NOTICES.md").read_text()
    for name, version in actual.items():
        if f"## {name} {version}\n" not in notices:
            raise ValueError("verbatim third-party notice missing")
    if re.search(r"\|\s*REVIEW\s*\|", pathlib.Path("docs/dependencies.md").read_text()):
        raise ValueError("unreviewed dependency license")
    print(f"PASS: {len(actual)} external module versions match the reviewed license inventory")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"dependency license audit failed: {type(error).__name__}", file=sys.stderr)
        sys.exit(1)
