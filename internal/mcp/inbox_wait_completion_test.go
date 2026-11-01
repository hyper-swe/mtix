package mcp

import "time"

// inboxWaitCompletionHangBound is the failure ceiling for every inbox_wait
// test wait that expects a POSITIVE completion (a response, a worker exit, a
// join). Healthy runs return as soon as the event happens; the bound only
// decides how long a hung run waits before failing, so it is generous enough
// for a loaded CI runner. It must never be used for a negative window (a
// check that nothing happens within some time), which keeps its own short
// duration.
const inboxWaitCompletionHangBound = 10 * time.Second
