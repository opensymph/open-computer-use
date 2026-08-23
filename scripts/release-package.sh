#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${repo_root}/dist"
release_dir="${dist_dir}/release"
staging_dir="${release_dir}/npm-staging"
tarball_dir="${release_dir}/npm"

rm -rf "${release_dir}"
mkdir -p "${tarball_dir}"

node "${repo_root}/scripts/npm/build-packages.mjs" \
  --configuration release \
  --arch universal \
  --out-dir "${staging_dir}"

while IFS= read -r package_dir; do
  npm pack "${package_dir}" --pack-destination "${tarball_dir}" >/dev/null
done < <(find "${staging_dir}" -mindepth 1 -maxdepth 3 -name package.json -exec dirname {} \; | sort)

(cd "${repo_root}/scripts/releasetool" && go run . manifest "${release_dir}/release-manifest.json" "${repo_root}" "${tarball_dir}")

echo "${release_dir}"
