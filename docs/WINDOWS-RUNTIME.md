# Windows operator-local runtime evidence

This maintainer workflow covers native Windows operator-local permission and ACL
checks plus hooks status. It adds no user configuration or production ACL policy.
It is separate from ordinary CI and never runs the full Windows suite.

## Capture-ready stage and merge conditions

Native Windows results and genuine stock ACL fixtures are **UNPROVEN** at the
capture-ready head. Local YAML checks and Windows cross-compilation establish
static structure and buildability only. They do not prove PowerShell execution,
stock ACL acceptance, Windows locking, or captured fixtures.

The approved sequence uses two separately reviewed heads on the same PR:

1. Review the bounded workflow and live capture helper, pass the complete local
   lint/race bar, then deliberately trigger the reviewed capture head.
2. Preserve and inspect the real hosted artifact, including failures. Add dated
   fixture data containing its exact raw SDDL, captured user SID and provenance
   only after this real capture exists.
   Fixture replay requires a second reviewed head with its own local bar and
   deliberate green native Windows run.
3. The maintainer merges only after genuine captured fixtures and native Windows
   GREEN are verified. Missing artifacts, setup failures, or policy rejection do
   not satisfy either condition. Representative ACL examples remain examples;
   do not relabel them as captured data or invent a capture date.

No dated fixture replay test is required by the first head because no genuine
fixture exists yet. The second head must add its real fixture test to both the
exact selection and required PASS manifest before the final native run.

## Reviewed runs and spending

Only a **non-draft** PR targeting main and the exact addition of the **windows-ci**
label starts this workflow. An unrelated label does not run it even if windows-ci
is already attached. It has no push, synchronize, schedule, automatic retry or
ready-for-review trigger. A new commit does not start another Windows run.
After reviewing a correction, remove and re-add windows-ci deliberately.

Manual workflow dispatch becomes available only after main contains the workflow.
Dispatch only a specifically reviewed ref. The initial pre-merge capture uses
the approved PR label event. Do not add another trigger to work around a failure.

One windows-latest job has a global budget of **15 minutes**; each selected
package command has **8 minutes**. There is no package/version matrix, PostgreSQL
service or full-suite expansion. Concurrency is keyed by PR number or dispatch
ref, with cancellation of an earlier overlapping run. Cancellation still counts
as an attempt and must appear in the duration report.

The workflow uses commit-pinned checkout, setup-go and artifact actions, a
read-only contents token and checkout without persistent credentials. Setup-go
reads go.mod rather than overriding the toolchain. No repository secret, write
token, installer, self-hosted runner, or failure-tolerant test step is needed.
Windows race execution requires CGO and a verified mingw-w64 runtime of at least
8, plus an actual synchronization library. Failed compiler setup fails the run;
CGO-disabled execution is not an acceptable substitute.

## Selected execution and interpretation

The checked-in workflow runs only the explicit anchored native permission/ACL
selection in internal/oplocal and `^TestHooksStatus_` in cmd/mtix, with race,
count=1, JSON output and package timeouts. The parsed workflow guard pins the
commands, required names, package identities, budgets and failure handling.

The live `TestAncestor_StockWindowsAclCapture_AcceptsPolicy` test reads C:\\,
C:\\Users and the actual runner profile. It captures OWNER and DACL information
from retained, read-only, reparse-aware directory handles. It tests the existing
ancestor allow-policy against each native descriptor and its exact SDDL parsed
back into a descriptor. It never changes a stock ACL or applies the private-state
DACL rule to stock ancestors. Every readable row is preserved before a policy
failure is reported. Read, parse, policy, replay and handle-close errors are
recorded separately under logical path labels.

A required test needs exactly one top-level PASS event for the exact package and
test name. Both packages need a successful package PASS and native command exit.
An empty selection, helper-only execution, malformed JSON, absent capture, or
required top-level SKIP fails. The live capture and protected-state roundtrip
cannot skip in this workflow.

There is one qualified top-level exception:
`TestEnsure_CanonicalHandleAlias_Refuses` remains selected and must execute once,
then report exactly one PASS or SKIP. A SKIP must include its existing reason,
“short path names are unavailable on this volume”, in the retained test output.
The run logs and raw JSON record the actual result; a SKIP leaves 8.3 alias
coverage unproven. Existing symlink/alias subcase skips must also be qualified
when interpreting coverage. They do not convert another required top-level SKIP
into success. Never infer unavailable platform coverage from a package PASS.

MTIX-130.9 Windows lock tests and MTIX-130.15 runtime cases are not present in
this selection. Their runtime remains unproven. Extend the allowlist only after
those changes supply genuinely implemented, reviewed Windows test names and a
required execution manifest. Running an absent name or a non-Windows helper is
not evidence for either ticket.

## Artifacts, provenance and failed runs

Download the artifact named for the exact run ID and attempt. It contains only
capture.json, oplocal.json, hooks.json, toolchain.json, duration.json and
started.txt; it never dumps a directory or the process environment. Preserve the
original bytes and their hashes before deriving a fixture.

capture.json records schema and helper version, actual UTC capture time, Go
version, ImageOS/ImageVersion, Windows version/build, captured current process
SID, PR head SHA, actual checked-out SHA, selected ref, run ID/attempt/URL and the
three logical labels with exact raw SDDL and individual outcomes. The captured
user SID is replay input. Personal profile paths and usernames do not belong in
capture metadata or committed fixture provenance. On PR events the checkout is
the integration merge commit; head SHA and merge SHA are distinct evidence.
Dispatch records the selected ref and actual checkout SHA.

Upload and duration steps run even when preceding work failed. A policy failure
keeps the real raw capture and fails the job. Do not weaken the allow-policy,
modify the runner's stock ACLs, substitute representative strings, retry
speculative heads, or widen the suite to hide a failure. Preserve the original
run and ask the maintainer to determine the appropriate corrective scope.

The local duration artifact measures the attempt from its first accounting step,
including setup, and honestly leaves timing or checkout fields absent when that
step or checkout could not execute. For authoritative spending, the maintainer
records Actions job **started_at** and **completed_at**, elapsed seconds, OS,
run ID/attempt/URL, head and checkout SHAs, result and any coverage qualification
for every attempt, including failed setup and canceled runs. Record these in the
approved per-run PR report and aggregate Windows job minutes in the weekly
summary. Test-only elapsed time or total workflow wall time is not billed time;
do not infer currency cost or billable minutes without billing evidence.

Local semantic guards pin reviewed YAML and executable text. They detect changed
commands and unsafe settings but do not execute PowerShell or prove GitHub event
semantics. A reviewed green hosted run and inspected native results are required
before claiming native execution or satisfying the merge conditions above.
