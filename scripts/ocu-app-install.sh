#!/usr/bin/env bash
# ocu-app-install.sh — the single install engine for Open Computer Use.app.
#
# Used both by the CLI upgrade path (mac-agent's ocu-upgrade) and by the
# app's own in-app updater: the staged (new) app installs itself via the
# copy of this script bundled in Contents/Resources/.
#
# Every step before the swap is validated first; any failure leaves the
# destination untouched. Once the current destination has been moved aside,
# the EXIT trap restores it if anything still fails (or the script is
# signalled) before the replacement move commits.
#
# Backup bookkeeping: backups get unique names and are recorded, in creation
# order, in backups/index.txt. Rollback restores the backup recorded by the
# last install (support dir install-state.json); a fresh install (no
# previous app) rolls back to the pre-install absent state instead of
# picking an arbitrary historical backup.
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
#   --rollback               Restore the newest backup instead of installing;
#                            a fresh install rolls back to the absent state
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
backup_index="${backup_dir}/index.txt"
state_file="${support_dir}/install-state.json"
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
  --rollback               Restore the newest backup instead of installing;
                           a fresh install rolls back to the absent state
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
  local value="$*" char out="" code
  local -i i
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  for ((i = 0; i < ${#value}; i++)); do
    char="${value:i:1}"
    case "${char}" in
      $'\b') out+="\\b" ;;
      $'\t') out+="\\t" ;;
      $'\n') out+="\\n" ;;
      $'\v') out+="\\u000b" ;;
      $'\f') out+="\\f" ;;
      $'\r') out+="\\r" ;;
      *)
        # Escape the remaining JSON-forbidden control characters
        # (U+0000-U+001F). UTF-8 lead/continuation bytes are all >= 0x80,
        # so anything below 0x20 here is exactly such a control character.
        printf -v code '%d' "'${char}"
        if ((code < 32)); then
          printf -v char '\\u%04x' "${code}"
        fi
        out+="${char}"
        ;;
    esac
  done
  printf '"%s"' "${out}"
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
      shift
      ;;
    --rollback)
      mode="rollback"
      shift
      ;;
    --backup)
      require_path_arg "$1" "${2:-}"
      rollback_backup="$2"
      shift
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

  if ! codesign --verify --deep --strict "${app_path}"; then
    fail "codesign --verify --deep --strict failed for staged app" 2
  fi

  if [[ -n "${expect_team}" ]]; then
    local requirement
    requirement="anchor apple generic and identifier \"${app_bundle_id}\" and certificate leaf[subject.OU] = \"${expect_team}\""
    if ! codesign --verify -R="${requirement}" "${app_path}"; then
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

device_of() {
  stat -f %d "$1"
}

# The staged app, the destination parent, and the backup directory must all
# live on one volume: the design relies on rename(2) being atomic, and an
# interruption mid copy-and-delete is not recoverable.
check_same_volume() {
  local staged_parent dest_parent staged_dev dest_dev
  staged_parent="$(cd "$(dirname "$1")" && pwd)"
  dest_parent="$(dirname "$2")"
  if [[ ! -d "${dest_parent}" ]]; then
    fail "destination parent directory does not exist: ${dest_parent}"
  fi
  staged_dev="$(device_of "${staged_parent}")"
  dest_dev="$(device_of "${dest_parent}")"
  if [[ "${staged_dev}" != "${dest_dev}" ]]; then
    fail "staged app and destination live on different volumes; atomic rename impossible"
  fi
}

check_dir_on_dest_volume() {
  local dir="$1" label="$2" dir_dev dest_dev
  dest_dev="$(device_of "$(dirname "${dest}")")"
  dir_dev="$(device_of "${dir}")"
  if [[ "${dir_dev}" != "${dest_dev}" ]]; then
    fail "${label} and destination live on different volumes; refusing non-atomic ${label} moves"
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
    "${hook}" >&2 || status=$?
  else
    "${hook}" || status=$?
  fi
  if [[ "${status}" -ne 0 ]]; then
    hooks_json="${hooks_json},\"${name}\":\"failed\""
    return "${status}"
  fi
  hooks_json="${hooks_json},\"${name}\":\"ok\""
  return 0
}

# --- Backup bookkeeping (M3: unique names, recorded creation order). ---

