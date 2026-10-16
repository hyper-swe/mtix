// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"unicode"

	"github.com/hyper-swe/mtix/internal/model"
)

// ShimName is the shim's file name in the operator-local directory.
const ShimName = "mtix-shim"

// shimRecordName is the operator-local record of what the shim runs.
const shimRecordName = "attach-shim"

// maxShimLinks bounds symbolic-link resolution in the shim and its Go mirror.
const maxShimLinks = 32

var (
	// ErrShimTargetRefused means the mtix the shim would run fails the
	// location, owner or mode rules (ADR-007 §6.5).
	ErrShimTargetRefused = errors.New("shim target refused")
	// ErrShimNotInstalled means there is no shim record to rewrite from.
	ErrShimNotInstalled = errors.New("attach shim is not installed")
)

// RefusalReason classifies a shim refusal so doctor can name an exact fix.
type RefusalReason string

// Shim refusal reasons.
const (
	// RefusedWritable: the path is writable by group or others.
	RefusedWritable RefusalReason = "writable"
	// RefusedOwner: the path is owned by neither the operator nor root.
	RefusedOwner RefusalReason = "owner"
	// RefusedNotExecutable: the final file has no execute bit.
	RefusedNotExecutable RefusalReason = "not-executable"
	// RefusedOther: the path is missing, not a regular file, not a
	// directory where one is needed, or has too many links.
	RefusedOther RefusalReason = "other"
)

// ShimRefusal is the error ValidateShimTarget returns for a refused target.
// Path is the component that was refused; it matches ErrShimTargetRefused.
type ShimRefusal struct {
	Path   string
	Reason RefusalReason
	Detail string
}

// Error implements error.
func (r *ShimRefusal) Error() string { return ErrShimTargetRefused.Error() + ": " + r.Detail }

// Is reports ErrShimTargetRefused as the sentinel of every refusal.
func (r *ShimRefusal) Is(target error) bool { return target == ErrShimTargetRefused }

// ShimRecord is what the shim records: the UNRESOLVED mtix path the operator
// uses, the operator's uid, and the attach protocol of the shim bytes.
type ShimRecord struct {
	Target   string `json:"target"`
	UID      int    `json:"uid"`
	Protocol int    `json:"protocol"`
}

func (r ShimRecord) validate() error {
	invalid := r.UID < 0 || r.Protocol < 1 || !path.IsAbs(r.Target) ||
		strings.ContainsAny(r.Target, "'\x00") ||
		strings.ContainsFunc(r.Target, unicode.IsControl)
	if invalid {
		return fmt.Errorf("shim record for %q: %w", r.Target, model.ErrInvalidInput)
	}
	return nil
}

