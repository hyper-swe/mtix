// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import "fmt"

// initializePrivateFileExclusions protects local state and private exports before
// init creates the store (FR-13.2, FR-19.1). Existing project rules are retained.
func initializePrivateFileExclusions(cwd string) error {
	if ignoreErr := addToGitignore(cwd, ".mtix/hooks.trust"); ignoreErr != nil {
		return fmt.Errorf("initialize exclusion: %w", ignoreErr)
	}

	// The store copy a refusal prints keeps the spelling of the aside directory.
	for _, pattern := range []string{".mtix/enroll-diff-*.json", ".mtix/enroll-lost-local-*.json", ".mtix/enrol-copy-*.db"} { //nolint:misspell // as above
		if ignoreErr := addToGitignore(cwd, pattern); ignoreErr != nil {
			return fmt.Errorf("ignore the enrolment reports and copies: %w", ignoreErr)
		}
	}
	if ignoreErr := addToGitignore(cwd, ".mtix/serve.json"); ignoreErr != nil {
		return fmt.Errorf("initialize exclusion: %w", ignoreErr)
	}

	if ignoreErr := addToGitignore(cwd, ".mtix/recovered-*.json"); ignoreErr != nil {
		return fmt.Errorf("initialize recovered-mail exclusion: %w", ignoreErr)
	}

	return nil
}
