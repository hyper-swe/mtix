// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFileURIPathFor_OSParameterised runs the Windows drive-letter and UNC
// rewrite on every OS, because the branch is selected by the goos argument and
// not by the host (MTIX-112.2.4).
func TestFileURIPathFor_OSParameterised(t *testing.T) {
	tests := []struct {
		name, goos, path, want string
	}{
		{"windows drive backslash", "windows", `C:\x\mtix.db`, "/C:/x/mtix.db"},
		{"windows drive slash", "windows", "c:/x/mtix.db", "/c:/x/mtix.db"},
		{"windows drive with specials", "windows", `D:\a?b\c#d\e%f.db`, "/D:/a%3fb/c%23d/e%25f.db"},
		{"windows unc", "windows", `\\server\share\mtix.db`, "////server/share/mtix.db"},
		{"windows relative", "windows", `dir\mtix.db`, "dir/mtix.db"},
		{"windows digit before colon is not a drive", "windows", `1:\x\mtix.db`, "1:/x/mtix.db"},
		{"windows symbol before colon is not a drive", "windows", `_:/x/mtix.db`, "_:/x/mtix.db"},
		{"windows drive relative", "windows", `C:dir\mtix.db`, "C:dir/mtix.db"},
		{"linux plain", "linux", "/t/x.db", "/t/x.db"},
		{"linux colon path untouched", "linux", "C:/x.db", "C:/x.db"},
		{"linux backslash is a name byte", "linux", `/t/a\b.db`, `/t/a\b.db`},
		{"darwin specials", "darwin", "/t/a?b/c#d/e%f.db", "/t/a%3fb/c%23d/e%25f.db"},
		{"linux relative", "linux", "rel/x.db", "rel/x.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, fileURIPathFor(tt.path, tt.goos))
		})
	}
}

// TestFileURIPath_UsesHostOS pins the wrapper to the running OS.
func TestFileURIPath_UsesHostOS(t *testing.T) {
	for _, p := range []string{"/t/a?b.db", "rel/x#y.db", "/t/e%f.db"} {
		assert.Equal(t, fileURIPathFor(p, runtime.GOOS), fileURIPath(p))
	}
}
