// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mtix_test

const windowsDurabilityStepName = "Run required native durability tests"

func windowsDurabilityStep() map[string]any {
	return map[string]any{"name": windowsDurabilityStepName, "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsDurabilityScript}
}

const windowsDurabilityScript = `$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$started = [DateTimeOffset]::UtcNow.ToString('o')
$pattern = '^Test(WithFileHandle_|FlushStateFile_|NativeFlushError_|WriteStateError_|WindowsFlush_|WriteJSON_(Native|FlushAndCloseErrors_|FlushError_|Windows_)|CreateJSON_NativePostFlush_|HostID_NativeFlushFailure_)'
& go test ./internal/oplocal -race -count=1 -timeout=5m -covermode=atomic "-coverprofile=$env:MTIX_WINDOWS_CAPTURE_DIR/durability.cover" -json -run $pattern > "$env:MTIX_WINDOWS_CAPTURE_DIR/durability.json"
$durabilityExit = $LASTEXITCODE
@{ exit_code=$durabilityExit; started_at=$started; finished_at=[DateTimeOffset]::UtcNow.ToString('o'); pattern=$pattern } | ConvertTo-Json | Set-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/durability-process.json"
$events = @(Get-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/durability.json" | ForEach-Object { $_ | ConvertFrom-Json })
$package = 'github.com/hyper-swe/mtix/internal/oplocal'
$bad = @($events | Where-Object { $_.Package -eq $package -and ($_.Action -eq 'fail' -or $_.Action -eq 'skip' -or $_.Action -eq 'build-fail') })
if ($bad.Count -ne 0) { throw 'Native durability FAIL, SKIP or build failure' }
$required = @('TestWithFileHandle_LifetimeAndFailures_PreservesCauses', 'TestFlushStateFile_NativeFailure_PreservesCause', 'TestFlushStateFile_ClosedHandle_ReturnsNativeError', 'TestNativeFlushError_Cause_PreservesClassification', 'TestWriteStateError_Policy_PreservesIndependentCauses', 'TestWindowsFlush_Delegate_PreservesHandleAndError', 'TestWriteJSON_NativeFileFlush_PreservesPreviousRecord', 'TestCreateJSON_NativePostFlush_ReportsPublishedRecord', 'TestHostID_NativeFlushFailure_RefusesInitialization', 'TestWriteJSON_FlushAndCloseErrors_PreservesIndependentCauses', 'TestWriteJSON_NativeFlushOptions_IsolatesConcurrentStates', 'TestWriteJSON_Windows_UsesWriteThrough', 'TestWriteJSON_FlushError_FailsWrite')
$subcases = @('TestWithFileHandle_LifetimeAndFailures_PreservesCauses/open', 'TestWithFileHandle_LifetimeAndFailures_PreservesCauses/callback_error', 'TestWithFileHandle_LifetimeAndFailures_PreservesCauses/closed', 'TestWithFileHandle_LifetimeAndFailures_PreservesCauses/nil', 'TestWriteStateError_Policy_PreservesIndependentCauses/ordinary', 'TestWriteStateError_Policy_PreservesIndependentCauses/native', 'TestWriteStateError_Policy_PreservesIndependentCauses/wrapped_native', 'TestWriteStateError_Policy_PreservesIndependentCauses/native_and_cleanup', 'TestHostID_NativeFlushFailure_RefusesInitialization/io_failure', 'TestHostID_NativeFlushFailure_RefusesInitialization/already_exists', 'TestWriteJSON_FlushError_FailsWrite/before_publication', 'TestWriteJSON_FlushError_FailsWrite/after_publication')
foreach ($test in @($required) + @($subcases)) {
  $ran = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'run' })
  $passed = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'pass' })
  if ($ran.Count -ne 1 -or $passed.Count -ne 1) { throw "Required native durability case did not execute and PASS: $test" }
}
$packagePassed = @($events | Where-Object { $_.Package -eq $package -and !$_.Test -and $_.Action -eq 'pass' })
if ($durabilityExit -ne 0 -or $packagePassed.Count -ne 1) { throw 'Native durability process or package failed' }
if (!(Test-Path "$env:MTIX_WINDOWS_CAPTURE_DIR/durability.cover" -PathType Leaf)) { throw 'Native durability coverage missing' }
$observations = @($events | Where-Object { $_.Test -eq 'TestWriteJSON_Windows_UsesWriteThrough' -and $_.Action -eq 'output' -and $_.Output -match 'DURABILITY_NATIVE_JSON (\{.*\})' } | ForEach-Object { [void]($_.Output -match 'DURABILITY_NATIVE_JSON (\{.*\})'); $Matches[1] | ConvertFrom-Json })
if ($observations.Count -ne 1) { throw 'Exactly one actual native durability observation required' }
$native = $observations[0]
if ($native.successful_flush_file_buffers -ne 2 -or $native.power_loss_tested -ne $false -or $native.observations.Count -ne 2) { throw 'Native flush evidence is incomplete' }
foreach ($observation in $native.observations) {
  if ($observation.write_through -ne $true -or !$observation.filesystem) { throw 'Actual native handle mode or filesystem evidence missing' }
}
if (!$env:MTIX_WINDOWS_HEAD_SHA -or !$env:MTIX_WINDOWS_CHECKOUT_SHA) { throw 'Durability source identity missing' }
@{ head_sha=$env:MTIX_WINDOWS_HEAD_SHA; checkout_sha=$env:MTIX_WINDOWS_CHECKOUT_SHA; run_id=$env:MTIX_WINDOWS_RUN_ID; run_url=$env:MTIX_WINDOWS_RUN_URL; native=$native } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/durability-runtime.json"
`
