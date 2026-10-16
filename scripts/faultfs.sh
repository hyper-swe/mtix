#!/usr/bin/env bash
# Copyright 2025-2026 HyperSWE
# SPDX-License-Identifier: Apache-2.0
#
# faultfs.sh — create/destroy the tiny dedicated volume the NFR-2.8
# fault-injection suite (e2e/faultinject) fills to capacity.
#
# Usage:
#   scripts/faultfs.sh create    # prints the mount point
#   scripts/faultfs.sh destroy
#
#   MTIX_FAULTFS_DIR=$(scripts/faultfs.sh create) \
#     go test ./e2e/faultinject/ -tags=faultinject -count=1 -v
#
# Linux uses a 16 MiB tmpfs (needs sudo); macOS uses a 16 MiB HFS+ RAM disk.

set -euo pipefail

SIZE_MB=16
LINUX_MOUNT=/mnt/mtix-faultfs
MAC_VOLNAME=MTIXFAULTFS
MAC_DEV_FILE="${TMPDIR:-/tmp}/mtix-faultfs.dev"

create_linux() {
  sudo mkdir -p "$LINUX_MOUNT"
  if ! mountpoint -q "$LINUX_MOUNT"; then
    sudo mount -t tmpfs -o "size=${SIZE_MB}m,mode=1777" tmpfs "$LINUX_MOUNT"
  fi
  echo "$LINUX_MOUNT"
}

destroy_linux() {
  if mountpoint -q "$LINUX_MOUNT"; then
    sudo umount "$LINUX_MOUNT"
  fi
  sudo rmdir "$LINUX_MOUNT" 2>/dev/null || true
}

create_mac() {
  if [ -d "/Volumes/$MAC_VOLNAME" ]; then
    echo "/Volumes/$MAC_VOLNAME"
    return
  fi
  local sectors=$((SIZE_MB * 2048)) # 512-byte sectors
  local dev
  dev=$(hdiutil attach -nomount "ram://${sectors}" | tr -d '[:space:]')
  echo "$dev" > "$MAC_DEV_FILE"
  diskutil erasevolume HFS+ "$MAC_VOLNAME" "$dev" >/dev/null
  echo "/Volumes/$MAC_VOLNAME"
}

destroy_mac() {
  if [ -d "/Volumes/$MAC_VOLNAME" ]; then
    diskutil unmount "/Volumes/$MAC_VOLNAME" >/dev/null || true
  fi
  if [ -f "$MAC_DEV_FILE" ]; then
    hdiutil detach "$(cat "$MAC_DEV_FILE")" >/dev/null 2>&1 || true
    rm -f "$MAC_DEV_FILE"
  fi
}

case "${1:-}" in
  create)
    if [ "$(uname)" = "Darwin" ]; then create_mac; else create_linux; fi
    ;;
  destroy)
    if [ "$(uname)" = "Darwin" ]; then destroy_mac; else destroy_linux; fi
    ;;
  *)
    echo "usage: $0 {create|destroy}" >&2
    exit 2
    ;;
esac
