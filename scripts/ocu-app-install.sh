#!/usr/bin/env bash
# ocu-app-install.sh — the single install engine for Open Computer Use.app.
#
# Used both by the CLI upgrade path (mac-agent's ocu-upgrade) and by the
# app's own in-app updater: the staged (new) app installs itself via the
# copy of this script bundled in Contents/Resources/.
#
# Every step before the swap is validated first; any failure leaves the
# destination untouched.
#
# Usage:
#   ocu-app-install.sh --staged-app PATH [options]
#   ocu-app-install.sh --rollback [options]
#
# Options:
#   --dest PATH              Install destination
#                            (default: "/Applications/Open Computer Use.app")
#   --expect-team TEAMID     Require a Developer ID signature from TEAMID
#   --expect-cdhash HEX      Require the given executable CDHash
#   --allow-adhoc            Accept ad-hoc signatures (default: any signature
#                            that passes codesign --verify --deep --strict)
#   --terminate-agent        Terminate running '__open-computer-use-app-agent'
#                            processes after the swap (TERM, then KILL)
#   --no-hooks               Skip pre-install / post-install hooks
#   --relaunch-after-pid PID Wait for PID to exit before swapping, then
#                            'open -n' the installed app (in-app upgrade)
#   --rollback               Restore the newest backup instead of installing
#   --backup PATH            With --rollback: restore this backup explicitly
#   --json                   Print a machine-readable result on stdout
#                            (progress messages go to stderr)
#
# Exit codes: 0 = installed / rolled back, 1 = operation failed,
# 2 = usage or staged-app validation failure.

set -euo pipefail

app_bundle_id="com.ifuryst.opencomputeruse"
default_dest="/Applications/Open Computer Use.app"
support_dir="${HOME}/Library/Application Support/OpenComputerUse"
hooks_dir="${support_dir}/hooks"
backup_dir="${support_dir}/backups"
agent_pattern='__open-computer-use-app-agent'

mode=""
staged_app=""
dest="${default_dest}"
expect_team=""
expect_cdhash=""
allow_adhoc=0
terminate_agent=0
no_hooks=0
relaunch_pid=""
rollback_backup=""
json=0

usage() {
  cat <<'EOF'
Usage:
  ocu-app-install.sh --staged-app PATH [options]
  ocu-app-install.sh --rollback [options]

Options:
  --dest PATH              Install destination
                           (default: "/Applications/Open Computer Use.app")
  --expect-team TEAMID     Require a Developer ID signature from TEAMID
  --expect-cdhash HEX      Require the given executable CDHash
  --allow-adhoc            Accept ad-hoc signatures (default: any signature
                           that passes codesign --verify --deep --strict)
  --terminate-agent        Terminate running '__open-computer-use-app-agent'
                           processes after the swap (TERM, then KILL)
  --no-hooks               Skip pre-install / post-install hooks
  --relaunch-after-pid PID Wait for PID to exit before swapping, then
                           'open -n' the installed app (in-app upgrade)
  --rollback               Restore the newest backup instead of installing
  --backup PATH            With --rollback: restore this backup explicitly
  --json                   Print a machine-readable result on stdout
                           (progress messages go to stderr)

Exit codes: 0 = installed / rolled back, 1 = operation failed,
2 = usage or staged-app validation failure.
EOF
}

log() {
  printf '%s\n' "$*" >&2
}

fail() {
  local code="${2:-1}"
  log "ocu-app-install: error: $*"
  if [[ "${json}" -eq 1 ]]; then
    printf '{"error":%s}\n' "$(json_string "$1")"
  fi
  exit "${code}"
}

json_string() {
  local value="$*"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  value="${value//$'\r'/}"
  printf '"%s"' "${value}"
}

