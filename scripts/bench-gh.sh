#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_OUTPUT_ROOT="$ROOT_DIR/artifacts/bench-gh"
RUN_ID="$(date +%Y%m%d-%H%M%S)-$$-$RANDOM"

HOST="${BENCH_GH_HOST:-${GH_HOST:-github.localhost}}"
TOKEN="${BENCH_GH_TOKEN:-${GH_TOKEN:-mytoken}}"
OWNER="${BENCH_GH_OWNER:-}"
RUNS="${BENCH_GH_RUNS:-3}"
WARMUPS="${BENCH_GH_WARMUPS:-1}"
OUTPUT_DIR=""
KEEP_WORKDIR=0
LIST_SCENARIOS_ONLY=0

ALL_SCENARIOS=(
  auth-login
  auth-status
  auth-logout
  repo-create
  repo-view
  repo-clone
  issue-create
  issue-list
  issue-view
  pr-create
  pr-list
  pr-view
  pr-merge
  workflow-list
  workflow-run
  run-view
)

SCENARIOS=()
CREATED_REPOS=()
WORK_ROOT=""
HOME_SANDBOX=""
GH_CONFIG_SANDBOX=""
SUMMARY_FILE=""
GIT_HELPER_SCRIPT=""

VIEWER_LOGIN=""
REPO_READ_REPO=""
ISSUE_REPO=""
ISSUE_VIEW_NUMBER=""
PR_READ_REPO=""
PR_VIEW_NUMBER=""
WORKFLOW_REPO=""
RUN_VIEW_ID=""
REPO_CLONE_MODE="gh"

note() {
  printf '[bench-gh] %s\n' "$*"
}

fail() {
  printf '[bench-gh] ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  scripts/bench-gh.sh [options]

Options:
  --scenario NAME      Run a specific scenario. Repeatable.
  --runs N             Timed iterations per scenario. Default: 3
  --warmups N          Warmup iterations per scenario. Default: 1
  --host HOST          gh-server hostname. Default: github.localhost
  --token TOKEN        Token for the benchmark login. Default: mytoken
  --owner OWNER        Repo owner to create fixtures under. Default: current viewer login
  --output-dir DIR     Output directory. Default: artifacts/bench-gh/<run-id>
  --keep-workdir       Preserve the temp workspace for inspection
  --list-scenarios     Print supported scenarios and exit
  --help               Show this help

Environment overrides:
  BENCH_GH_HOST
  BENCH_GH_TOKEN
  BENCH_GH_OWNER
  BENCH_GH_RUNS
  BENCH_GH_WARMUPS

Examples:
  scripts/bench-gh.sh
  scripts/bench-gh.sh --scenario repo-view --scenario issue-list --runs 10 --warmups 2
  scripts/bench-gh.sh --host github.localhost --token mytoken --owner testadmin
EOF
}

list_scenarios() {
  printf '%s\n' "${ALL_SCENARIOS[@]}"
}

contains_scenario() {
  local needle="$1"
  local item
  for item in "${SCENARIOS[@]}"; do
    if [[ "$item" == "$needle" ]]; then
      return 0
    fi
  done
  return 1
}

