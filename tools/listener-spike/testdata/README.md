# Registry fixtures

These JSONL files are synthetic samples of a session registry record
(`~/.claude/sessions/<pid>.json`), one sample per line. They are not
recordings: the key names, the idle/busy `status` and the epoch-millisecond
`statusUpdatedAt` follow the keys observed in a real scratch session during
the run-1 spike (see the board notes on MTIX-112.40.1), and the identifiers
(pid 4242, the zero-prefixed session id, `/scratch/run` paths) are made up so
no real session data is stored. A scrubbed recording from the live matrix run
replaces them when that run exists.