require_path_arg() {
  local flag="$1"
  local value="${2:-}"
  if [[ -z "${value}" ]]; then
    usage >&2
    fail "${flag} requires a value" 2
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --staged-app)
      require_path_arg "$1" "${2:-}"
      staged_app="$2"
      shift 2
      ;;
    --dest)
      require_path_arg "$1" "${2:-}"
      dest="$2"
      shift 2
      ;;
    --expect-team)
      require_path_arg "$1" "${2:-}"
      expect_team="$2"
      shift 2
      ;;
    --expect-cdhash)
      require_path_arg "$1" "${2:-}"
      expect_cdhash="$(printf '%s' "$2" | tr 'A-F' 'a-f')"
      shift 2
      ;;
    --allow-adhoc)
      allow_adhoc=1
      shift
      ;;
    --terminate-agent)
      terminate_agent=1
      shift
      ;;
    --no-hooks)
      no_hooks=1
      shift
      ;;
    --relaunch-after-pid)
      require_path_arg "$1" "${2:-}"
      relaunch_pid="$2"
      shift 2
      ;;
    --rollback)
      mode="rollback"
      shift
      ;;
    --backup)
      require_path_arg "$1" "${2:-}"
      rollback_backup="$2"
      shift 2
      ;;
    --json)
      json=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown argument: $1"
      ;;
  esac
done

if [[ -n "${staged_app}" && -n "${mode}" ]]; then
  fail "--staged-app cannot be combined with --rollback"
fi
if [[ -z "${staged_app}" && -z "${mode}" ]]; then
  usage >&2
  fail "either --staged-app or --rollback is required"
fi
if [[ -n "${staged_app}" ]]; then
  mode="install"
fi
if [[ "${mode}" == "rollback" && -n "${relaunch_pid}" ]]; then
  fail "--relaunch-after-pid only applies to --staged-app"
fi
if [[ "${mode}" == "rollback" && -n "${staged_app}" ]]; then
  fail "--rollback does not take --staged-app"
fi

expect_flags=0
[[ -n "${expect_team}" ]] && expect_flags=$((expect_flags + 1))
[[ -n "${expect_cdhash}" ]] && expect_flags=$((expect_flags + 1))
[[ "${allow_adhoc}" -eq 1 ]] && expect_flags=$((expect_flags + 1))
if [[ "${expect_flags}" -gt 1 ]]; then
  fail "--expect-team, --expect-cdhash and --allow-adhoc are mutually exclusive"
fi
if [[ "${mode}" == "rollback" && "${expect_flags}" -gt 0 ]]; then
  fail "signing expectations only apply to --staged-app"
fi

plist_value() {
  local file="$1" key="$2" out
  if ! out="$(/usr/libexec/PlistBuddy -c "Print :${key}" "${file}" 2>/dev/null)"; then
    return 1
  fi
  printf '%s\n' "${out}"
}

codesign_details() {
  codesign -dvvv "$1" 2>&1 || true
}

first_cdhash() {
  sed -n 's/^.*CDHash=\([0-9a-fA-F]*\).*$/\1/p' | head -n 1
}

# Populate m_version / m_build / m_commit / m_cdhash / m_team for an app
# bundle (missing values become empty strings).
collect_app_metadata() {
  local app_path="$1" info details
  info="${app_path}/Contents/Info.plist"

  m_version=""
  m_build=""
  m_commit=""
  m_cdhash=""
  m_team=""

  [[ -f "${info}" ]] || return 0
  m_version="$(plist_value "${info}" CFBundleShortVersionString || true)"
  m_build="$(plist_value "${info}" CFBundleVersion || true)"
  m_commit="$(plist_value "${info}" OpenComputerUseGitCommit || true)"

  details="$(codesign_details "${app_path}")"
  m_cdhash="$(printf '%s\n' "${details}" | first_cdhash)"
  m_team="$(printf '%s\n' "${details}" | sed -n 's/^.*TeamIdentifier=\([^ ]*\).*$/\1/p' | head -n 1)"
  if [[ "${m_team}" == "not" ]]; then
    # 'TeamIdentifier=not set'
    m_team=""
  fi
}

