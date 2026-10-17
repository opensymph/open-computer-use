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
# The manifest only asserts what was actually verified: signature validity
# (deep/strict), signing mode derived from the certificate chain (never a
# bare TeamIdentifier heuristic), a validated notarization staple when
# --notarized true, and nonempty version/build/commit/cdhash metadata.
# Any check failure aborts before the three artifacts are written.
#
# Usage: package-macos-app.sh --app PATH --out-dir DIR
#          [--channel stable|prerelease] [--notarized true|false]

set -euo pipefail

app_path=""
out_dir=""
channel="stable"
notarized="false"

usage() {
  sed -n '2,21p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
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

fail() {
  echo "package-macos-app: error: $*" >&2
  exit 1
}

# --- Trust and metadata checks (all before any artifact is written). ---

codesign --verify --deep --strict "${app_path}" \
  || fail "codesign --verify --deep --strict failed for ${app_path}"

signature_details="$(codesign -dvvv "${app_path}" 2>&1)"

if [[ "${notarized}" == "true" ]]; then
  # The caller's assertion alone never ends up in the manifest: the
  # notarization staple must validate. Generic spctl gatekeeper assessment
  # is not notarization evidence (it follows local policy and approvals, and
  # trivially passes where Gatekeeper is disabled), so without stapler there
  # is no way to prove notarization and the build fails closed.
  command -v xcrun >/dev/null 2>&1 \
    || fail "notarized=true was requested but xcrun/stapler is unavailable; cannot validate the notarization staple of ${app_path}"
  xcrun stapler validate "${app_path}" >/dev/null 2>&1 \
    || fail "notarized=true was requested but stapler validate failed for ${app_path}"
fi

info="${app_path}/Contents/Info.plist"
bundle_id="$(plist_value "${info}" CFBundleIdentifier)"
[[ "${bundle_id}" == "com.ifuryst.opencomputeruse" ]] \
  || fail "unexpected CFBundleIdentifier '${bundle_id}' (expected com.ifuryst.opencomputeruse)"

version="$(plist_value "${info}" CFBundleShortVersionString)"
bundle_version="$(plist_value "${info}" CFBundleVersion)"
commit="$(plist_value "${info}" OpenComputerUseGitCommit)"
[[ -n "${version}" && -n "${bundle_version}" && -n "${commit}" ]] \
  || fail "missing required Info.plist metadata (version='${version}' bundle_version='${bundle_version}' commit='${commit}')"

team_id="$(printf '%s\n' "${signature_details}" | sed -n 's/^.*TeamIdentifier=\([^ ]*\).*$/\1/p' | head -n 1)"
if [[ "${team_id}" == "not" ]]; then
  team_id=""
fi

signing_mode=""
if printf '%s\n' "${signature_details}" | grep -q '^Signature=adhoc'; then
  signing_mode="adhoc"
elif printf '%s\n' "${signature_details}" | grep -q '^Authority=Developer ID Application:'; then
  signing_mode="developer-id"
elif printf '%s\n' "${signature_details}" | grep -q '^Authority=Apple Development:'; then
  signing_mode="apple-development"
fi
[[ -n "${signing_mode}" ]] || fail "cannot classify the signing certificate chain of ${app_path} (expected ad-hoc, Developer ID Application, or Apple Development)"

cdhash_arm64="$(arch_cdhash "${app_path}" arm64)"
cdhash_x86_64="$(arch_cdhash "${app_path}" x86_64)"
[[ "${cdhash_arm64}" =~ ^[0-9a-fA-F]{40}$ ]] || fail "missing or invalid arm64 CDHash '${cdhash_arm64}'"
[[ "${cdhash_x86_64}" =~ ^[0-9a-fA-F]{40}$ ]] || fail "missing or invalid x86_64 CDHash '${cdhash_x86_64}'"

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