unique_child_name() {
  # $1 = name base without suffix, $2 = suffix ("prev"/"rolled-back").
  # Second-resolution timestamps can repeat; never reuse a name and never
  # mv into an existing directory (which would nest instead of rename).
  local base="$1" suffix="$2"
  local candidate="${base}.${suffix}"
  local n=0
  while [[ -e "${candidate}" ]]; do
    n=$((n + 1))
    candidate="${base}.${n}.${suffix}"
  done
  printf '%s\n' "${candidate}"
}

record_backup() {
  printf '%s\n' "$(basename "$1")" >> "${backup_index}"
}

list_contains() {
  local needle="$1" item
  for item in "${@:2}"; do
    [[ "${item}" == "${needle}" ]] && return 0
  done
  return 1
}

# Keep only the newest ${keep} recorded entries per backup class. Ordering
# comes from backups/index.txt (recorded creation order), never from the
# bundled app's preserved mtime: a restored older build must not sort behind
# a newer backup. Backups that predate the index are appended in lexical
# (name-timestamp) order so retention keeps working across the upgrade.
prune_backups() {
  local keep="${1:-2}" name idx
  [[ -f "${backup_index}" ]] || return 0

  local -a on_disk=()
  while IFS= read -r name; do
    [[ -n "${name}" ]] && on_disk+=("${name}")
  done < "${backup_index}"

  # Fold legacy on-disk backups into the index once.
  local -a legacy=()
  while IFS= read -r name; do
    if ! list_contains "${name}" ${on_disk[@]+"${on_disk[@]}"}; then
      legacy+=("${name}")
    fi
  done < <(ls -1 "${backup_dir}" 2>/dev/null | grep -e '\.prev$' -e '\.rolled-back$' || true)
  if [[ ${#legacy[@]} -gt 0 ]]; then
    local -a sorted_legacy=()
    while IFS= read -r name; do
      sorted_legacy+=("${name}")
    done < <(printf '%s\n' "${legacy[@]}" \
      | sed 's/^.*-\([0-9]\{14\}\)\.\(prev\|rolled-back\)$/\1 &/' \
      | sort \
      | sed 's/^[0-9]\{14\} //')
    for ((idx = 0; idx < ${#sorted_legacy[@]}; idx++)); do
      on_disk+=("${sorted_legacy[${idx}]}")
    done
  fi

  # Everything on disk but not in the index (external tampering aside, this
  # cannot happen): drop it from rotation.
  local -a pruned=()
  local -a classes=(".prev" ".rolled-back")
  local suffix entry total
  for suffix in "${classes[@]}"; do
    local -a matching=()
    for ((idx = 0; idx < ${#on_disk[@]}; idx++)); do
      entry="${on_disk[${idx}]}"
      if [[ -d "${backup_dir}/${entry}" && "${entry}" == *"${suffix}" ]]; then
        matching+=("${entry}")
      fi
    done
    total=${#matching[@]}
    for ((idx = 0; idx < total - keep; idx++)); do
      pruned+=("${matching[${idx}]}")
    done
  done

  local -a kept=()
  for ((idx = 0; idx < ${#on_disk[@]}; idx++)); do
    entry="${on_disk[${idx}]}"
    if [[ -d "${backup_dir}/${entry}" ]] && ! list_contains "${entry}" ${pruned[@]+"${pruned[@]}"}; then
      kept+=("${entry}")
    fi
  done

  for ((idx = 0; idx < ${#pruned[@]}; idx++)); do
    entry="${pruned[${idx}]}"
    log "pruning old backup: ${entry}"
    rm -rf "${backup_dir:?}/${entry}" 2>/dev/null || log "warning: failed to prune ${entry}"
  done

  if [[ ${#kept[@]} -eq 0 ]]; then
    : > "${backup_index}"
  else
    printf '%s\n' "${kept[@]}" > "${backup_index}"
  fi
}

# --- Install state (M2: the pre-install absent state is explicit). ---

write_install_state() {
  # $1 = had previous app (0/1), $2 = backup path or "".
  local had_previous="$1" prev_backup="$2" backup_field="null"
  [[ -n "${prev_backup}" ]] && backup_field="$(json_string "${prev_backup}")"
  printf '{"dest":%s,"had_previous":%s,"backup":%s,"ts":%s}\n' \
    "$(json_string "${dest}")" \
    "$([[ "${had_previous}" -eq 1 ]] && printf 'true' || printf 'false')" \
    "${backup_field}" \
    "$(json_string "$(date +%Y%m%d%H%M%S)")" > "${state_file}.tmp"
  mv "${state_file}.tmp" "${state_file}"
}

terminate_agents() {
  terminated_agents=0
  [[ "${terminate_agent}" -eq 1 ]] || return 0

  local pids
  pids="$(pgrep -f "${agent_pattern}" || true)"
  if [[ -z "${pids}" ]]; then
    log "no running app agent to terminate"
    return 0
  fi

  terminated_agents="$(printf '%s\n' "${pids}" | sed '/^$/d' | wc -l | tr -d ' ')"
  log "terminating ${terminated_agents} app agent process(es)"
  pkill -TERM -f "${agent_pattern}" 2>/dev/null || true

  local waited=0
  while (( waited < 50 )); do
    pgrep -f "${agent_pattern}" >/dev/null 2>&1 || return 0
    sleep 0.1
    waited=$((waited + 1))
  done

  log "agent still alive after 5s, sending KILL"
  pkill -KILL -f "${agent_pattern}" 2>/dev/null || true
  sleep 0.5
  if pgrep -f "${agent_pattern}" >/dev/null 2>&1; then
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

# --- Interruption recovery (M4). ---
#
# Once the destination has been moved aside, any failure or signal before
# the replacement move commits must put it back; the brief two-rename gap is
# by design, an indefinitely missing destination is not.
displaced_path=""
swap_in_progress=0
swap_committed=0

recover_interrupted_swap() {
  if [[ "${swap_in_progress}" -eq 1 \
    && "${swap_committed}" -eq 0 \
    && -n "${displaced_path}" \
    && ! -e "${dest}" ]]; then
    log "recovering from interrupted swap: restoring ${displaced_path} -> ${dest}"
    if mv "${displaced_path}" "${dest}" 2>/dev/null; then
      log "recovery complete: destination restored"
    else
      log "warning: failed to restore ${displaced_path}; destination is left missing"
    fi
  fi
}

handle_signal() {
  log "interrupted by signal; aborting"
  exit 130
}

trap 'recover_interrupted_swap' EXIT
trap 'handle_signal' INT TERM

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

  # Create and volume-check every participating directory before the first
  # destructive operation.
  mkdir -p "${backup_dir}"
  check_same_volume "${staged_app}" "${dest}"
  check_dir_on_dest_volume "${backup_dir}" "backup directory"
  strip_quarantine "${staged_app}"

  # --- In-app upgrade: the caller (the running app) exits first. ---
  if [[ -n "${relaunch_pid}" ]]; then
    wait_for_pid_exit "${relaunch_pid}"
  fi

  # --- Phase 2: hooks, backup, swap. ---
  if ! run_hook "pre-install"; then
    fail "pre-install hook failed; destination left untouched"
  fi

  had_previous=0
  if [[ -e "${dest}" ]]; then
    require_app_bundle "${dest}" "existing destination"
    collect_app_metadata "${dest}"
    old_metadata_known=1
    had_previous=1
    displaced_path="$(unique_child_name "${backup_dir}/Open Computer Use.app.${m_version}-${m_build}-$(date +%Y%m%d%H%M%S)" "prev")"
    backup_path="${displaced_path}"
    log "backing up current app to ${backup_path}"
    swap_in_progress=1
    mv "${dest}" "${backup_path}"
    record_backup "${backup_path}"
  else
    log "no existing app at ${dest} (fresh install)"
  fi
  if [[ "${old_metadata_known}" -eq 1 ]]; then
    collect_app_metadata "${backup_path}"
    old_json="$(metadata_json)"
  fi

  if ! mv "${staged_app}" "${dest}"; then
    fail "failed to move staged app into place"
  fi
  swap_committed=1

  write_install_state "${had_previous}" "${backup_path}"

  register_app "${dest}"
  terminate_agents

  if ! run_hook "post-install"; then
    log "post-install hook failed (recorded, not rolled back)"
  fi
  prune_backups 2

  if [[ -n "${relaunch_pid}" ]]; then
    log "relaunching ${dest}"
    open -n "${dest}" || log "warning: failed to relaunch ${dest}"
  fi
else
  # --- Rollback mode. ---
  #
  # The restore source is chosen explicitly, never by bundle mtime:
  #   1. --backup PATH: restore exactly that bundle.
  #   2. The backup recorded by the last install (install-state.json). A
  #      fresh install recorded had_previous=false: rollback moves the
  #      installed app aside and returns to the pre-install absent state.
  #      A missing recorded backup is an error, not a licence to pick some
  #      unrelated historical backup.
  #   3. Installs that predate install-state.json: the newest recorded
  #      .prev backup.
  restore_to_absent=0
  restore_source=""

  [[ -d "$(dirname "${dest}")" ]] || fail "destination parent directory does not exist: $(dirname "${dest}")"

  if [[ -n "${rollback_backup}" ]]; then
    require_app_bundle "${rollback_backup}" "backup"
    check_dir_on_dest_volume "$(dirname "${rollback_backup}")" "backup directory"
    restore_source="${rollback_backup}"
  else
    mkdir -p "${backup_dir}"
    check_dir_on_dest_volume "${backup_dir}" "backup directory"

    recorded_dest=""
    recorded_had_previous=""
    recorded_backup=""
    if [[ -f "${state_file}" ]]; then
      recorded_dest="$(sed -n 's/^{"dest":"\(.*\)","had_previous".*/\1/p' "${state_file}")"
      recorded_had_previous="$(sed -n 's/.*"had_previous":\(true\|false\),.*/\1/p' "${state_file}")"
      if grep -q '"backup":null' "${state_file}"; then
        recorded_backup=""
      else
        recorded_backup="$(sed -n 's/.*"backup":"\(.*\)","ts".*/\1/p' "${state_file}")"
      fi
    fi

    if [[ "${recorded_dest}" == "${dest}" && -n "${recorded_had_previous}" ]]; then
      if [[ "${recorded_had_previous}" == "false" ]]; then
        log "last install was a fresh install; rolling back to the pre-install absent state"
        restore_to_absent=1
      elif [[ -n "${recorded_backup}" && -d "${recorded_backup}" ]]; then
        require_app_bundle "${recorded_backup}" "recorded backup"
        restore_source="${recorded_backup}"
      else
        fail "the backup recorded for the last install (${recorded_backup:-none}) no longer exists; refusing to restore an arbitrary historical backup"
      fi
    else
      # Legacy installs predate install-state.json: newest recorded .prev.
      latest_name=""
      while IFS= read -r name; do
        if [[ -d "${backup_dir}/${name}" && "${name}" == *.prev ]]; then
          latest_name="${name}"
        fi
      done < "${backup_index}" 2>/dev/null || true
      if [[ -z "${latest_name}" ]]; then
        latest_name="$(ls -1 "${backup_dir}" 2>/dev/null | grep -- '\.prev$' | sort | tail -n 1 || true)"
      fi
      [[ -n "${latest_name}" ]] || fail "no .prev backup found in ${backup_dir} and no install state recorded; nothing to roll back to"
      restore_source="${backup_dir}/${latest_name}"
      require_app_bundle "${restore_source}" "backup"
    fi
  fi

  if [[ "${restore_to_absent}" -eq 1 ]]; then
    if [[ -e "${dest}" ]]; then
      require_app_bundle "${dest}" "current destination"
      collect_app_metadata "${dest}"
      old_json="$(metadata_json)"
      displaced_path="$(unique_child_name "${backup_dir}/Open Computer Use.app.${m_version}-${m_build}-$(date +%Y%m%d%H%M%S)" "rolled-back")"
      backup_path="${displaced_path}"
      log "moving installed app aside to ${backup_path}"
      swap_in_progress=1
      mv "${dest}" "${backup_path}"
      record_backup "${backup_path}"
      swap_committed=1
    else
      log "destination already absent; nothing to roll back"
    fi
  else
    collect_app_metadata "${restore_source}"
    new_json="$(metadata_json)"

    if [[ -e "${dest}" ]]; then
      require_app_bundle "${dest}" "current destination"
      collect_app_metadata "${dest}"
      old_json="$(metadata_json)"
      displaced_path="$(unique_child_name "${backup_dir}/Open Computer Use.app.${m_version}-${m_build}-$(date +%Y%m%d%H%M%S)" "rolled-back")"
      backup_path="${displaced_path}"
      log "moving current app aside to ${backup_path}"
      swap_in_progress=1
      mv "${dest}" "${backup_path}"
      record_backup "${backup_path}"
    fi

    if ! mv "${restore_source}" "${dest}"; then
      fail "failed to restore backup"
    fi
    swap_committed=1
  fi

  if [[ -e "${dest}" ]]; then
    register_app "${dest}"
  fi
  terminate_agents
  prune_backups 2
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
