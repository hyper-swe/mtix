// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

// ReadHostID reads an existing identifier without creating state or an identifier.
// An absent directory or host-id is reported as an empty identifier.
func (s *State) ReadHostID() (id string, err error) {
	dir, err := s.directory(false)
	if missingState(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closeWith(dir, &err)
	id, err = s.hostID(dir, false)
	if missingState(err) {
		return "", nil
	}
	return id, err
}

// PermissionStatePresent detects protected permission records without creating files.
// Invalid existing records remain errors rather than being mistaken for absence.
func (s *State) PermissionStatePresent() (present bool, err error) {
	dir, err := s.directory(false)
	if missingState(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closeWith(dir, &err)
	names := []string{"grants.json", "permissions-pending.json", "permissions-replays.json", confirmationFilename(0), confirmationFilename(1), confirmationFilename(2)}
	for _, name := range names {
		_, readErr := readData(dir, name)
		if missingState(readErr) {
			continue
		}
		if readErr != nil {
			return false, readErr
		}
		return true, nil
	}
	return false, nil
}
