# IEC 62304 Compliance Mapping — MTIX

This checklist maps mtix workflows to IEC 62304 (Medical Device Software — Software Life Cycle Processes).

## Software Safety Classes

| Class | Risk Level | mtix Mapping |
|-------|-----------|--------------|
| A | No injury possible | Story-level annotation: `Class-A`. Standard verification. |
| B | Non-serious injury possible | Story-level annotation: `Class-B`. Traceability required: requirement → design → test. |
| C | Death or serious injury possible | Story-level annotation: `Class-C`. Full traceability: requirement → design → implementation → test. Independent verification mandatory. |

## Software Development Process (§5)

### §5.1 Software Development Planning
- Use `mtix_tree` and `mtix_decompose` to create the software development plan as a task hierarchy
- Each story represents a software system requirement
- Decomposition creates the detailed design and implementation tasks

### §5.2 Software Requirements Analysis
- Top-level tasks contain software requirements in their description/prompt fields
- Use `mtix_dep_add` to declare requirements dependencies
- Requirements traceability is maintained through the dot-notation hierarchy

### §5.3 Software Architectural Design
- Mid-level tasks represent architectural decisions
- The context chain propagates architectural constraints to implementation tasks
- Cross-references between components use `mtix_dep_add` with `related` type

### §5.4 Software Detailed Design
- Leaf tasks contain detailed design: file paths, function signatures, API contracts
- The `prompt` field serves as the detailed design document for each unit

### §5.5 Software Unit Implementation and Verification
- Agent executes the leaf task following the assembled context
- `acceptance` field defines unit verification criteria
- `tests` field specifies required unit tests

### §5.5.5 Change Control
Every state transition in mtix is a change record:
- Timestamped with the transition time
- Attributed to the agent that made the change
- Logged in the activity history
- Queryable via `mtix_search`

## HIPAA Considerations

**Task descriptions MUST NOT contain Protected Health Information (PHI).**

mtix tasks describe software work, not patient data. Never include:
- Patient names, IDs, or demographics
- Clinical data or test results
- Any individually identifiable health information

## Risk Management Integration (ISO 14971)

- Story-level tasks reference the risk analysis document
- Safety class annotations flow down the context chain
- Risk mitigations are tracked as acceptance criteria in child tasks
- Residual risk assessment referenced in completion comments

## SOUP Management (§8)

If tasks involve Software of Unknown Provenance:
- Document the SOUP component in the task description
- Include version and source in the prompt
- Acceptance criteria must include SOUP validation steps
- Use `mtix_dep_add` to link SOUP-related tasks