// shimTemplate is the release-defined shim. Under a fixed PATH it walks the
// recorded path one component at a time from /, following at most 32
// symbolic links (file or directory links) and resolving .. physically.
// Every directory it enters (so every ancestor, and the directory holding
// every link) must be owned by the operator or root and not writable by
// group or others, unless it is a root-owned sticky directory; every link
// must be owned by the operator or root; the final file must be a regular
// executable owned by the operator or root and not writable by group or
// others. It then restores the caller's PATH and execs the physical path it
// checked, never a path through a link and never anything relative to the
// working directory. It holds no authority (ADR-007 §6.5).
const shimTemplate = `#!/bin/sh
# mtix attach shim, attach protocol %d. Release-defined bytes written by mtix;
# they hold no authority. mtix doctor --fix rewrites them.
set -f
mtix_shim_saved_path=${PATH-}
PATH=/usr/bin:/bin
mtix_shim_target='%s'
mtix_shim_uid=%d
mtix_shim_refuse() {
	printf 'mtix shim: refusing %%s: %%s\n' "$mtix_shim_target" "$1" >&2
	exit 126
}
# mtix_shim_owner PATH LS-LINE: the owner must be the operator or root.
mtix_shim_owner() {
	set -- "$1" $2
	case $4 in
	"$mtix_shim_uid" | 0) ;;
	*) mtix_shim_refuse "$1 is owned by uid $4, neither the operator nor root" ;;
	esac
}
# mtix_shim_dir DIR: a physical directory owned by the operator or root, not
# writable by group or others unless it is sticky and owned by root.
mtix_shim_dir() {
	mtix_shim_ls=$(ls -ldn "$1") || mtix_shim_refuse "cannot inspect $1"
	mtix_shim_owner "$1" "$mtix_shim_ls"
	case $mtix_shim_ls in
	?????w* | ????????w*)
		case $mtix_shim_ls in
		?????????[tT]*) set -- "$1" $mtix_shim_ls; [ "$4" = 0 ] || mtix_shim_refuse "$1 is writable by group or others" ;;
		*) mtix_shim_refuse "$1 is writable by group or others" ;;
		esac
		;;
	esac
}
mtix_shim_dir /
mtix_shim_cur=
mtix_shim_rest=$mtix_shim_target
mtix_shim_final=
mtix_shim_n=0
while [ -n "$mtix_shim_rest" ]; do
	case $mtix_shim_rest in /*) mtix_shim_rest=${mtix_shim_rest#/}; continue ;; esac
	mtix_shim_c=${mtix_shim_rest%%%%/*}
	case $mtix_shim_rest in */*) mtix_shim_rest=${mtix_shim_rest#*/} ;; *) mtix_shim_rest= ;; esac
	case $mtix_shim_c in
	. ) continue ;;
	.. ) mtix_shim_cur=${mtix_shim_cur%%/*}; continue ;;
	esac
	mtix_shim_p=$mtix_shim_cur/$mtix_shim_c
	mtix_shim_last=1
	case $mtix_shim_rest in *[!/]*) mtix_shim_last= ;; esac
	if [ -L "$mtix_shim_p" ]; then
		mtix_shim_n=$((mtix_shim_n + 1))
		[ "$mtix_shim_n" -le %d ] || mtix_shim_refuse "more than %d symbolic links"
		mtix_shim_owner "$mtix_shim_p" "$(ls -ldn "$mtix_shim_p")"
		mtix_shim_l=$(readlink "$mtix_shim_p") || mtix_shim_refuse "cannot read link $mtix_shim_p"
		[ -n "$mtix_shim_l" ] || mtix_shim_refuse "cannot read link $mtix_shim_p"
		case $mtix_shim_l in /*) mtix_shim_cur= ;; esac
		mtix_shim_rest=$mtix_shim_l/$mtix_shim_rest
	elif [ -n "$mtix_shim_last" ]; then
		mtix_shim_final=$mtix_shim_p
		mtix_shim_rest=
	elif [ -d "$mtix_shim_p" ]; then
		mtix_shim_dir "$mtix_shim_p"
		mtix_shim_cur=$mtix_shim_p
	else
		mtix_shim_refuse "$mtix_shim_p is not a directory"
	fi
done
[ -n "$mtix_shim_final" ] || mtix_shim_refuse "the recorded path does not name a file"
[ -f "$mtix_shim_final" ] || mtix_shim_refuse "$mtix_shim_final is not a regular file"
[ -x "$mtix_shim_final" ] || mtix_shim_refuse "$mtix_shim_final is not executable"
mtix_shim_ls=$(ls -ldn "$mtix_shim_final") || mtix_shim_refuse "cannot inspect $mtix_shim_final"
mtix_shim_owner "$mtix_shim_final" "$mtix_shim_ls"
case $mtix_shim_ls in
?????w* | ????????w*) mtix_shim_refuse "$mtix_shim_final is writable by group or others" ;;
esac
PATH=$mtix_shim_saved_path
exec "$mtix_shim_final" "$@"
`

// ShimScript renders the release-defined shim bytes for rec.
func ShimScript(rec ShimRecord) ([]byte, error) {
	if err := rec.validate(); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(shimTemplate, rec.Protocol, rec.Target, rec.UID, maxShimLinks, maxShimLinks)), nil
}

// ShimStore is the operator-local access the shim writer needs;
// *oplocal.State implements it.
type ShimStore interface {
	RecordStore
	Path() (string, error)
	WriteExecutable(name string, data []byte) (string, error)
	ReadExecutable(name string) ([]byte, string, error)
}

// ShimWriter installs, checks and rewrites the shim. It is the Writer
// named "shim" in the refresh registry.
type ShimWriter struct {
	store    ShimStore
	current  int
	validate func(target string, uid int) (string, error)
}

// NewShimWriter builds the shim writer for a binary at protocol current;
// validate is ValidateShimTarget in production.
func NewShimWriter(store ShimStore, current int, validate func(string, int) (string, error)) *ShimWriter {
	return &ShimWriter{store: store, current: current, validate: validate}
}

// Name implements Writer.
func (w *ShimWriter) Name() string { return "shim" }