needs_any() {
  local item
  for item in "$@"; do
    if contains_scenario "$item"; then
      return 0
    fi
  done
  return 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

register_repo() {
  CREATED_REPOS+=("$1")
}

delete_repo_quietly() {
  local repo="$1"
  gh repo delete "$repo" --yes >/dev/null 2>&1 || true
}

cleanup() {
  local repo
  for (( idx=${#CREATED_REPOS[@]}-1; idx>=0; idx-- )); do
    repo="${CREATED_REPOS[idx]}"
    delete_repo_quietly "$repo"
  done

  if [[ "$KEEP_WORKDIR" != "1" && -n "$WORK_ROOT" && -d "$WORK_ROOT" ]]; then
    rm -rf "$WORK_ROOT"
  fi
}

trap cleanup EXIT

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --scenario)
        [[ $# -ge 2 ]] || fail "--scenario requires a value"
        SCENARIOS+=("$2")
        shift 2
        ;;
      --runs)
        [[ $# -ge 2 ]] || fail "--runs requires a value"
        RUNS="$2"
        shift 2
        ;;
      --warmups)
        [[ $# -ge 2 ]] || fail "--warmups requires a value"
        WARMUPS="$2"
        shift 2
        ;;
      --host)
        [[ $# -ge 2 ]] || fail "--host requires a value"
        HOST="$2"
        shift 2
        ;;
      --token)
        [[ $# -ge 2 ]] || fail "--token requires a value"
        TOKEN="$2"
        shift 2
        ;;
      --owner)
        [[ $# -ge 2 ]] || fail "--owner requires a value"
        OWNER="$2"
        shift 2
        ;;
      --output-dir)
        [[ $# -ge 2 ]] || fail "--output-dir requires a value"
        OUTPUT_DIR="$2"
        shift 2
        ;;
      --keep-workdir)
        KEEP_WORKDIR=1
        shift
        ;;
      --list-scenarios)
        LIST_SCENARIOS_ONLY=1
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        fail "unknown argument: $1"
        ;;
    esac
  done
}

validate_args() {
  [[ "$RUNS" =~ ^[0-9]+$ ]] || fail "--runs must be an integer"
  [[ "$WARMUPS" =~ ^[0-9]+$ ]] || fail "--warmups must be an integer"

  if [[ "${#SCENARIOS[@]}" -eq 0 ]]; then
    SCENARIOS=("${ALL_SCENARIOS[@]}")
  fi

  local scenario supported found
  for scenario in "${SCENARIOS[@]}"; do
    found=1
    for supported in "${ALL_SCENARIOS[@]}"; do
      if [[ "$scenario" == "$supported" ]]; then
        found=0
        break
      fi
    done
    if [[ "$found" -ne 0 ]]; then
      fail "unsupported scenario: $scenario"
    fi
  done

  if [[ -z "$OUTPUT_DIR" ]]; then
    OUTPUT_DIR="$DEFAULT_OUTPUT_ROOT/$RUN_ID"
  fi
}

prepare_environment() {
  require_cmd awk
  require_cmd curl
  require_cmd gh
  require_cmd git
  require_cmd mktemp
  require_cmd sort

  [[ -x /usr/bin/time ]] || fail "/usr/bin/time is required"

  mkdir -p "$OUTPUT_DIR"
  SUMMARY_FILE="$OUTPUT_DIR/summary.tsv"
  : > "$SUMMARY_FILE"
  printf 'scenario\tn\tmean_ms\tp50_ms\tp95_ms\tmin_ms\tmax_ms\traw_file\n' >>"$SUMMARY_FILE"

  WORK_ROOT="$(mktemp -d "/tmp/bench-gh-${RUN_ID}.XXXXXX")"
  HOME_SANDBOX="$WORK_ROOT/home"
  GH_CONFIG_SANDBOX="$WORK_ROOT/gh-config"
  mkdir -p "$HOME_SANDBOX" "$GH_CONFIG_SANDBOX" "$WORK_ROOT/repos" "$WORK_ROOT/clones"

  export HOME="$HOME_SANDBOX"
  export GH_CONFIG_DIR="$GH_CONFIG_SANDBOX"
  export GH_HOST="$HOST"
  export GH_PAGER=cat
  export PAGER=cat
  export NO_COLOR=1
  export GH_NO_UPDATE_NOTIFIER=1
  export GIT_TERMINAL_PROMPT=0
  export LANG=C
  export LC_ALL=C
}

preflight_server() {
  note "checking gh-server reachability on http://$HOST"
  curl -fsS "http://$HOST/api/v3/" >/dev/null || fail "cannot reach http://$HOST/api/v3/"
  curl -fsS -H "Authorization: token $TOKEN" "http://api.$HOST/user" >/dev/null || fail "token rejected by http://api.$HOST/user"
}

login_main_config() {
  note "logging into $HOST in isolated gh config"
  printf '%s\n' "$TOKEN" | gh auth login --hostname "$HOST" --with-token --insecure-storage >/dev/null
  gh auth setup-git >/dev/null
  export GH_TOKEN="$TOKEN"
  VIEWER_LOGIN="$(gh api user --hostname "$HOST" --jq '.login')"
  [[ -n "$VIEWER_LOGIN" ]] || fail "failed to resolve viewer login"
  if [[ -z "$OWNER" ]]; then
    OWNER="$VIEWER_LOGIN"
  fi
  note "viewer=$VIEWER_LOGIN owner=$OWNER"
}

configure_git_auth_helper() {
  GIT_HELPER_SCRIPT="$WORK_ROOT/git-credential-helper.sh"
  cat >"$GIT_HELPER_SCRIPT" <<EOF
#!/usr/bin/env bash
set -eu

if [[ "\${1:-}" == "get" ]]; then
  printf 'username=%s\n' '$VIEWER_LOGIN'
  printf 'password=%s\n' '$TOKEN'
fi
EOF
  chmod +x "$GIT_HELPER_SCRIPT"
  git config --global --add credential."http://$HOST".helper "!$GIT_HELPER_SCRIPT"
  git config --global --add credential."https://$HOST".helper "!$GIT_HELPER_SCRIPT"
}

repo_full_name() {
  local name="$1"
  printf '%s/%s\n' "$OWNER" "$name"
}

create_repo_with_readme() {
  local repo="$1"
  gh repo create "$repo" --private --add-readme >/dev/null
}

clone_repo() {
  local repo="$1"
  local dest="$2"
  git clone -q "http://$HOST/$repo.git" "$dest" >/dev/null
}

setup_git_identity() {
  local dir="$1"
  git -C "$dir" config user.email "bench-gh@example.com"
  git -C "$dir" config user.name "bench-gh"
}

write_workflow_file() {
  local dir="$1"
  mkdir -p "$dir/.github/workflows"
  cat >"$dir/.github/workflows/bench.yml" <<'EOF'
name: Bench Workflow
on:
  workflow_dispatch:
jobs:
  bench:
    runs-on: ubuntu-latest
    steps:
      - name: Echo
        run: echo bench
EOF
}

wait_for_latest_run_id() {
  local repo="$1"
  local attempts="${2:-20}"
  local delay="${3:-1}"
  local run_id=""
  local i

  for i in $(seq 1 "$attempts"); do
    run_id="$(gh run list --repo "$repo" --limit 1 --json databaseId --jq '.[0].databaseId' 2>/dev/null || true)"
    if [[ -n "$run_id" && "$run_id" != "null" ]]; then
      printf '%s\n' "$run_id"
      return 0
    fi
    sleep "$delay"
  done

  return 1
}

elapsed_ms_from_ns() {
  local start_ns="$1"
  local end_ns="$2"
  awk -v start="$start_ns" -v end="$end_ns" 'BEGIN { printf "%.3f\n", (end - start) / 1000000 }'
}

measure_cmd() {
  local log_file="$1"
  shift
  local start_ns end_ns

  start_ns="$(date +%s%N)"
  if "$@" >>"$log_file" 2>&1; then
    end_ns="$(date +%s%N)"
    elapsed_ms_from_ns "$start_ns" "$end_ns"
    return 0
  else
    local rc=$?
    end_ns="$(date +%s%N)"
    note "command failed; last log lines:"
    tail -n 40 "$log_file" >&2 || true
    return "$rc"
  fi
}

measure_in_dir() {
  local dir="$1"
  local log_file="$2"
  shift 2
  local start_ns end_ns

  start_ns="$(date +%s%N)"
  if (cd "$dir" && "$@") >>"$log_file" 2>&1; then
    end_ns="$(date +%s%N)"
    elapsed_ms_from_ns "$start_ns" "$end_ns"
    return 0
  else
    local rc=$?
    end_ns="$(date +%s%N)"
    note "command failed in $dir; last log lines:"
    tail -n 40 "$log_file" >&2 || true
    return "$rc"
  fi
}

measure_auth_login() {
  local phase="$1"
  local iter="$2"
  local log_file="$3"
  local auth_root="$WORK_ROOT/auth-login-${phase}-${iter}"
  local home_dir="$auth_root/home"
  local cfg_dir="$auth_root/gh-config"
  local start_ns end_ns

  mkdir -p "$home_dir" "$cfg_dir"
  start_ns="$(date +%s%N)"
  if printf '%s\n' "$TOKEN" | env -u GH_TOKEN HOME="$home_dir" GH_CONFIG_DIR="$cfg_dir" GH_HOST="$HOST" gh auth login --hostname "$HOST" --with-token --insecure-storage >>"$log_file" 2>&1; then
    end_ns="$(date +%s%N)"
    elapsed_ms_from_ns "$start_ns" "$end_ns"
    return 0
  fi

  local rc=$?
  note "auth-login failed; last log lines:"
  tail -n 40 "$log_file" >&2 || true
  return "$rc"
}

measure_auth_logout() {
  local phase="$1"
  local iter="$2"
  local log_file="$3"
  local auth_root="$WORK_ROOT/auth-logout-${phase}-${iter}"
  local home_dir="$auth_root/home"
  local cfg_dir="$auth_root/gh-config"
  local start_ns end_ns

  mkdir -p "$home_dir" "$cfg_dir"
  printf '%s\n' "$TOKEN" | env -u GH_TOKEN HOME="$home_dir" GH_CONFIG_DIR="$cfg_dir" GH_HOST="$HOST" gh auth login --hostname "$HOST" --with-token --insecure-storage >/dev/null

  start_ns="$(date +%s%N)"
  if env -u GH_TOKEN HOME="$home_dir" GH_CONFIG_DIR="$cfg_dir" GH_HOST="$HOST" gh auth logout --hostname "$HOST" >>"$log_file" 2>&1; then
    end_ns="$(date +%s%N)"
    elapsed_ms_from_ns "$start_ns" "$end_ns"
    return 0
  fi

  local rc=$?
  note "auth-logout failed; last log lines:"
  tail -n 40 "$log_file" >&2 || true
  return "$rc"
}

extract_number_from_url() {
  local url="$1"
  printf '%s\n' "${url##*/}"
}

prepare_issue_fixtures() {
  local repo_name="bench-issue-$RUN_ID"
  local first_issue_url=""
  local idx

  ISSUE_REPO="$(repo_full_name "$repo_name")"
  note "preparing issue fixtures in $ISSUE_REPO"
  create_repo_with_readme "$ISSUE_REPO"
  register_repo "$ISSUE_REPO"

  for idx in $(seq 1 5); do
    first_issue_url="$(gh issue create --repo "$ISSUE_REPO" --title "Bench issue seed $idx" --body "seed issue $idx")"
    if [[ "$idx" == "1" ]]; then
      ISSUE_VIEW_NUMBER="$(extract_number_from_url "$first_issue_url")"
    fi
  done
}

prepare_pr_read_fixtures() {
  local repo_name="bench-pr-read-$RUN_ID"
  local clone_dir="$WORK_ROOT/repos/pr-read"
  local pr_url=""

  PR_READ_REPO="$(repo_full_name "$repo_name")"
  note "preparing PR read fixtures in $PR_READ_REPO"
  create_repo_with_readme "$PR_READ_REPO"
  register_repo "$PR_READ_REPO"
  clone_repo "$PR_READ_REPO" "$clone_dir"
  setup_git_identity "$clone_dir"

  git -C "$clone_dir" checkout -b feature-read >/dev/null
  printf 'pr-read fixture\n' >"$clone_dir/fixture.txt"
  git -C "$clone_dir" add fixture.txt >/dev/null
  git -C "$clone_dir" commit -m 'bench: prepare pr read fixture' >/dev/null
  git -C "$clone_dir" push -u origin feature-read >/dev/null

  pr_url="$(cd "$clone_dir" && gh pr create --repo "$PR_READ_REPO" --base main --head feature-read --title 'Bench PR Read' --body 'fixture')"
  PR_VIEW_NUMBER="$(extract_number_from_url "$pr_url")"
}

prepare_workflow_fixtures() {
  local repo_name="bench-workflow-$RUN_ID"
  local clone_dir="$WORK_ROOT/repos/workflow"

  WORKFLOW_REPO="$(repo_full_name "$repo_name")"
  note "preparing workflow fixtures in $WORKFLOW_REPO"
  create_repo_with_readme "$WORKFLOW_REPO"
  register_repo "$WORKFLOW_REPO"
  clone_repo "$WORKFLOW_REPO" "$clone_dir"
  setup_git_identity "$clone_dir"
  write_workflow_file "$clone_dir"
  git -C "$clone_dir" add .github/workflows/bench.yml >/dev/null
  git -C "$clone_dir" commit -m 'bench: add workflow fixture' >/dev/null
  git -C "$clone_dir" push -u origin main >/dev/null

  sleep 1
}

prepare_run_view_fixture() {
  [[ -n "$WORKFLOW_REPO" ]] || fail "workflow repo fixture is not prepared"
  gh workflow run 'Bench Workflow' --repo "$WORKFLOW_REPO" --ref main >/dev/null
  RUN_VIEW_ID="$(wait_for_latest_run_id "$WORKFLOW_REPO")" || fail "failed to obtain workflow run id for $WORKFLOW_REPO"
  gh run watch "$RUN_VIEW_ID" --repo "$WORKFLOW_REPO" --exit-status >/dev/null
}

prepare_repo_read_fixture() {
  local repo_name="bench-repo-read-$RUN_ID"
  local probe_dir=""
  REPO_READ_REPO="$(repo_full_name "$repo_name")"
  note "preparing repo read fixture in $REPO_READ_REPO"
  create_repo_with_readme "$REPO_READ_REPO"
  register_repo "$REPO_READ_REPO"

  if contains_scenario repo-clone; then
    probe_dir="$WORK_ROOT/clones/repo-clone-probe"
    rm -rf "$probe_dir"
    if gh repo clone "$REPO_READ_REPO" "$probe_dir" >/dev/null 2>"$OUTPUT_DIR/repo-clone-probe.log"; then
      REPO_CLONE_MODE="gh"
    else
      REPO_CLONE_MODE="git"
      note "gh repo clone is not usable against the current clone URL; repo-clone will fall back to git clone"
    fi
    rm -rf "$probe_dir"
  fi
}

prepare_fixtures() {
  if needs_any repo-view repo-clone; then
    prepare_repo_read_fixture
  fi
  if needs_any issue-create issue-list issue-view; then
    prepare_issue_fixtures
  fi
  if needs_any pr-list pr-view; then
    prepare_pr_read_fixtures
  fi
  if needs_any workflow-list workflow-run run-view; then
    prepare_workflow_fixtures
  fi
  if needs_any run-view; then
    prepare_run_view_fixture
  fi
}

scenario_iteration() {
  local scenario="$1"
  local phase="$2"
  local iter="$3"
  local log_file="$4"

  case "$scenario" in
    auth-login)
      measure_auth_login "$phase" "$iter" "$log_file"
      ;;
    auth-status)
      measure_cmd "$log_file" gh auth status --hostname "$HOST"
      ;;
    auth-logout)
      measure_auth_logout "$phase" "$iter" "$log_file"
      ;;
    repo-create)
      local repo_name="bench-repo-create-$RUN_ID-$phase-$iter"
      local repo
      repo="$(repo_full_name "$repo_name")"
      register_repo "$repo"
      measure_cmd "$log_file" gh repo create "$repo" --private --add-readme
      delete_repo_quietly "$repo"
      ;;
    repo-view)
      measure_cmd "$log_file" gh repo view "$REPO_READ_REPO" --json name --jq '.name'
      ;;
    repo-clone)
      local clone_dir="$WORK_ROOT/clones/repo-clone-$phase-$iter"
      rm -rf "$clone_dir"
      if [[ "$REPO_CLONE_MODE" == "gh" ]]; then
        measure_cmd "$log_file" gh repo clone "$REPO_READ_REPO" "$clone_dir"
      else
        measure_cmd "$log_file" git clone -q "http://$HOST/$REPO_READ_REPO.git" "$clone_dir"
      fi
      rm -rf "$clone_dir"
      ;;
    issue-create)
      measure_cmd "$log_file" gh issue create --repo "$ISSUE_REPO" --title "Bench issue $RUN_ID $phase $iter" --body "bench issue body $iter"
      ;;
    issue-list)
      measure_cmd "$log_file" gh issue list --repo "$ISSUE_REPO" --limit 50
      ;;
    issue-view)
      measure_cmd "$log_file" gh issue view "$ISSUE_VIEW_NUMBER" --repo "$ISSUE_REPO"
      ;;
    pr-create)
      local repo_name="bench-pr-create-$RUN_ID-$phase-$iter"
      local repo clone_dir branch
      repo="$(repo_full_name "$repo_name")"
      clone_dir="$WORK_ROOT/clones/pr-create-$phase-$iter"
      branch="bench-pr-create-$RUN_ID-$phase-$iter"
      register_repo "$repo"
      create_repo_with_readme "$repo"
      clone_repo "$repo" "$clone_dir"
      setup_git_identity "$clone_dir"
      git -C "$clone_dir" checkout -b "$branch" >/dev/null
      git -C "$clone_dir" commit --allow-empty -m "bench: prepare pr create $iter" >/dev/null
      git -C "$clone_dir" push -u origin "$branch" >/dev/null
      measure_in_dir "$clone_dir" "$log_file" gh pr create --repo "$repo" --base main --head "$branch" --title "Bench PR Create $iter" --body "fixture"
      delete_repo_quietly "$repo"
      rm -rf "$clone_dir"
      ;;
    pr-list)
      measure_cmd "$log_file" gh pr list --repo "$PR_READ_REPO" --limit 50
      ;;
    pr-view)
      measure_cmd "$log_file" gh pr view "$PR_VIEW_NUMBER" --repo "$PR_READ_REPO"
      ;;
    pr-merge)
      local repo_name="bench-pr-merge-$RUN_ID-$phase-$iter"
      local repo clone_dir pr_url
      repo="$(repo_full_name "$repo_name")"
      clone_dir="$WORK_ROOT/clones/pr-merge-$phase-$iter"
      register_repo "$repo"
      create_repo_with_readme "$repo"
      clone_repo "$repo" "$clone_dir"
      setup_git_identity "$clone_dir"
      git -C "$clone_dir" checkout -b feature-merge >/dev/null
      printf 'merge fixture %s\n' "$iter" >"$clone_dir/merge.txt"
      git -C "$clone_dir" add merge.txt >/dev/null
      git -C "$clone_dir" commit -m "bench: prepare pr merge $iter" >/dev/null
      git -C "$clone_dir" push -u origin feature-merge >/dev/null
      pr_url="$(cd "$clone_dir" && gh pr create --repo "$repo" --base main --head feature-merge --title "Bench PR Merge $iter" --body "fixture")"
      measure_cmd "$log_file" gh pr merge "$pr_url" --repo "$repo" --merge
      delete_repo_quietly "$repo"
      rm -rf "$clone_dir"
      ;;
    workflow-list)
      measure_cmd "$log_file" gh workflow list --repo "$WORKFLOW_REPO"
      ;;
    workflow-run)
      measure_cmd "$log_file" gh workflow run 'Bench Workflow' --repo "$WORKFLOW_REPO" --ref main
      ;;
    run-view)
      measure_cmd "$log_file" gh run view "$RUN_VIEW_ID" --repo "$WORKFLOW_REPO"
      ;;
    *)
      fail "scenario dispatch not implemented: $scenario"
      ;;
  esac
}

