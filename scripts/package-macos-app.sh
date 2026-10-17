#!/usr/bin/env bash
# package-macos-app.sh — turn a built Open Computer Use.app into the
# distributable macOS app set:
#
#   Open-Computer-Use-macOS.zip   (ditto -c -k --keepParent)
#   SHA256SUMS.txt
#   app-manifest.json             {version, bundle_version, commit, sha256,
#                                  signing{mode, team_id, notarized},
#                                  cdhash{arm64, x86_64}, channel}
#
# Asset names are fixed (no version in the file name) so
# releases/latest/download/Open-Computer-Use-macOS.zip keeps working.
#
# Usage: package-macos-app.sh --app PATH --out-dir DIR
#          [--channel stable|prerelease] [--notarized true|false]

set -euo pipefail

app_path=""
out_dir=""
channel="stable"
notarized="false"

usage() {
  sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --app)
      app_path="${2:-}"
      if [[ -z "${app_path}" ]]; then
        echo "--app requires a value" >&2
        exit 2
      fi
      shift 2
      ;;
    --out-dir)
      out_dir="${2:-}"
      if [[ -z "${out_dir}" ]]; then
        echo "--out-dir requires a value" >&2
        exit 2
      fi
      shift 2
      ;;
    --channel)
      channel="${2:-}"
      if [[ "${channel}" != "stable" && "${channel}" != "prerelease" ]]; then
        echo "--channel must be stable or prerelease" >&2
        exit 2
      fi
      shift 2
      ;;
    --notarized)
      notarized="${2:-}"
      if [[ "${notarized}" != "true" && "${notarized}" != "false" ]]; then
        echo "--notarized must be true or false" >&2
        exit 2
      fi
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${app_path}" || -z "${out_dir}" ]]; then
  usage >&2
  echo "--app and --out-dir are required" >&2
  exit 2
fi

if [[ ! -d "${app_path}" || ! -f "${app_path}/Contents/Info.plist" ]]; then
  echo "Not an app bundle: ${app_path}" >&2
  exit 1
fi

plist_value() {
  /usr/libexec/PlistBuddy -c "Print :$2" "$1" 2>/dev/null || true
}

arch_cdhash() {
  codesign -dvvv --arch "$2" "$1" 2>&1 \
    | sed -n 's/^.*CDHash=\([0-9a-fA-F]*\).*$/\1/p' \
    | head -n 1
}

info="${app_path}/Contents/Info.plist"
version="$(plist_value "${info}" CFBundleShortVersionString)"
bundle_version="$(plist_value "${info}" CFBundleVersion)"
commit="$(plist_value "${info}" OpenComputerUseGitCommit)"
team_id="$(codesign -dvvv "${app_path}" 2>&1 | sed -n 's/^.*TeamIdentifier=\([^ ]*\).*$/\1/p' | head -n 1)"
if [[ "${team_id}" == "not" ]]; then
  team_id=""
fi

signing_mode="adhoc"
if [[ -n "${team_id}" && "${OPEN_COMPUTER_USE_CODESIGN_MODE:-}" != "adhoc" ]]; then
  signing_mode="developer-id"
fi

cdhash_arm64="$(arch_cdhash "${app_path}" arm64)"
cdhash_x86_64="$(arch_cdhash "${app_path}" x86_64)"

mkdir -p "${out_dir}"
zip_name="Open-Computer-Use-macOS.zip"
zip_path="${out_dir}/${zip_name}"

ditto -c -k --keepParent "${app_path}" "${zip_path}"
(cd "${out_dir}" && shasum -a 256 "${zip_name}" > SHA256SUMS.txt)

sha256="$(awk '{print $1}' "${out_dir}/SHA256SUMS.txt")"

MANIFEST_CHANNEL="${channel}" \
MANIFEST_VERSION="${version}" \
MANIFEST_BUNDLE_VERSION="${bundle_version}" \
MANIFEST_COMMIT="${commit}" \
MANIFEST_SHA256="${sha256}" \
MANIFEST_SIGNING_MODE="${signing_mode}" \
MANIFEST_TEAM_ID="${team_id}" \
MANIFEST_NOTARIZED="${notarized}" \
MANIFEST_CDHASH_ARM64="${cdhash_arm64}" \
MANIFEST_CDHASH_X86_64="${cdhash_x86_64}" \
MANIFEST_OUT_DIR="${out_dir}" \
/usr/bin/python3 - <<'PY'
import json
import os

manifest = {
    "version": os.environ["MANIFEST_VERSION"],
    "bundle_version": os.environ["MANIFEST_BUNDLE_VERSION"],
    "commit": os.environ["MANIFEST_COMMIT"],
    "sha256": os.environ["MANIFEST_SHA256"],
    "channel": os.environ["MANIFEST_CHANNEL"],
    "signing": {
        "mode": os.environ["MANIFEST_SIGNING_MODE"],
        "team_id": os.environ["MANIFEST_TEAM_ID"],
        "notarized": os.environ["MANIFEST_NOTARIZED"] == "true",
    },
    "cdhash": {
        "arm64": os.environ["MANIFEST_CDHASH_ARM64"],
        "x86_64": os.environ["MANIFEST_CDHASH_X86_64"],
    },
}

with open(os.path.join(os.environ["MANIFEST_OUT_DIR"], "app-manifest.json"), "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")
PY
