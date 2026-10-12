// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

// Live stock ACL capture is test-only. It never modifies an ancestor ACL and
// never describes synthetic descriptors as captured fixtures.
package oplocal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type stockACLCapture struct {
	Schema          int           `json:"schema"`
	HelperVersion   string        `json:"helper_version"`
	CapturedUTC     string        `json:"captured_utc"`
	GoVersion       string        `json:"go_version"`
	ImageOS         string        `json:"image_os"`
	ImageVersion    string        `json:"image_version"`
	WindowsVersion  string        `json:"windows_version"`
	WindowsBuild    uint32        `json:"windows_build"`
	UserSID         string        `json:"user_sid"`
	HeadSHA         string        `json:"head_sha"`
	CheckoutSHA     string        `json:"checkout_sha"`
	Ref             string        `json:"ref"`
	RunID           string        `json:"run_id"`
	RunAttempt      string        `json:"run_attempt"`
	RunURL          string        `json:"run_url"`
	IdentityError   string        `json:"identity_error,omitempty"`
	ProvenanceError string        `json:"provenance_error,omitempty"`
	Rows            []stockACLRow `json:"rows"`
}

type stockACLRow struct {
	LogicalPath    string `json:"logical_path"`
	SDDL           string `json:"sddl"`
	ReadError      string `json:"read_error,omitempty"`
	ParseError     string `json:"parse_error,omitempty"`
	PolicyError    string `json:"policy_error,omitempty"`
	ReplayError    string `json:"replay_error,omitempty"`
	CloseError     string `json:"close_error,omitempty"`
	PolicyAccepted bool   `json:"policy_accepted"`
	ReplayAccepted bool   `json:"replay_accepted"`
}