summarize_raw_file() {
  local raw_file="$1"
  sort -n "$raw_file" | awk '
    {
      a[NR] = $1
      sum += $1
    }
    END {
      if (NR == 0) {
        exit 1
      }
      p50_target = NR * 0.50
      p95_target = NR * 0.95
      p50_idx = int(p50_target)
      if (p50_idx < p50_target) p50_idx++
      p95_idx = int(p95_target)
      if (p95_idx < p95_target) p95_idx++
      if (p50_idx < 1) p50_idx = 1
      if (p95_idx < 1) p95_idx = 1
      if (p50_idx > NR) p50_idx = NR
      if (p95_idx > NR) p95_idx = NR
      printf "%d\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n", NR, sum / NR, a[p50_idx], a[p95_idx], a[1], a[NR]
    }
  '
}

print_summary_table() {
  printf '\n'
  printf '[bench-gh] results\n'
  awk -F'\t' '
    BEGIN {
      fmt = "%-16s %4s %10s %10s %10s %10s %10s\n"
      printf fmt, "scenario", "n", "mean_ms", "p50_ms", "p95_ms", "min_ms", "max_ms"
      printf fmt, "----------------", "----", "----------", "----------", "----------", "----------", "----------"
    }
    NR > 1 {
      printf fmt, $1, $2, $3, $4, $5, $6, $7
    }
  ' "$SUMMARY_FILE"
}

