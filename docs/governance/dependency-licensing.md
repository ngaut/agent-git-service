# Dependency Licensing

This document records the automated dependency license checks used by this
repository. It is an engineering checklist, not legal advice.

## Automated Check

Run:

```bash
python3 scripts/license_check.py --report artifacts/dependency-licenses.tsv
```

The script scans the root Go module build list from `go list -m -json all`,
reads top-level license and NOTICE files from each downloaded module, and fails
when it finds:

- an unknown license
- a license outside the reviewed allowlist
- a third-party NOTICE file that has not been reviewed into the root `NOTICE`
- a reviewed third-party NOTICE file whose text is missing from the root `NOTICE`

The current reviewed allowlist is:

- `Apache-2.0`
- `BSD-2-Clause`
- `BSD-3-Clause`
- `ISC`
- `MIT`
- `MPL-2.0`

`license-check.yml` runs this check on pull requests, pushes to `main`, and
manual workflow dispatch.

## Reviewed Override

`github.com/joho/godotenv v1.5.1` is treated as `MIT` because its Go module zip
does not include a top-level license file, while [pkg.go.dev reports MIT for
the module](https://pkg.go.dev/github.com/joho/godotenv).

## NOTICE Status

The root `NOTICE` file includes reviewed third-party notices currently detected
from:

- `github.com/prometheus/client_golang`
- `github.com/prometheus/client_model`
- `github.com/prometheus/common`
- `github.com/prometheus/procfs`
- `github.com/skeema/knownhosts`
- `go.yaml.in/yaml/v2`
- `gopkg.in/yaml.v2`
- `gopkg.in/yaml.v3`

If a future dependency adds a new NOTICE file, `scripts/license_check.py` fails
until the notice is reviewed and either added to the root `NOTICE` or otherwise
handled by project policy. Existing reviewed NOTICE entries also fail if the
root `NOTICE` text drifts away from the checked-in reviewed notice content.

## Scope Notes

- The check covers the root Go module dependency graph.
- License identification is based on reviewed text heuristics plus explicit
  overrides for exceptional modules.
- The vendored `cli/` tree carries its own MIT license at `cli/LICENSE`.
- The nested `cli/_go-gh-local` tree carries its own MIT license at
  `cli/_go-gh-local/LICENSE`.
- A broader distribution audit can still run a dedicated license scanner over
  the vendored CLI tree and generated build artifacts.
