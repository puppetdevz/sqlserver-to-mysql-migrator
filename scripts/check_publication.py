#!/usr/bin/env python3
"""Audit publishable files and optionally all locally reachable history.

Only paths, line numbers and rule IDs are reported. This is a guardrail, not
proof of publication rights, credential revocation or production correctness.
Ignored private files are never copied into the optional public snapshot.
"""
import argparse
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys


FORBIDDEN_DIRS = {"data", "backups", "reports", "diagnostics", "dist", ".agents", ".claude", ".vscode", ".codegraph", "db-migration"}
FORBIDDEN_NAMES = {"tables.txt", "completed_tables.txt", "failed_tables.txt", "create_failed_tables.txt", "row_count_mismatch_tables.txt", "slow_tables.txt", "local-db.env"}
SIGNATURE = re.compile(r"-----BEGIN " + r"(?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----|\bAKIA[0-9A-Z]{16}\b|\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}")
PASSWORD = re.compile(r"(?im)^\s*(?:password|DB_PASS|DB_PASSWORD|MYSQL_PWD)\s*[:=]\s*(.*?)\s*$")
IDENTITY = re.compile(r"(?m)^(?:author|committer) (.*?) <([^>]+)> ")


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def forbidden(name):
    p = pathlib.PurePosixPath(name)
    return (p.parts[0] in FORBIDDEN_DIRS or p.name in FORBIDDEN_NAMES
            or p.name.endswith((".local.yaml", ".local.yml", ".log", ".prof", ".pprof"))
            or (p.parts[0] == "scripts" and p.suffix == ".sql"))


def check_content(name, data, commit=False, patterns=(), expected_identity=None):
    text = data.decode("utf-8", "replace")
    findings = []
    rules = [("private-key-or-token", SIGNATURE)]
    if patterns:
        rules.append(("restricted-organization-marker", re.compile("|".join(map(re.escape, patterns)), re.IGNORECASE)))
    for rule, pattern in rules:
        for match in pattern.finditer(text):
            findings.append((name, text.count("\n", 0, match.start()) + 1, rule))
    if name.endswith((".yaml", ".yml", ".env", ".env.example")):
        for match in PASSWORD.finditer(text):
            value = match.group(1).split(" #", 1)[0].strip().strip("\"'")
            if value and value not in {"REPLACE_ME", "PLACEHOLDER"}:
                findings.append((name, text.count("\n", 0, match.start()) + 1, "nonempty-config-secret"))
    if commit and expected_identity:
        for match in IDENTITY.finditer(text):
            if match.groups() != expected_identity:
                findings.append((name, 0, "unexpected-commit-identity"))
    return findings


def history_findings(root, patterns=(), expected_identity=None):
    findings = []
    for oid in git(root, "rev-list", "--all").decode().splitlines():
        for name in git(root, "ls-tree", "-r", "--name-only", oid).decode().splitlines():
            if forbidden(name):
                findings.append(("history:" + name, 0, "restricted-path"))
    paths = {}
    for row in git(root, "rev-list", "--objects", "--all").decode().splitlines():
        oid, _, name = row.partition(" ")
        paths[oid] = name
    proc = subprocess.Popen(["git", "-C", str(root), "cat-file", "--batch"], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    try:
        metadata = git(root, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
        for row in metadata.decode().splitlines():
            oid, kind = row.split()
            if kind not in {"blob", "commit", "tag"}:
                continue
            proc.stdin.write((oid + "\n").encode())
            proc.stdin.flush()
            header = proc.stdout.readline().split()
            data = proc.stdout.read(int(header[2]))
            proc.stdout.read(1)
            findings.extend(check_content("history:" + (paths.get(oid) or oid), data, commit=kind == "commit", patterns=patterns, expected_identity=expected_identity))
    finally:
        proc.stdin.close()
        proc.wait()
    return findings


def audit(root, history=False, snapshot=None, patterns=(), expected_identity=None):
    names = sorted(set(x.decode() for x in git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").split(b"\0") if x))
    findings = []
    files = []
    for name in names:
        path = root / name
        if not path.exists() and not path.is_symlink():  # intentionally deleted tracked file
            continue
        if forbidden(name):
            findings.append((name, 0, "restricted-path"))
            continue
        if path.is_symlink() or not path.is_file():
            findings.append((name, 0, "nonregular-public-file"))
            continue
        findings.extend(check_content(name, path.read_bytes(), patterns=patterns))
        files.append(name)
    if history:
        findings.extend(history_findings(root, patterns, expected_identity))
    if findings:
        for name, line, rule in sorted(set(findings)):
            print(f"FAIL {name}:{line}: {rule}", file=sys.stderr)
        return 1
    if snapshot:
        if snapshot.exists() and (not snapshot.is_dir() or any(snapshot.iterdir())):
            raise ValueError("snapshot destination must be new or empty")
        snapshot.mkdir(parents=True, exist_ok=True)
        for name in files:
            dest = snapshot / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(root / name, dest)
    print(f"PASS: {len(files)} publishable files audited" + ("; local history audited" if history else ""))
    return 0


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--history", action="store_true")
    p.add_argument("--snapshot", type=pathlib.Path, help="export audited files only to a new/empty directory")
    p.add_argument("--forbidden-patterns", type=pathlib.Path, help="private JSON list of organization markers; never copy this policy into the repo")
    p.add_argument("--expected-author-name", help="optional historical-publication audit, not a contributor restriction")
    p.add_argument("--expected-author-email")
    args = p.parse_args()
    if bool(args.expected_author_name) != bool(args.expected_author_email):
        raise ValueError("expected author name/email must be provided together")
    identity = (args.expected_author_name, args.expected_author_email) if args.expected_author_name else None
    raw = args.forbidden_patterns.read_text() if args.forbidden_patterns else os.environ.get("PUBLICATION_FORBIDDEN_PATTERNS", "[]")
    patterns = json.loads(raw)
    if not isinstance(patterns, list) or len(patterns) > 1000 or any(not isinstance(x, str) or not x or len(x) > 128 for x in patterns):
        raise ValueError("invalid private marker policy")
    root = pathlib.Path(git(pathlib.Path.cwd(), "rev-parse", "--show-toplevel").decode().strip())
    if args.forbidden_patterns and args.forbidden_patterns.resolve().is_relative_to(root.resolve()):
        raise ValueError("private marker policy must be outside the repository")
    return audit(root, args.history, args.snapshot, patterns, identity)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"audit failed: {type(error).__name__}", file=sys.stderr)
        sys.exit(1)