metadata_json() {
  # Expects collect_app_metadata to have run.
  local build_json="null" commit_json="null" cdhash_json="null" team_json="null"
  [[ -n "${m_build}" ]] && build_json="$(json_string "${m_build}")"
  [[ -n "${m_commit}" ]] && commit_json="$(json_string "${m_commit}")"
  [[ -n "${m_cdhash}" ]] && cdhash_json="$(json_string "${m_cdhash}")"
  [[ -n "${m_team}" ]] && team_json="$(json_string "${m_team}")"
  printf '{"version":%s,"build":%s,"commit":%s,"cdhash":%s,"team":%s}' \
    "$(json_string "${m_version}")" "${build_json}" "${commit_json}" "${cdhash_json}" "${team_json}"
}

lsregister_path() {
  if command -v lsregister >/dev/null 2>&1; then
    command -v lsregister
    return 0
  fi
  local path="/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
  [[ -x "${path}" ]] || fail "lsregister not found"
  printf '%s\n' "${path}"
}

require_app_bundle() {
  local path="$1" label="$2" code="${3:-1}"
  if [[ ! -d "${path}" ]]; then
    fail "${label} is not a directory: ${path}" "${code}"
  fi
  if [[ ! -f "${path}/Contents/Info.plist" ]]; then
    fail "${label} is not an app bundle (missing Contents/Info.plist): ${path}" "${code}"
  fi
}

validate_staged_app() {
  local app_path="$1" info bundle_id variant

  require_app_bundle "${app_path}" "staged app" 2

  info="${app_path}/Contents/Info.plist"
  bundle_id="$(plist_value "${info}" CFBundleIdentifier || true)"
  if [[ "${bundle_id}" != "${app_bundle_id}" ]]; then
    fail "staged app bundle id is '${bundle_id}', expected '${app_bundle_id}'" 2
  fi

  variant="$(plist_value "${info}" OpenComputerUseAppVariant || true)"
  if [[ "${variant}" != "release" ]]; then
    fail "staged app variant is '${variant}', expected 'release' (refusing to install a dev build)" 2
  fi

  if ! codesign --verify --deep --strict "${app_path}" 2>&1; then
    fail "codesign --verify --deep --strict failed for staged app" 2
  fi

  if [[ -n "${expect_team}" ]]; then
    local requirement
    requirement="anchor apple generic and identifier \"${app_bundle_id}\" and certificate leaf[subject.OU] = \"${expect_team}\""
    if ! codesign --verify -R="${requirement}" "${app_path}" 2>&1; then
      fail "staged app does not satisfy signing requirement for team ${expect_team}" 2
    fi
    if command -v xcrun >/dev/null 2>&1; then
      if ! xcrun stapler validate "${app_path}" >/dev/null 2>&1; then
        fail "staged app is Developer ID signed but has no valid notarization staple" 2
      fi
    elif ! spctl -a -t exec -vv "${app_path}" >/dev/null 2>&1; then
      fail "staged app failed spctl gatekeeper assessment (no xcrun available)" 2
    fi
  fi

  if [[ -n "${expect_cdhash}" ]]; then
    collect_app_metadata "${app_path}"
    if [[ "$(printf '%s' "${m_cdhash}" | tr 'A-F' 'a-f')" != "${expect_cdhash}" ]]; then
      fail "staged app cdhash is '${m_cdhash}', expected '${expect_cdhash}'" 2
    fi
  fi
}

check_same_volume() {
  local staged_parent dest_parent staged_dev dest_dev
  staged_parent="$(cd "$(dirname "$1")" && pwd)"
  dest_parent="$(dirname "$2")"
  if [[ ! -d "${dest_parent}" ]]; then
    fail "destination parent directory does not exist: ${dest_parent}"
  fi
  staged_dev="$(stat -f %d "${staged_parent}")"
  dest_dev="$(stat -f %d "${dest_parent}")"
  if [[ "${staged_dev}" != "${dest_dev}" ]]; then
    fail "staged app and destination live on different volumes; atomic rename impossible"
  fi
}

strip_quarantine() {
  # Daemon-launched apps with quarantine attributes stall on a Gatekeeper
  # prompt nobody can answer; the staged copy is validated by this script
  # instead.
  if ! xattr -dr com.apple.quarantine "$1" 2>/dev/null; then
    fail "failed to strip com.apple.quarantine from staged app"
  fi
}

