#!/usr/bin/env bash
# Linux-runnable regression tests for installer behavior that is otherwise
# macOS-specific. The F1 case extracts and calls the production bookkeeping
# functions; the F2 case stops before macOS tools would be needed.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
installer="${repo_root}/scripts/ocu-app-install.sh"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

fail() {
  printf 'test-ocu-app-install: %s\n' "$*" >&2
  exit 1
}

test_legacy_backups_do_not_prune_new_indexed_backup() {
  local backup_dir="${tmpdir}/backups"
  local backup_index="${backup_dir}/index.txt"
  local newest="2-2-20261017000000.prev"

  mkdir -p "${backup_dir}/1-1-20260101000000.prev"
  mkdir -p "${backup_dir}/1-1-20260102000000.prev"
  mkdir -p "${backup_dir}/1-1-20260103000000.prev"
  mkdir -p "${backup_dir}/${newest}"
  printf '%s\n' "${newest}" > "${backup_index}"

  # Shellcheck cannot source a subsection, but these functions are extracted
  # from the production installer so this checks its real retention logic.
  # shellcheck disable=SC1090
  source <(sed -n '/^list_contains()/,/^# --- Install state/p' "${installer}")
  log() { :; }
  prune_backups 2

  [[ -d "${backup_dir}/${newest}" ]] || fail "indexed backup was pruned"
  [[ -d "${backup_dir}/1-1-20260103000000.prev" ]] || fail "newest legacy backup was pruned"
  [[ ! -e "${backup_dir}/1-1-20260101000000.prev" ]] || fail "oldest legacy backup survived"
  [[ ! -e "${backup_dir}/1-1-20260102000000.prev" ]] || fail "second-oldest legacy backup survived"
  [[ "$(cat "${backup_index}")" == $'1-1-20260103000000.prev\n2-2-20261017000000.prev' ]] \
    || fail "backup index has an unexpected retention order"
}

test_relaunch_after_pid_parser() {
  local output status

  set +e
  output="$(bash "${installer}" --staged-app /nonexistent/x.app --relaunch-after-pid 123 2>&1)"
  status=$?
  set -e
  [[ ${status} -ne 0 ]] || fail "missing staged app unexpectedly installed"
  [[ "${output}" == *"staged app directory not found: /nonexistent"* ]] \
    || fail "numeric --relaunch-after-pid was not parsed: ${output}"

  set +e
  output="$(bash "${installer}" --staged-app /nonexistent/x.app --relaunch-after-pid nope 2>&1)"
  status=$?
  set -e
  [[ ${status} -eq 2 ]] || fail "non-numeric relaunch PID returned ${status}, expected 2"
  [[ "${output}" == *"--relaunch-after-pid requires a numeric PID"* ]] \
    || fail "non-numeric relaunch PID was accepted: ${output}"
}

test_legacy_backups_do_not_prune_new_indexed_backup
test_relaunch_after_pid_parser
printf 'ocu app installer regressions passed\n'
