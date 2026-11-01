// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that staged script content never supplies a path (MTIX-187.7.7).
package hooks

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestHostileIDs_NeverCreateFilesOutsideBaseDir(t *testing.T) { testHostileStaging(t, false) }
func TestHostileIDs_NeverCreateSurpriseFilenames(t *testing.T)   { testHostileStaging(t, true) }

// hostileScriptPaths are operator-supplied script paths; staging must keep only the basename.
var hostileScriptPaths = []string{"approved.sh", "../escape.sh", "/abs/dir/abs.sh", "a/b/../../c.sh", "dir/.hidden", "x\\y.sh"}

func testHostileStaging(t *testing.T, names bool) {
	t.Helper()
	for _, f := range append(testutil.HostileIDs(), testutil.HostileText()...) {
		for _, mode := range []os.FileMode{0o700, 0o600} {
			for _, script := range hostileScriptPaths {
				t.Run(fmt.Sprintf("%s/%o/%s", f.Name, mode, script), func(t *testing.T) {
					root := t.TempDir()
					base := filepath.Join(root, "base")
					// IDs/titles are only content; the path is operator-selected.
					input := scriptInput{path: script, content: []byte(f.Value), mode: mode}
					target, err := (stager{}).stage(base, input)
					require.NoError(t, err)
					kind := "-r"
					if mode&0o111 != 0 {
						kind = "-x"
					}
					digest := fmt.Sprintf("%x%s", sha256.Sum256(input.content), kind)
					name := filepath.Base(script)
					require.Equal(t, filepath.Join(base, digest, name), target)
					assertStagedTree(t, root, digest, name, names)
					got, err := os.ReadFile(target)
					require.NoError(t, err)
					require.Equal(t, input.content, got)
				})
			}
		}
	}
}

func assertStagedTree(t *testing.T, root, digest, name string, names bool) {
	t.Helper()
	allowed := []string{".", "base", filepath.Join("base", digest), filepath.Join("base", digest, name)}
	require.NoError(t, filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		require.Contains(t, allowed, rel, "outside the staging tree")
		require.Zero(t, e.Type()&os.ModeSymlink)
		if names {
			require.Regexp(t, `^(\.|base|[0-9a-f]{64}-[rx]|[^/]+)$`, e.Name())
		}
		return nil
	}))
}
