#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"${repo_root}/scripts/check-docs.sh"
"${repo_root}/scripts/check-repo-hygiene.sh"
"${repo_root}/scripts/check-action-pinning.sh"

while IFS= read -r file; do
  bash -n "$file"
done < <(find "${repo_root}/scripts" -type f -name '*.sh' | sort)

while IFS= read -r file; do
  node --check "$file"
done < <(find "${repo_root}/scripts" -type f -name '*.mjs' | sort)

if command -v go >/dev/null 2>&1; then
  # 平台 runtime 模块只在匹配宿主 OS 时编译（Windows 模块依赖 x/sys/windows，
  # Linux 模块的未打 tag 文件使用 syscall.Stat_t，跨 OS 都无法构建）。
  case "$(uname -s)" in
    Linux*)
      (
        cd "${repo_root}/apps/OpenComputerUseLinux"
        go test ./...
      )
      ;;
    MINGW*|MSYS*|CYGWIN*)
      (
        cd "${repo_root}/apps/OpenComputerUseWindows"
        go test ./...
      )
      ;;
  esac
  (
    cd "${repo_root}/scripts/releasetool"
    go vet ./...
    go build ./...
  )
fi

echo "基础 CI 检查通过"