run_hook() {
  local name="$1" hook="${hooks_dir}/$1" status=0
  if [[ "${no_hooks}" -eq 1 ]]; then
    hooks_json="${hooks_json},\"${name}\":\"skipped\""
    return 0
  fi
  if [[ ! -x "${hook}" ]]; then
    hooks_json="${hooks_json},\"${name}\":\"absent\""
    return 0
  fi
  log "running hook: ${hook}"
  if [[ "${json}" -eq 1 ]]; then
    if ! "${hook}" >&2; then
      status=$?
    fi
  else
    if ! "${hook}"; then
      status=$?
    fi
  fi
  if [[ "${status}" -ne 0 ]]; then
    hooks_json="${hooks_json},\"${name}\":\"failed\""
    return "${status}"
  fi
  hooks_json="${hooks_json},\"${name}\":\"ok\""
  return 0
}

# Keep only the newest ${keep} entries matching a suffix in the backup dir.
prune_backups() {
  local suffix="$1" keep="${2:-2}" entries=()
  [[ -d "${backup_dir}" ]] || return 0
  while IFS= read -r entry; do
    entries+=("${entry}")
  done < <(ls -1t "${backup_dir}" 2>/dev/null | grep -- "${suffix}$" || true)
  local index
  for ((index = keep; index < ${#entries[@]}; index++)); do
    log "pruning old backup: ${entries[${index}]}"
    rm -rf "${backup_dir:?}/${entries[${index}]}"
  done
}

terminate_agents() {
  terminated_agents=0
  [[ "${terminate_agent}" -eq 1 ]] || return 0

  local pids
  pids="$(pgrep -f -- "${agent_pattern}" || true)"
  if [[ -z "${pids}" ]]; then
    log "no running app agent to terminate"
    return 0
  fi

  terminated_agents="$(printf '%s\n' "${pids}" | sed '/^$/d' | wc -l | tr -d ' ')"
  log "terminating ${terminated_agents} app agent process(es)"
  pkill -TERM -f -- "${agent_pattern}" 2>/dev/null || true

  local waited=0
  while (( waited < 50 )); do
    pgrep -f -- "${agent_pattern}" >/dev/null 2>&1 || return 0
    sleep 0.1
    waited=$((waited + 1))
  done

  log "agent still alive after 5s, sending KILL"
  pkill -KILL -f -- "${agent_pattern}" 2>/dev/null || true
  sleep 0.5
  if pgrep -f -- "${agent_pattern}" >/dev/null 2>&1; then
    log "warning: app agent survived SIGKILL; install itself succeeded but a stale agent may still be running"
  fi
}

wait_for_pid_exit() {
  local pid="$1" waited=0
  if ! kill -0 "${pid}" 2>/dev/null; then
    return 0
  fi
  log "waiting for pid ${pid} to exit before swapping"
  while kill -0 "${pid}" 2>/dev/null; do
    if (( waited >= 1200 )); then
      fail "pid ${pid} did not exit within 120s; destination left untouched"
    fi
    sleep 0.1
    waited=$((waited + 1))
  done
}

register_app() {
  local lsregister
  lsregister="$(lsregister_path)"
  if ! "${lsregister}" -f "$1" >/dev/null 2>&1; then
    log "warning: lsregister -f failed for $1 (non-fatal)"
  fi
}

hooks_json=""
terminated_agents=0
old_json="null"
new_json="null"
backup_json="null"
backup_path=""
old_metadata_known=0

if [[ "${mode}" == "install" ]]; then
  # --- Phase 1: validate everything before touching the destination. ---
  staged_dir="$(dirname "${staged_app}")"
  staged_base="$(basename "${staged_app}")"
  [[ -d "${staged_dir}" ]] || fail "staged app directory not found: ${staged_dir}"
  staged_app="$(cd "${staged_dir}" && pwd)/${staged_base}"
  require_app_bundle "${staged_app}" "staged app" 2
  [[ "${staged_app}" != "${dest}" ]] || fail "staged app and destination are the same path" 2
  validate_staged_app "${staged_app}"
  collect_app_metadata "${staged_app}"
  new_json="$(metadata_json)"
  check_same_volume "${staged_app}" "${dest}"
  strip_quarantine "${staged_app}"

  # --- In-app upgrade: the caller (the running app) exits first. ---
  if [[ -n "${relaunch_pid}" ]]; then
    wait_for_pid_exit "${relaunch_pid}"
  fi

  # --- Phase 2: hooks, backup, swap. ---
  if ! run_hook "pre-install"; then
    fail "pre-install hook failed; destination left untouched"
  fi

  mkdir -p "${backup_dir}"
  if [[ -e "${dest}" ]]; then
    require_app_bundle "${dest}" "existing destination"
    collect_app_metadata "${dest}"
    old_metadata_known=1
    backup_path="${backup_dir}/Open Computer Use.app.${m_version}-${m_build}-$(date +%Y%m%d%H%M%S).prev"
    log "backing up current app to ${backup_path}"
    mv "${dest}" "${backup_path}"
  else
    log "no existing app at ${dest} (fresh install)"
  fi
  if [[ "${old_metadata_known}" -eq 1 ]]; then
    collect_app_metadata "${backup_path}"
    old_json="$(metadata_json)"
  fi

  if ! mv "${staged_app}" "${dest}"; then
    # Best effort: put the backup back so the destination is not left missing.
    if [[ -n "${backup_path}" ]]; then
      mv "${backup_path}" "${dest}" || true
    fi
    fail "failed to move staged app into place"
  fi

  register_app "${dest}"
  terminate_agents

  if ! run_hook "post-install"; then
    log "post-install hook failed (recorded, not rolled back)"
  fi
  prune_backups '\.prev' 2

  if [[ -n "${relaunch_pid}" ]]; then
    log "relaunching ${dest}"
    open -n "${dest}" || log "warning: failed to relaunch ${dest}"
  fi
else
  # --- Rollback mode. ---
  if [[ -n "${rollback_backup}" ]]; then
    require_app_bundle "${rollback_backup}" "backup"
    backup_path="${rollback_backup}"
  else
    [[ -d "${backup_dir}" ]] || fail "no backups directory at ${backup_dir}; nothing to roll back to"
    latest_backup="$(ls -1t "${backup_dir}" 2>/dev/null | grep -- '\.prev$' | head -n 1 || true)"
    [[ -n "${latest_backup}" ]] || fail "no .prev backup found in ${backup_dir}"
    backup_path="${backup_dir}/${latest_backup}"
    require_app_bundle "${backup_path}" "backup"
  fi

  collect_app_metadata "${backup_path}"
  new_json="$(metadata_json)"

  if [[ -e "${dest}" ]]; then
    require_app_bundle "${dest}" "current destination"
    collect_app_metadata "${dest}"
    old_json="$(metadata_json)"
    mkdir -p "${backup_dir}"
    rolled_path="${backup_dir}/Open Computer Use.app.${m_version}-${m_build}-$(date +%Y%m%d%H%M%S).rolled-back"
    log "moving current app aside to ${rolled_path}"
    mv "${dest}" "${rolled_path}"
  fi

  if ! mv "${backup_path}" "${dest}"; then
    if [[ -n "${rolled_path:-}" ]]; then
      mv "${rolled_path}" "${dest}" || true
    fi
    fail "failed to restore backup"
  fi

  register_app "${dest}"
  terminate_agents
  prune_backups '\.prev' 2
fi

if [[ -n "${backup_path}" ]]; then
  backup_json="$(json_string "${backup_path}")"
fi

log "done: ${mode} completed -> ${dest}"

if [[ "${json}" -eq 1 ]]; then
  printf '{"mode":%s,"dest":%s,"old":%s,"new":%s,"backup":%s,"terminated_agents":%s,"hooks":{%s}}\n' \
    "$(json_string "${mode}")" "$(json_string "${dest}")" \
    "${old_json}" "${new_json}" "${backup_json}" "${terminated_agents}" \
    "${hooks_json#,}"
fi