run_scenario() {
  local scenario="$1"
  local raw_file="$OUTPUT_DIR/${scenario}.raw.tsv"
  local log_file="$OUTPUT_DIR/${scenario}.log"
  local iter elapsed summary n mean p50 p95 min max elapsed_file

  : > "$raw_file"
  : > "$log_file"

  note "running $scenario (warmups=$WARMUPS runs=$RUNS)"

  for iter in $(seq 1 "$WARMUPS"); do
    scenario_iteration "$scenario" "warmup" "$iter" "$log_file" >/dev/null
  done

  for iter in $(seq 1 "$RUNS"); do
    elapsed_file="$WORK_ROOT/${scenario}-elapsed-${iter}.txt"
    if ! scenario_iteration "$scenario" "run" "$iter" "$log_file" >"$elapsed_file"; then
      fail "scenario $scenario failed on run iteration $iter (see $log_file)"
    fi
    elapsed="$(tr -d '\n' <"$elapsed_file")"
    printf '%s\n' "$elapsed" >>"$raw_file"
  done

  summary="$(summarize_raw_file "$raw_file")"
  IFS=$'\t' read -r n mean p50 p95 min max <<<"$summary"

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$scenario" "$n" "$mean" "$p50" "$p95" "$min" "$max" "$raw_file" >>"$SUMMARY_FILE"
  note "$scenario: n=$n mean=${mean}ms p50=${p50}ms p95=${p95}ms min=${min}ms max=${max}ms"
}

main() {
  parse_args "$@"

  if [[ "$LIST_SCENARIOS_ONLY" == "1" ]]; then
    list_scenarios
    exit 0
  fi

  validate_args
  prepare_environment
  preflight_server
  login_main_config
  configure_git_auth_helper
  prepare_fixtures

  local scenario
  for scenario in "${SCENARIOS[@]}"; do
    run_scenario "$scenario"
  done

  print_summary_table
  note "benchmarks complete"
  note "summary: $SUMMARY_FILE"
  if [[ "$KEEP_WORKDIR" == "1" ]]; then
    note "workdir preserved: $WORK_ROOT"
  fi
}

main "$@"
