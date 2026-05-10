#!/usr/bin/env python3
from __future__ import annotations

import argparse
import csv
import json
import subprocess
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]

ALLOWED_LICENSES = {
    "Apache-2.0",
    "BSD-2-Clause",
    "BSD-3-Clause",
    "ISC",
    "MIT",
    "MPL-2.0",
}

DISALLOWED_LICENSES = {
    "AGPL",
    "BUSL",
    "GPL",
    "LGPL",
    "SSPL",
    "UNKNOWN",
}

REVIEWED_LICENSE_OVERRIDES = {
    ("github.com/joho/godotenv", "v1.5.1"): {
        "licenses": ["MIT"],
        "note": "module zip has no top-level license file; pkg.go.dev reports MIT",
    },
}

REVIEWED_NOTICE_MODULES = {
    ("github.com/prometheus/client_golang", "v1.23.2"),
    ("github.com/prometheus/client_model", "v0.6.2"),
    ("github.com/prometheus/common", "v0.66.1"),
    ("github.com/prometheus/procfs", "v0.16.1"),
    ("github.com/skeema/knownhosts", "v1.3.2"),
    ("go.yaml.in/yaml/v2", "v2.4.2"),
    ("gopkg.in/yaml.v2", "v2.4.0"),
    ("gopkg.in/yaml.v3", "v3.0.1"),
}

LICENSE_FILE_PREFIXES = (
    "copying",
    "license",
)


def normalize_notice_text(text: str) -> str:
    normalized_lines: list[str] = []
    previous_blank = False
    for raw_line in text.strip().splitlines():
        line = raw_line.rstrip()
        is_blank = line == ""
        if is_blank and previous_blank:
            continue
        normalized_lines.append(line)
        previous_blank = is_blank
    return "\n".join(normalized_lines)


