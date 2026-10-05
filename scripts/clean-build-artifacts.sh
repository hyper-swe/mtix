#!/usr/bin/env bash
# MTIX-122: remove only named build artifacts beneath this worktree.
# --check validates without deleting (used before npm or artifact writes).
set -euo pipefail

SCRIPT_DIR="$(dirname -- "${BASH_SOURCE[0]}")"
[ ! -L "$SCRIPT_DIR" ] || { echo 'refusing symlinked scripts directory' >&2; exit 1; }
ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
CHECK=false
if [ "${1:-}" = --check ]; then CHECK=true; shift; fi
[ "$#" -gt 0 ] || { echo 'expected fixed build artifact paths' >&2; exit 1; }

die() { echo "cleanup refused: $*" >&2; exit 1; }

validate() {
  local target="$1" version component current="$ROOT"
  case "$target" in
    internal/web/dist|web/dist|mtix|cover.out) ;;
    dist/mtix-agent-kit-v*)
      version="${target#dist/mtix-agent-kit-v}"
      [[ "$version" =~ ^[[:alnum:]][[:alnum:].+_-]*$ ]] && [[ "$version" != *..* ]] || die 'invalid agent-kit version'
      ;;
    *) die "not a fixed build artifact: $target" ;;
  esac
  local parts
  IFS=/ read -r -a parts <<< "$target"
  for component in "${parts[@]}"; do
    current="$current/$component"
    [ ! -L "$current" ] || die "symlink at $current"
  done
  current="$ROOT/$target"
  [ -e "$current" ] || return 0
  case "$target" in
    mtix|cover.out) [ -f "$current" ] || die "expected regular file: $target" ;;
    *) [ -d "$current" ] || die "expected directory: $target" ;;
  esac
  # Refuse links and special files before deleting ANY part of the tree.
  [ -z "$(find "$current" ! -type f ! -type d -print)" ] || die "unsupported entry in $target"
}

# Validate the whole request first, so a later invalid path cannot cause a
# partial cleanup. Concurrent replacement of build directories is unsupported.
for target in "$@"; do validate "$target"; done
$CHECK && exit 0
for target in "$@"; do
  path="$ROOT/$target"
  [ -e "$path" ] || continue
  find "$path" -mindepth 1 -print
  find "$path" -type f -print -delete
  if [ -d "$path" ]; then find "$path" -depth -type d -empty -print -delete; fi
done