// Install records target (unresolved) and uid, then writes the shim. The
// operator directory path must contain no whitespace, and the target must
// pass ValidateShimTarget. It returns the shim's path.
func (w *ShimWriter) Install(_ context.Context, target string, uid int) (string, error) {
	dir, err := w.store.Path()
	if err != nil {
		return "", fmt.Errorf("locate operator directory: %w", err)
	}
	if strings.ContainsFunc(dir, unicode.IsSpace) {
		return "", fmt.Errorf("shim directory %q contains whitespace: %w", dir, model.ErrInvalidInput)
	}
	if _, err := w.validate(target, uid); err != nil {
		return "", fmt.Errorf("install shim: %w", err)
	}
	return w.write(ShimRecord{Target: target, UID: uid, Protocol: w.current})
}

func (w *ShimWriter) write(rec ShimRecord) (string, error) {
	data, err := ShimScript(rec)
	if err != nil {
		return "", err
	}
	if writeErr := w.store.WriteJSON(shimRecordName, rec); writeErr != nil {
		return "", fmt.Errorf("record shim target: %w", writeErr)
	}
	p, err := w.store.WriteExecutable(ShimName, data)
	if err != nil {
		return "", fmt.Errorf("write shim: %w", err)
	}
	return p, nil
}

func (w *ShimWriter) record() (ShimRecord, bool, error) {
	var rec ShimRecord
	reports, err := w.store.ReadJSON(shimRecordName, &rec)
	if err != nil {
		return ShimRecord{}, false, fmt.Errorf("read shim record: %w", err)
	}
	if len(reports) > 0 || rec.Target == "" {
		return ShimRecord{}, false, nil
	}
	return rec, true, nil
}

// Write rewrites the shim from its record at the current protocol.
func (w *ShimWriter) Write(_ context.Context) error {
	rec, ok, err := w.record()
	if err != nil {
		return err
	}
	if !ok {
		return ErrShimNotInstalled
	}
	rec.Protocol = w.current
	_, err = w.write(rec)
	return err
}

// Check reports the shim's state for doctor and the refresh pass.
func (w *ShimWriter) Check(_ context.Context) (ArtifactStatus, error) {
	rec, ok, err := w.record()
	if err != nil {
		return ArtifactStatus{}, err
	}
	data, p, readErr := w.store.ReadExecutable(ShimName)
	present := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ArtifactStatus{State: ArtifactRefused, Path: p, Detail: "the shim cannot be read: " + readErr.Error(), Fix: "rm " + p}, nil
	}
	switch {
	case !ok && !present:
		return ArtifactStatus{State: ArtifactAbsent, Path: p}, nil
	case !ok:
		return ArtifactStatus{State: ArtifactRefused, Path: p, Detail: "the shim has no operator-local record of its target", Fix: "rm " + p}, nil
	}
	if _, refusal := w.validate(rec.Target, rec.UID); refusal != nil {
		return ArtifactStatus{State: ArtifactRefused, Path: p, Detail: refusal.Error(), Fix: targetFix(rec.Target, p, refusal)}, nil
	}
	rec.Protocol = w.current
	want, err := ShimScript(rec)
	if err != nil || !present || !bytes.Equal(data, want) {
		return ArtifactStatus{State: ArtifactStale, Path: p, Detail: fmt.Sprintf("the shim does not match attach protocol %d", w.current)}, nil
	}
	return ArtifactStatus{State: ArtifactCurrent, Path: p, Detail: "the shim runs " + rec.Target}, nil
}

// targetFix names the exact remedy for a refusal: the refused component,
// not the recorded path (ADR-007 §7 exact fixes).
func targetFix(target, shimPath string, refusal error) string {
	var r *ShimRefusal
	if !errors.As(refusal, &r) || r.Path == "" {
		return fmt.Sprintf("install mtix at %s, or remove the shim: rm %s", target, shimPath)
	}
	switch r.Reason {
	case RefusedWritable:
		return "chmod go-w " + r.Path
	case RefusedOwner:
		return fmt.Sprintf("remove %s or make it owned by you or root (rm or chown), so the path to mtix holds nothing another user controls", r.Path)
	case RefusedNotExecutable:
		return "chmod u+x " + r.Path
	default:
		return fmt.Sprintf("install mtix at %s (refused: %s), or remove the shim: rm %s", target, r.Path, shimPath)
	}
}
