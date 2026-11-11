# Registry fixtures

These JSONL files are synthetic samples of a session registry record
(`~/.claude/sessions/<pid>.json`), one sample per line. They are not
recordings: the key names, the idle/busy `status` and the epoch-millisecond
`statusUpdatedAt` follow the keys observed in a real scratch session during
the run-1 spike (see the board notes on MTIX-112.40.1), and the identifiers
(pid 4242, the zero-prefixed session id, `/scratch/run` paths) are made up so
no real session data is stored. A scrubbed recording from the live matrix run
replaces them when that run exists.

## registry-recorded.jsonl

Derived from a real recording. The run-1 spike sampled the registry file of
a real scratch session and logged, per status change, the key names present,
`status` and `statusUpdatedAt` (15 samples: a first sample without a status,
then strictly alternating idle and busy over about 13 minutes). This file
turns each logged sample back into a JSON object: the key set, the `status`
values and the `statusUpdatedAt` epoch milliseconds are the recorded ones.
The run logged no values for the other keys, so every other value is a
placeholder (pid 4242, a zero-prefixed session id, `/scratch/run` paths,
`redacted`, or 0 for timestamps). It contains no real path, user name, session
id or secret. The synthetic files above stay for the failure cases.
