// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Operator alert runtime contracts extend the existing bounded Windows job.
package mtix_test

import "strings"

const windowsAlertsStepName = "Run required native operator alerts tests"
const windowsAlertsFilter = "^TestAlert(Raise_(SameClassWithinHour_CountIncrements|ClockBoundaries_PreservesWindows|DistinctKeys_IsolatesCounts|EvictedWindow_PreservesCount|ProjectionBoundaries_ReplaysWithoutExtraCount|ConcurrentProcesses_PreservesEveryCount|ProcessDeath_ReleasesLockAndRecoversAcceptedRow)|Log_(Rotation_KeepsThree|CountGrowthAtLimit_ReintroducesLogicalRow)|State_ProtectedACL_RefusesBeforePrepare|Recent_AbsentAndLimits_ReadOnly)$"
const windowsAlertsDoctorFilter = "^Test(RunSyncDoctor_OperatorAlerts_ReportsRecent|RunSyncDoctor_NoOperatorAlerts_PreservesLocalChecks|ScrubDoctorReport_RecentAlerts_MasksNestedDiagnostic)$"

func windowsAlertsNames() []string {
	return []string{"TestAlertRaise_SameClassWithinHour_CountIncrements", "TestAlertRaise_ClockBoundaries_PreservesWindows", "TestAlertRaise_DistinctKeys_IsolatesCounts", "TestAlertRaise_EvictedWindow_PreservesCount", "TestAlertRaise_ProjectionBoundaries_ReplaysWithoutExtraCount", "TestAlertRaise_ConcurrentProcesses_PreservesEveryCount", "TestAlertRaise_ProcessDeath_ReleasesLockAndRecoversAcceptedRow", "TestAlertLog_Rotation_KeepsThree", "TestAlertLog_CountGrowthAtLimit_ReintroducesLogicalRow", "TestAlertState_ProtectedACL_RefusesBeforePrepare", "TestAlertRecent_AbsentAndLimits_ReadOnly"}
}
func windowsAlertsDoctorNames() []string {
	return []string{"TestRunSyncDoctor_OperatorAlerts_ReportsRecent", "TestRunSyncDoctor_NoOperatorAlerts_PreservesLocalChecks", "TestScrubDoctorReport_RecentAlerts_MasksNestedDiagnostic"}
}
func windowsAlertsSubcases() []string {
	names := []string{}
	for _, stage := range []string{"before:alerts-pending.json", "after:alerts-pending.json", "before:alerts.2.jsonl", "after:alerts.2.jsonl", "before:alerts.1.jsonl", "after:alerts.1.jsonl", "before:alerts.jsonl", "after:alerts.jsonl", "before:rate", "after:rate", "before:clear", "after:clear"} {
		names = append(names, "TestAlertRaise_ProjectionBoundaries_ReplaysWithoutExtraCount/"+stage)
	}
	for _, name := range []string{"before", "exact", "backward"} {
		names = append(names, "TestAlertRaise_ClockBoundaries_PreservesWindows/"+name)
	}
	for _, name := range []string{"hold", "crash"} {
		names = append(names, "TestAlertRaise_ProcessDeath_ReleasesLockAndRecoversAcceptedRow/"+name)
	}
	for _, name := range []string{"0", "1", "2"} {
		names = append(names, "TestAlertLog_CountGrowthAtLimit_ReintroducesLogicalRow/"+name)
	}
	for _, name := range []string{"alerts.jsonl", "alerts.1.jsonl", "alerts.2.jsonl", "alerts-pending.json", "alerts.lock", "rate"} {
		names = append(names, "TestAlertState_ProtectedACL_RefusesBeforePrepare/"+name)
	}
	return names
}
func windowsAlertsScript() string {
	return strings.NewReplacer("@ALERT_FILTER@", windowsAlertsFilter, "@DOCTOR_FILTER@", windowsAlertsDoctorFilter, "@ALERT_NAMES@", windowsRuntimeQuotedNames(windowsAlertsNames()), "@DOCTOR_NAMES@", windowsRuntimeQuotedNames(windowsAlertsDoctorNames()), "@ALERT_SUBCASES@", windowsRuntimeQuotedNames(windowsAlertsSubcases())).Replace(windowsAlertsTemplate)
}

const windowsAlertsTemplate = `$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
& go test ./internal/oplocal -race -count=1 -timeout=5m -json -run '@ALERT_FILTER@' > "$env:MTIX_WINDOWS_CAPTURE_DIR/alerts.json"
$alertsExit = $LASTEXITCODE
& go test ./cmd/mtix -race -count=1 -timeout=5m -json -run '@DOCTOR_FILTER@' > "$env:MTIX_WINDOWS_CAPTURE_DIR/alerts-doctor.json"
$doctorExit = $LASTEXITCODE
$requiredAlerts = @ALERT_NAMES@
$requiredDoctor = @DOCTOR_NAMES@
$mandatoryAlerts = @ALERT_SUBCASES@
$mandatoryDoctor = @('TestRunSyncDoctor_OperatorAlerts_ReportsRecent/json', 'TestRunSyncDoctor_OperatorAlerts_ReportsRecent/text')
foreach ($suite in @(@{ file='alerts.json'; package='github.com/hyper-swe/mtix/internal/oplocal'; names=$requiredAlerts; subcases=$mandatoryAlerts }, @{ file='alerts-doctor.json'; package='github.com/hyper-swe/mtix/cmd/mtix'; names=$requiredDoctor; subcases=$mandatoryDoctor })) {
  $events = Get-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/$($suite.file)" | ForEach-Object { $_ | ConvertFrom-Json }
  $package = $suite.package
  $badEvents = @($events | Where-Object { $_.Package -eq $package -and ($_.Action -eq 'fail' -or $_.Action -eq 'skip') })
  if ($badEvents.Count -ne 0) { throw "Native alerts FAIL or SKIP: $package" }
  foreach ($test in @($suite.names) + @($suite.subcases)) {
    $ran = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'run' })
    $passed = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'pass' })
    if ($ran.Count -ne 1 -or $passed.Count -ne 1) { throw "Required native alerts test did not execute and PASS: $package/$test" }
  }
  $packagePassed = @($events | Where-Object { $_.Package -eq $package -and !$_.Test -and $_.Action -eq 'pass' })
  if ($packagePassed.Count -ne 1) { throw "Required native alerts package did not PASS: $package" }
}
if ($alertsExit -ne 0 -or $doctorExit -ne 0) { throw "Native alert test failure: alerts=$alertsExit doctor=$doctorExit" }
`
