#!/usr/bin/env python3
from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

REQUIRED_FILES = [
    Path("docs/ci.md"),
    Path(".github/pull_request_template.md"),
    Path(".github/workflows/ci.yml"),
    Path(".github/workflows/cli-pr-check.yml"),
    Path(".github/workflows/doc-lint.yml"),
    Path(".github/workflows/secret-scan.yml"),
    Path(".github/workflows/license-check.yml"),
    Path("NOTICE"),
    Path("docs/README.md"),
    Path("docs/architecture.md"),
    Path("docs/governance/dependency-licensing.md"),
    Path("docs/module-contracts.md"),
    Path("docs/test-strategy.md"),
    Path("docs/monitoring/README.md"),
    Path("scripts/check-module-contracts.sh"),
    Path("scripts/regression_gate.sh"),
    Path("scripts/regression_gate.list"),
    Path("scripts/integration_tests.sh"),
    Path("scripts/backend_smoke.sh"),
]

LINK_CHECK_DOCS = [
    Path("docs/ci.md"),
    Path("docs/README.md"),
    Path("docs/test-strategy.md"),
]

EXPECTED_WORKFLOWS = [
    "ci.yml",
    "cli-pr-check.yml",
    "doc-lint.yml",
    "secret-scan.yml",
    "license-check.yml",
]

MARKDOWN_LINK_RE = re.compile(r"\[[^\]]+\]\(([^)]+)\)")


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def is_external_link(target: str) -> bool:
    return (
        target.startswith("http://")
        or target.startswith("https://")
        or target.startswith("mailto:")
    )


def resolve_doc_link(doc_path: Path, target: str) -> Path | None:
    target = target.strip().strip("<>")
    if not target or is_external_link(target) or target.startswith("#"):
        return None

    relative_target = target.split("#", 1)[0]
    if not relative_target:
        return None

    if relative_target.startswith("/"):
        return ROOT / relative_target.lstrip("/")

    return (doc_path.parent / relative_target).resolve()


def check_required_files(errors: list[str]) -> None:
    for relative_path in REQUIRED_FILES:
        if not (ROOT / relative_path).exists():
            errors.append(f"missing required file: {relative_path}")


def check_markdown_links(errors: list[str]) -> None:
    for relative_doc in LINK_CHECK_DOCS:
        doc_path = ROOT / relative_doc
        text = read_text(doc_path)
        for match in MARKDOWN_LINK_RE.finditer(text):
            target = match.group(1).strip()
            resolved = resolve_doc_link(doc_path, target)
            if resolved is None:
                continue
            if not resolved.exists():
                errors.append(
                    f"{relative_doc}: broken relative link target `{target}`"
                )


def check_ci_docs_inventory(errors: list[str]) -> None:
    ci_docs_text = read_text(ROOT / "docs/ci.md")
    for workflow_name in EXPECTED_WORKFLOWS:
        workflow_path = ROOT / ".github" / "workflows" / workflow_name
        if not workflow_path.exists():
            errors.append(f"workflow missing from repo: .github/workflows/{workflow_name}")
        if f"`{workflow_name}`" not in ci_docs_text:
            errors.append(f"docs/ci.md must mention `{workflow_name}`")


def check_gate_docs(errors: list[str]) -> None:
    test_strategy = read_text(ROOT / "docs/test-strategy.md")
    ci_docs_text = read_text(ROOT / "docs/ci.md")
    pr_template = read_text(ROOT / ".github/pull_request_template.md")

    for snippet in (
        "scripts/check-module-contracts.sh",
        "scripts/regression_gate.sh",
        "scripts/integration_tests.sh",
        "scripts/backend_smoke.sh",
    ):
        if snippet not in test_strategy:
            errors.append(f"docs/test-strategy.md must mention `{snippet}`")

    for snippet in ("docs/ci.md", "docs/test-strategy.md", "#123"):
        if snippet not in pr_template:
            errors.append(f".github/pull_request_template.md must mention `{snippet}`")

    for snippet in (
        "Regression Gate",
        "Integration Tests",
        "Compatibility Tests",
        "E2E Tests",
        "Backend Smoke",
    ):
        if snippet not in ci_docs_text:
            errors.append(f"docs/ci.md must document `{snippet}`")


def check_module_contracts(errors: list[str]) -> None:
    result = subprocess.run(
        ["bash", str(ROOT / "scripts/check-module-contracts.sh")],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    if result.returncode == 0:
        return

    output = []
    if result.stderr.strip():
        output.extend(result.stderr.strip().splitlines())
    if result.stdout.strip():
        output.extend(result.stdout.strip().splitlines())

    if not output:
        errors.append("module-contract coverage check failed")
        return

    errors.extend(output)


def main() -> int:
    errors: list[str] = []
    check_required_files(errors)
    if errors:
        for error in errors:
            print(f"doc-lint: {error}", file=sys.stderr)
        return 1

    check_markdown_links(errors)
    check_ci_docs_inventory(errors)
    check_gate_docs(errors)
    check_module_contracts(errors)

    if errors:
        for error in errors:
            print(f"doc-lint: {error}", file=sys.stderr)
        return 1

    print("doc-lint: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