def run(cmd: list[str]) -> str:
    proc = subprocess.run(
        cmd,
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return proc.stdout


def parse_json_stream(raw: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    items: list[dict[str, Any]] = []
    idx = 0
    while idx < len(raw):
        while idx < len(raw) and raw[idx].isspace():
            idx += 1
        if idx >= len(raw):
            break
        item, next_idx = decoder.raw_decode(raw, idx)
        items.append(item)
        idx = next_idx
    return items


def detect_license_ids(text: str) -> set[str]:
    compact = " ".join(text.lower().split())
    ids: set[str] = set()

    if "apache license" in compact and "version 2.0" in compact:
        ids.add("Apache-2.0")
    if "mozilla public license version 2.0" in compact:
        ids.add("MPL-2.0")
    # MPL-2.0 includes secondary-license terms that mention GPL/LGPL/AGPL.
    # Treat those as part of MPL text, not as the module's detected license.
    if "MPL-2.0" not in ids:
        if "gnu affero general public license" in compact:
            ids.add("AGPL")
        elif "gnu lesser general public license" in compact:
            ids.add("LGPL")
        elif "gnu general public license" in compact:
            ids.add("GPL")
    if "business source license" in compact:
        ids.add("BUSL")
    if "server side public license" in compact:
        ids.add("SSPL")
    if (
        "permission is hereby granted, free of charge, to any person obtaining a copy"
        in compact
        and "the software" in compact
    ):
        ids.add("MIT")
    if "redistribution and use in source and binary forms" in compact:
        if "neither the name" in compact:
            ids.add("BSD-3-Clause")
        else:
            ids.add("BSD-2-Clause")
    if (
        "permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted"
        in compact
    ):
        ids.add("ISC")

    return ids


def top_level_files(module_dir: Path) -> tuple[list[Path], list[Path]]:
    license_files: list[Path] = []
    notice_files: list[Path] = []
    for child in module_dir.iterdir():
        if not child.is_file():
            continue
        name = child.name.lower()
        if name.startswith("notice"):
            notice_files.append(child)
            continue
        if name.startswith(LICENSE_FILE_PREFIXES):
            license_files.append(child)
    return sorted(license_files), sorted(notice_files)


def scan_module(module: dict[str, Any]) -> dict[str, str]:
    path = module["Path"]
    version = module.get("Version", "")
    module_dir = module.get("Dir", "")
    override = REVIEWED_LICENSE_OVERRIDES.get((path, version))

    license_files: list[Path] = []
    notice_files: list[Path] = []
    license_ids: set[str] = set()
    notes: list[str] = []

    if module_dir:
        dir_path = Path(module_dir)
        if dir_path.exists():
            license_files, notice_files = top_level_files(dir_path)
            for file_path in license_files:
                text = file_path.read_text(encoding="utf-8", errors="ignore")
                detected = detect_license_ids(text)
                if detected:
                    license_ids.update(detected)
                else:
                    license_ids.add("UNKNOWN")
                    notes.append(f"unrecognized license text in {file_path.name}")
        else:
            notes.append(f"module dir does not exist: {module_dir}")
    else:
        notes.append("go list did not report module dir")

    if not license_ids and override:
        license_ids.update(override["licenses"])
        notes.append(f"reviewed override: {override['note']}")

    if not license_ids:
        license_ids.add("UNKNOWN")

    if notice_files and (path, version) in REVIEWED_NOTICE_MODULES:
        notes.append("reviewed notice: included in root NOTICE")

    return {
        "module": path,
        "version": version,
        "module_dir": module_dir,
        "licenses": ",".join(sorted(license_ids)),
        "license_files": ",".join(file.name for file in license_files),
        "notice_files": ",".join(file.name for file in notice_files),
        "notes": "; ".join(notes),
    }


def validate_root_notice(rows: list[dict[str, str]], root_notice_path: Path) -> list[str]:
    failures: list[str] = []
    root_notice = normalize_notice_text(root_notice_path.read_text(encoding="utf-8"))

    for row in rows:
        if not row["notice_files"]:
            continue

        module_key = (row["module"], row["version"])
        if module_key not in REVIEWED_NOTICE_MODULES:
            continue

        module_dir = Path(row["module_dir"])
        notice_names = [name for name in row["notice_files"].split(",") if name]
        for notice_name in notice_names:
            notice_path = module_dir / notice_name
            if not notice_path.exists():
                failures.append(
                    f"{row['module']} {row['version']}: reviewed NOTICE file missing from module cache: {notice_name}"
                )
                continue

            normalized_notice = normalize_notice_text(
                notice_path.read_text(encoding="utf-8", errors="ignore")
            )
            if normalized_notice not in root_notice:
                failures.append(
                    f"{row['module']} {row['version']}: root NOTICE does not include reviewed NOTICE text from {notice_name}"
                )

    return failures


def write_tsv(rows: list[dict[str, str]], output: Path | None) -> None:
    fieldnames = [
        "module",
        "version",
        "licenses",
        "license_files",
        "notice_files",
        "notes",
    ]
    target = output.open("w", encoding="utf-8", newline="") if output else sys.stdout
    close_target = output is not None
    try:
        writer = csv.DictWriter(
            target,
            fieldnames=fieldnames,
            delimiter="\t",
            extrasaction="ignore",
        )
        writer.writeheader()
        writer.writerows(rows)
    finally:
        if close_target:
            target.close()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Check root Go module dependency licenses and NOTICE files.",
    )
    parser.add_argument(
        "--report",
        type=Path,
        help="Optional TSV report path.",
    )
    parser.add_argument(
        "--allow-notice-files",
        action="store_true",
        help="Do not fail when dependency NOTICE files are present.",
    )
    parser.add_argument(
        "--skip-download",
        action="store_true",
        help="Skip `go mod download all` before scanning.",
    )
    args = parser.parse_args()

    if not args.skip_download:
        run(["go", "mod", "download", "all"])

    modules = [
        item
        for item in parse_json_stream(run(["go", "list", "-m", "-json", "all"]))
        if not item.get("Main")
    ]
    rows = sorted((scan_module(module) for module in modules), key=lambda row: row["module"])

    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
    write_tsv(rows, args.report)

    failures: list[str] = []
    for row in rows:
        licenses = set(row["licenses"].split(","))
        unknown_or_disallowed = sorted(licenses & DISALLOWED_LICENSES)
        not_allowed = sorted(licenses - ALLOWED_LICENSES - DISALLOWED_LICENSES)
        if unknown_or_disallowed:
            failures.append(
                f"{row['module']} {row['version']}: disallowed or unknown license(s): {', '.join(unknown_or_disallowed)}"
            )
        if not_allowed:
            failures.append(
                f"{row['module']} {row['version']}: license requires review: {', '.join(not_allowed)}"
            )
        if (
            row["notice_files"]
            and not args.allow_notice_files
            and (row["module"], row["version"]) not in REVIEWED_NOTICE_MODULES
        ):
            failures.append(
                f"{row['module']} {row['version']}: NOTICE file(s) require root NOTICE review: {row['notice_files']}"
            )

    if not args.allow_notice_files:
        failures.extend(validate_root_notice(rows, ROOT / "NOTICE"))

    if failures:
        for failure in failures:
            print(f"license-check: {failure}", file=sys.stderr)
        return 1

    print(
        f"license-check: PASS ({len(rows)} Go modules, {len(ALLOWED_LICENSES)} allowed license families)",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
