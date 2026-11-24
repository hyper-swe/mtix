# Decision v1 fixtures

`decision-wire-v1.json` describes the `decision` member of a journal comment. `decision-resource-v1.json` describes a card with `schema: "mtix.decision/1"`. Resolve its references against the wire schema in this directory. Both use JSON Schema Draft 2020-12.

- `fixtures/request.json` and `fixtures/request-duplicates.json` have the same canonical representation. Feed the duplicate fixture to the ingest parser as **raw bytes**. At each object level the last member wins. An author or push validator must reject that same input.
- `fixtures/request-hashes.json` gives the lowercase SHA-256 digests. The payload hash includes unknown fields. The identity hash removes only the top-level `request_key` and `supersedes` before RFC 8785 serialization. Apply typed defaults after hashing.
- `fixtures/answer.json`, `outcome.json`, `refused.json`, `withdraw.json` and `answered-elsewhere.json` illustrate the remaining event variants. `fixtures/tty-session-vectors.json` supplies literal JSON tokens to insert into `evidence.tty_session`. Omission means the answer has no controlling terminal.
- `fixtures/resource-open.json` is a card for the example request. The request event ID is the answer's `target`.

The schemas check decoded structures. The model also checks raw size, nesting depth, I-JSON encoding, UTF-8 byte caps, parallel ticket arrays and cross-field references. Answer validation needs the request to check option input, required notes and opened link indexes. Unknown display values remain visible. A future version or unknown authority value makes a request unsupported and excludes a dependent event from the fold.

Canonical size: the 64 KiB cap applies to the RFC 8785 form of the `decision` value, not to the raw bytes, so a number kept as `1e300` by one transport and expanded to digits by another gets the same verdict. A 4 MiB raw bound only limits parse work. Unicode noncharacters (U+FDD0..U+FDEF, U+xFFFE, U+xFFFF), literal or escaped, are refused in names and values (RFC 7493 section 2.1).

`fixtures/resource-invalid-missing-*.json` are cards that must FAIL `decision-resource-v1.json`: each omits one field the schema requires of an `open` card. A card whose `raised_at` is `null` (a legacy request id) is valid. State-specific members (`effective_answer` for answered and applied, `outcome` for applied, `withdrawn`, `superseded_by`, `invalid_reason`) are required by the schema for their state.

The schemas are checked with any external JSON Schema 2020-12 validator; the Go tests do not run one, they pin the required-field lists of the card schema (`internal/model/decision_schema_test.go`).
