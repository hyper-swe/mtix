# DO-178C Compliance Mapping — MTIX

This checklist maps mtix workflows to DO-178C (Software Considerations in Airborne Systems and Equipment Certification) objectives.

## Design Assurance Levels (DAL)

| DAL | Failure Condition | mtix Mapping |
|-----|-------------------|--------------|
| A — Catastrophic | Loss of life | Story-level annotation: `DAL-A`. All children inherit. 100% MC/DC coverage in acceptance criteria. |
| B — Hazardous | Severe injury | Story-level annotation: `DAL-B`. 100% decision coverage in acceptance criteria. |
| C — Major | Significant impact | Story-level annotation: `DAL-C`. 100% statement coverage. |
| D — Minor | Nuisance | Story-level annotation: `DAL-D`. Standard testing. |
| E — No Effect | No safety impact | Story-level annotation: `DAL-E`. Minimal verification. |

## Requirements Traceability (§6.3)

mtix satisfies requirements traceability via the context chain:

1. **Requirement → Task:** Each task references its FR/NFR in the description/prompt fields
2. **Task → Implementation:** The prompt specifies exact files, functions, and code changes
3. **Task → Test:** The `tests` field specifies test function names and scenarios
4. **Test → Result:** Completion comments link to test results

To verify: Use `mtix_search` to find tasks referencing a specific requirement number. Use `mtix_tree` to see the full decomposition from requirement to implementation.

## Verification Objectives (§6.4)

| Objective | mtix Feature |
|-----------|--------------|
| Reviews | Independent agent verification (implementing agent ≠ verifying agent) |
| Analysis | Context chain provides full requirement-to-code traceability |
| Testing | `tests` field specifies required test coverage per DAL level |
| Traceability | Dot-notation hierarchy + dependency tracking |

## Configuration Management (§7)

| CM Objective | mtix Feature |
|--------------|--------------|
| Configuration identification | Dot-notation IDs uniquely identify every work item |
| Change control | State machine enforces valid transitions; all changes logged |
| Status accounting | `mtix_stats`, `mtix_progress` provide real-time status |
| Audit | Session records, heartbeats, comments create complete audit trail |

## Problem Reporting (§8)

Use `mtix_comment` to document problems discovered during implementation or verification. Use `mtix_defer` with root cause for blocked work. All anomalies are timestamped and attributed to the reporting agent.
