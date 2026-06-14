#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "missing dependency: $cmd" >&2
    exit 1
  }
}

require_cmd go

cd "$ROOT_DIR"

echo "==> Wiki stale projection service regressions"
go test ./internal/service -count=1 -run 'Test(ListWikiTreeAtRef_UsesCatalogRowsWhenGitProjectionLagsLiveHead|ListWikiTreeAtRef_FallsBackToGitWithoutCatalogRows|WikiTreeUsesCatalogRowsWhenGitProjectionLagsCurrentHead|WikiTreeUsesCatalogRowsWhenV2IndexTracksLaggingGitProjection|ListWikiBacklinksDoesNotReturnGitOnlySourceWhenCatalogHasLiveRows)$'

echo "==> Wiki stale projection search regressions"
go test ./internal/service -count=1 -run 'TestWikiSearch(DoesNotReturnGitOnlyPageWhenCatalogHasLiveRows|FallsBackToGitWithoutCatalogRows|UsesCatalogBodyWhenGitProjectionLagsCatalogPage|UsesCatalogLexicalWhenIndexedRowsMissLivePage)$'

echo "==> Wiki REST transform link regressions"
go test ./internal/rest/transform -count=1 -run 'TestWikiTreeEntryCarriesRefInPageURLs$'

echo "Wiki regression gate passed."
