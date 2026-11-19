// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"path/filepath"
)

// programInsideProject decides with the same physical answer launch uses
// (file identity, not spelling): a program inside the project, or one whose
// location cannot be decided, counts as inside, so the decision fails closed.
func programInsideProject(root, program string) bool {
	phys := physicalLocation(root, program)
	return !phys.Complete || phys.Inside
}

// reachesProject reports a program, absolute or a bare name looked up in the
// fixed PATH directories, whose physical location is inside the project or
// cannot be decided. A bare name that is not found is not reported here.
func reachesProject(root string, pathDirs []string, program string) bool {
	path := program
	if !filepath.IsAbs(program) {
		found, ok := lookPathFixed(pathDirs, program)
		if !ok {
			return false
		}
		path = found
	}
	return programInsideProject(root, path)
}

// envReachesProject reports a top-level env, written as a bare name or an
// absolute path, that reaches the project. A relative project path is a script
// and is judged by the pinning rules instead.
func envReachesProject(root, program string) bool {
	if !isEnv(program) || (!filepath.IsAbs(program) && !isBareName(program)) {
		return false
	}
	return reachesProject(root, filepath.SplitList(FixedHookPath(root)), program)
}

// insideProjectFix is the corrective step for a program that is reached
// inside the project: it must be pinned as a project script, never approved
// as an outside program.
func insideProjectFix(hook string) string {
	return "The program resolves inside the project or its location cannot be decided: name that file in the hook command as a project script so it is pinned (or remove the link that reaches it from outside), then approve the hook again: " + TrustFix(hook)
}

// insideProjectReview is the review of a program that is reached inside the
// project without being a pinned project script.
func insideProjectReview(hook, program string) ProgramReview {
	return ProgramReview{
		Class:   ClassOther,
		Needed:  true,
		Program: program,
		Detail:  fmt.Sprintf("hook %q runs %s, which resolves inside the project (or whose location cannot be decided) but is not a pinned project script, so mtix does not inspect it or what it runs", hook, program),
		Fix:     insideProjectFix(hook),
	}
}

// insideProjectError is the refusal of strict mode to approve a program that
// resolves inside the project; callers give it the pin fix, not an approval.
type insideProjectError struct{ launcher, resolved string }

func (e *insideProjectError) Error() string {
	return fmt.Sprintf("program %q resolves to %q inside the project; a project file is a script and must be pinned instead", e.launcher, e.resolved)
}
