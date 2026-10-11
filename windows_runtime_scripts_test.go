// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// PowerShell contracts for bounded Windows runtime and attempt accounting.
// Script execution and native Windows results remain hosted-only evidence.
package mtix_test

import "strings"

// These are executable workflow contracts, not evidence that PowerShell or
// Windows has run locally. Real Windows provenance comes from the live native
// capture test. Dated fixture replay is intentionally absent until capture.
const windowsRuntimeStartScript = `$ErrorActionPreference = 'Stop'
[IO.Directory]::CreateDirectory($env:MTIX_WINDOWS_CAPTURE_DIR) | Out-Null
[DateTimeOffset]::UtcNow.ToString('o') | Set-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/started.txt"
`

const windowsRuntimeCompilerScript = `$ErrorActionPreference = 'Stop'
$goVersion = & go version
if ($LASTEXITCODE -ne 0) { throw 'go version failed' }
$cgo = & go env CGO_ENABLED
if ($LASTEXITCODE -ne 0 -or $cgo -ne '1') { throw 'Windows race requires CGO' }
$gccVersion = & gcc --version
if ($LASTEXITCODE -ne 0) { throw 'gcc version failed' }
$macros = '' | & gcc -dM -E -x c -include _mingw.h -
if ($LASTEXITCODE -ne 0) { throw 'mingw runtime verification failed' }
$major = ($macros | Select-String '^#define __MINGW64_VERSION_MAJOR ([0-9]+)$').Matches.Groups[1].Value
if (!$major -or [int]$major -lt 8) { throw 'mingw-w64 runtime 8+ required' }
$library = & gcc --print-file-name libsynchronization.a
if ($LASTEXITCODE -ne 0 -or ![IO.Path]::IsPathRooted($library) -or !(Test-Path $library -PathType Leaf)) { throw 'race synchronization library missing' }
@{ go_version=$goVersion; compiler_version=$gccVersion; mingw_runtime_major=$major; synchronization_library_verified=$true } | ConvertTo-Json | Set-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/toolchain.json"
`

func windowsRuntimeTestScript() string {
	oplocal := []string{}
	for _, name := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(windowsRuntimeACLFilter, "^Test("), ")$"), "|") {
		if name != "Ensure_CanonicalHandleAlias_Refuses" {
			oplocal = append(oplocal, "Test"+name)
		}
	}
	hooks := []string{"TestHooksStatus_ValidateInput", "TestHooksStatus_Output", "TestHooksStatus_Command", "TestHooksStatus_Reports", "TestHooksStatus_HomeInput"}
	return strings.NewReplacer("@ACL_FILTER@", windowsRuntimeACLFilter, "@ACL_NAMES@", windowsRuntimeQuotedNames(oplocal), "@HOOK_NAMES@", windowsRuntimeQuotedNames(hooks)).Replace(windowsRuntimeTestTemplate)
}

func windowsRuntimeQuotedNames(names []string) string {
	return "@('" + strings.Join(names, "', '") + "')"
}