func TestAncestor_StockWindowsAclCapture_AcceptsPolicy(t *testing.T) {
	output := os.Getenv("MTIX_WINDOWS_CAPTURE_DIR")
	if output == "" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("Windows CI requires an explicit capture output directory")
		}
		t.Skip("live stock capture requires an explicit owned output directory")
	}
	capture := stockCaptureProvenance()
	user, identityErr := windows.GetCurrentProcessToken().GetTokenUser()
	var sid *windows.SID
	if identityErr == nil {
		sid = user.User.Sid
		capture.UserSID = sid.String()
	} else {
		capture.IdentityError = identityErr.Error()
	}
	capture.ProvenanceError = stockCaptureProvenanceError(capture)
	home, homeErr := os.UserHomeDir()
	for _, path := range []struct{ label, path string }{{"volume_root", `C:\`}, {"users_root", `C:\Users`}, {"runner_profile", home}} {
		row := captureStockACL(path.label, path.path, sid)
		if path.label == "runner_profile" && homeErr != nil {
			row.ReadError = "runner profile lookup failed"
		}
		capture.Rows = append(capture.Rows, row)
	}
	// Preserve every readable raw row, including rejected policies, before any
	// assertion reports failure. The artifact is diagnostic evidence, not GREEN.
	if err := writeStockCapture(output, capture); err != nil {
		t.Fatalf("preserve stock capture: %v", err)
	}
	if capture.ProvenanceError != "" {
		t.Error(capture.ProvenanceError)
	}
	if capture.IdentityError != "" {
		t.Errorf("stock capture identity: %s", capture.IdentityError)
	}
	for _, row := range capture.Rows {
		if !row.PolicyAccepted || !row.ReplayAccepted || row.ReadError != "" || row.ParseError != "" || row.CloseError != "" {
			t.Errorf("stock ACL %s failed: read=%q parse=%q policy=%q replay=%q close=%q", row.LogicalPath, row.ReadError, row.ParseError, row.PolicyError, row.ReplayError, row.CloseError)
		}
	}
}

func stockCaptureProvenance() stockACLCapture {
	version := windows.RtlGetVersion()
	return stockACLCapture{
		Schema: 1, HelperVersion: "stock-acl-v1", CapturedUTC: time.Now().UTC().Format(time.RFC3339Nano), GoVersion: runtime.Version(),
		ImageOS: os.Getenv("ImageOS"), ImageVersion: os.Getenv("ImageVersion"),
		WindowsVersion: fmt.Sprintf("%d.%d", version.MajorVersion, version.MinorVersion), WindowsBuild: version.BuildNumber,
		HeadSHA: os.Getenv("MTIX_WINDOWS_HEAD_SHA"), CheckoutSHA: os.Getenv("MTIX_WINDOWS_CHECKOUT_SHA"), Ref: os.Getenv("MTIX_WINDOWS_REF"),
		RunID: os.Getenv("MTIX_WINDOWS_RUN_ID"), RunAttempt: os.Getenv("MTIX_WINDOWS_RUN_ATTEMPT"), RunURL: os.Getenv("MTIX_WINDOWS_RUN_URL"),
	}
}

func stockCaptureProvenanceError(capture stockACLCapture) string {
	for _, value := range []string{capture.CapturedUTC, capture.GoVersion, capture.ImageOS, capture.ImageVersion, capture.WindowsVersion, capture.UserSID, capture.HeadSHA, capture.CheckoutSHA, capture.Ref, capture.RunID, capture.RunAttempt, capture.RunURL} {
		if strings.TrimSpace(value) == "" {
			return "stock capture provenance is incomplete"
		}
	}
	if capture.WindowsBuild == 0 {
		return "stock capture Windows build is unavailable"
	}
	return ""
}

func captureStockACL(label, path string, user *windows.SID) (row stockACLRow) {
	row.LogicalPath = label
	handles, err := captureStockDirectory(path)
	defer func() {
		if closeErr := closeStockHandles(handles); closeErr != nil {
			row.CloseError = closeErr.Error()
		}
	}()
	if err != nil {
		row.ReadError = err.Error()
		return row
	}
	descriptor, err := windows.GetSecurityInfo(handles[len(handles)-1], windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		row.ReadError = fmt.Errorf("read owner and DACL: %w", err).Error()
		return row
	}
	row.SDDL = descriptor.String()
	if row.SDDL == "" {
		row.ParseError = "native SDDL serialization returned empty"
		return row
	}
	parsed, err := windows.SecurityDescriptorFromString(row.SDDL)
	if err != nil {
		row.ParseError = fmt.Errorf("parse native SDDL: %w", err).Error()
	}
	if user == nil {
		row.PolicyError = "current process SID unavailable"
		row.ReplayError = row.PolicyError
		return row
	}
	if err = verifyAncestorAccess(descriptor, user); err != nil {
		row.PolicyError = err.Error()
	} else {
		row.PolicyAccepted = true
	}
	if parsed != nil {
		if err = verifyAncestorAccess(parsed, user); err != nil {
			row.ReplayError = err.Error()
		} else {
			row.ReplayAccepted = true
		}
	}
	return row
}

// Retain the entire read-only handle chain. Relative native opens refuse
// reparses before traversal; each opened handle must describe a real directory.
// No private-state final-DACL requirement is imposed on these stock ancestors.
func captureStockDirectory(path string) ([]windows.Handle, error) {
	volume := filepath.VolumeName(path)
	if !filepath.IsAbs(path) || volume == "" {
		return nil, errors.New("stock directory must be absolute")
	}
	text, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return nil, fmt.Errorf("encode stock volume: %w", err)
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	root, err := windows.CreateFile(text, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("open stock volume: %w", err)
	}
	handles := []windows.Handle{root}
	if err = stockDirectoryType(root); err != nil {
		return handles, err
	}
	parts := strings.TrimLeft(strings.TrimPrefix(filepath.Clean(path), volume), `\`)
	if parts == "" {
		return handles, nil
	}
	for _, part := range strings.Split(parts, `\`) {
		next, openErr := captureStockChild(handles[len(handles)-1], part, access, share)
		if openErr != nil {
			return handles, openErr
		}
		handles = append(handles, next)
		if err = stockDirectoryType(next); err != nil {
			return handles, err
		}
	}
	return handles, nil
}

func captureStockChild(parent windows.Handle, name string, access, share uint32) (windows.Handle, error) {
	text, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, fmt.Errorf("encode stock component: %w", err)
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: text, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	var result windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, &attrs, &result, nil, windows.FILE_ATTRIBUTE_NORMAL, share, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open stock component: %w", ntError(err))
	}
	return handle, nil
}

func stockDirectoryType(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("read stock directory metadata: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("stock component is not a non-reparse directory")
	}
	return nil
}

func closeStockHandles(handles []windows.Handle) error {
	var result error
	for i := len(handles) - 1; i >= 0; i-- {
		if err := windows.CloseHandle(handles[i]); err != nil {
			result = errors.Join(result, fmt.Errorf("close stock directory handle: %w", err))
		}
	}
	return result
}

func writeStockCapture(output string, capture stockACLCapture) error {
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return fmt.Errorf("encode stock capture: %w", err)
	}
	if err = os.WriteFile(filepath.Join(output, "capture.json"), append(data, '\n'), 0600); err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return fmt.Errorf("write stock capture: %w", err)
	}
	return nil
}
