// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Id lists bound to SQL (MTIX-187.7.15).
//
// A sync event id (or a uid or task number taken from one) is stored as the
// bytes received, whatever they are (founder decision MTIX-187.6, option 3).
// json.Marshal replaces invalid UTF-8 with U+FFFD, so an id list bound as a
// plain JSON array of strings reaches json_each changed and misses the row it
// names: a pending event is not selected, a push hold is never released, a
// task event does not wait for its creation. Every statement that matches ids
// through json_each therefore binds hexIDList and reads the list back with
//
//	SELECT CAST(unhex(value) AS TEXT) FROM json_each(?)
//
// The subquery keeps the primary-key probe of IN (SELECT ...), needs no
// parameter per id and no variable limit, and unhex returns the exact bytes
// (CAST to TEXT keeps NUL and invalid UTF-8). The statements spell the
// subquery out as a literal, as the SQL rules require; idListSubquery is the
// reference text that TestSQLiteIDLists_NeverBindPlainJSONLists looks for.
const idListSubquery = "SELECT CAST(unhex(value) AS TEXT) FROM json_each(?)"

// hexIDList returns ids as a JSON array of hex strings, the argument of
// idListSubquery. Any byte sequence is representable.
func hexIDList(ids []string) (string, error) {
	hexed := make([]string, len(ids))
	for i, id := range ids {
		hexed[i] = hex.EncodeToString([]byte(id))
	}
	list, err := json.Marshal(hexed)
	if err != nil {
		return "", fmt.Errorf("encode id list: %w", err)
	}
	return string(list), nil
}