const windowsRuntimeTestTemplate = `$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$env:MTIX_WINDOWS_CHECKOUT_SHA = & git rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw 'checked-out source SHA unavailable' }
"MTIX_WINDOWS_CHECKOUT_SHA=$env:MTIX_WINDOWS_CHECKOUT_SHA" | Out-File -FilePath $env:GITHUB_ENV -Append
& go test ./internal/oplocal -race -count=1 -timeout=8m -json -run '@ACL_FILTER@' > "$env:MTIX_WINDOWS_CAPTURE_DIR/oplocal.json"
$oplocalExit = $LASTEXITCODE
& go test ./cmd/mtix -race -count=1 -timeout=8m -json -run '^TestHooksStatus_' > "$env:MTIX_WINDOWS_CAPTURE_DIR/hooks.json"
$hooksExit = $LASTEXITCODE
$requiredOplocal = @ACL_NAMES@
$requiredHooks = @HOOK_NAMES@
foreach ($suite in @(@{ file='oplocal.json'; package='github.com/hyper-swe/mtix/internal/oplocal'; names=$requiredOplocal }, @{ file='hooks.json'; package='github.com/hyper-swe/mtix/cmd/mtix'; names=$requiredHooks })) {
  $events = Get-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/$($suite.file)" | ForEach-Object { $_ | ConvertFrom-Json }
  $package = $suite.package
  if ($package -eq 'github.com/hyper-swe/mtix/internal/oplocal') {
    $optionalAlias = 'TestEnsure_CanonicalHandleAlias_Refuses'
    $aliasEvents = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $optionalAlias })
    $aliasRun = @($aliasEvents | Where-Object { $_.Action -eq 'run' })
    $aliasPassed = @($aliasEvents | Where-Object { $_.Action -eq 'pass' })
    $aliasSkipped = @($aliasEvents | Where-Object { $_.Action -eq 'skip' })
    if ($aliasRun.Count -ne 1 -or $aliasPassed.Count + $aliasSkipped.Count -ne 1) { throw 'Optional alias test must execute and report exactly one PASS or SKIP' }
    $aliasReason = @($aliasEvents | Where-Object { $_.Action -eq 'output' -and $_.Output -match [regex]::Escape('short path names are unavailable on this volume') })
    if ($aliasSkipped.Count -eq 1 -and $aliasReason.Count -eq 0) { throw 'Optional alias SKIP must document unavailable short paths' }
    Write-Output "Optional 8.3 alias coverage: PASS=$($aliasPassed.Count) SKIP=$($aliasSkipped.Count); raw oplocal.json records exact result and reason"
  }
  foreach ($test in $suite.names) {
    $passed = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'pass' })
    $skipped = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'skip' })
    if ($passed.Count -ne 1 -or $skipped.Count -ne 0) { throw "Required native top-level test did not PASS: $package/$test" }
  }
  $packagePassed = @($events | Where-Object { $_.Package -eq $package -and !$_.Test -and $_.Action -eq 'pass' })
  if ($packagePassed.Count -ne 1) { throw "Required native package did not PASS: $package" }
}
if (!(Test-Path "$env:MTIX_WINDOWS_CAPTURE_DIR/capture.json" -PathType Leaf)) { throw 'actual native stock capture missing' }
if ($oplocalExit -ne 0 -or $hooksExit -ne 0) { throw "Native test failure: oplocal=$oplocalExit hooks=$hooksExit" }
`

const windowsRuntimeDurationScript = `$ErrorActionPreference = 'Stop'
[IO.Directory]::CreateDirectory($env:MTIX_WINDOWS_CAPTURE_DIR) | Out-Null
$completed = [DateTimeOffset]::UtcNow
$started = $null
$elapsed = $null
$startFile = "$env:MTIX_WINDOWS_CAPTURE_DIR/started.txt"
if (Test-Path $startFile -PathType Leaf) {
  $started = [DateTimeOffset]::Parse((Get-Content -LiteralPath $startFile -Raw).Trim())
  $elapsed = ($completed - $started).TotalSeconds
}
@{ schema=1; started_utc=$started; completed_utc=$completed.ToString('o'); elapsed_seconds=$elapsed; run_id=$env:MTIX_WINDOWS_RUN_ID; attempt=$env:MTIX_WINDOWS_RUN_ATTEMPT; run_url=$env:MTIX_WINDOWS_RUN_URL; head_sha=$env:MTIX_WINDOWS_HEAD_SHA; checkout_sha=$env:MTIX_WINDOWS_CHECKOUT_SHA; image_os=$env:ImageOS; image_version=$env:ImageVersion; authoritative_job_duration='maintainer records Actions started_at/completed_at for every attempt, including setup failure and cancellation' } | ConvertTo-Json | Set-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/duration.json"
`
