# Specification Quality Checklist: Deliver Issued and Renewed Certificates to Inventory-Agent Hosts

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-10-02
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain (the three binding decisions were made by the user on 2026-10-02; remaining choices are recorded as open questions Q1–Q4 in research.md with a chosen default)
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

- "Deployer provider", "certbot layout", `LCM_*` variables and the file
  names are user-facing domain terms fixed by the user's decisions and v3
  parity, not implementation choices.
- Out of scope: Windows agents (D15), removal of certificates from hosts,
  notifications on delivery failure, rollback through the deployer.
- Amendment 2026-10-02 (user): schema-driven deployer configuration drawer
  for all providers with per-provider required fields like v3 → US5
  (P2), FR-023–FR-031, SR-011–SR-014, SC-007–SC-008; host picker story
  renumbered US6, revocation US7. Re-validated against every item above:
  requirements testable (per-field client + 422 server refusal), success
  criteria measurable (SC-007 per required field, SC-008 secret scan),
  edge cases added (legacy rows, override-supplied required values,
  secrets in custom headers, removed provider).
- Clarifications 2026-10-02 (user answers Q5–Q7), re-validated against
  every item above: Q5 → v3 BIG-IP `ssl_profile` and FortiGate
  `default_ssl_profile` included as User Story 8 (P3), FR-037–FR-042,
  SR-016, SC-009 (testable against fake appliances; precondition failures
  leave appliances unchanged); Q6 → default confirmed (non-secret
  credential values pre-filled for managers); Q7 → target-supplied
  required fields via the `overridable` descriptor flag, US5 scenarios
  10–12, FR-032–FR-036, SR-015, SC-007 reworded (refusal measured at
  configuration save for non-overridable fields and at target attach /
  job start for overridable ones), edge cases added. The
  credential-redirect fix is delivered by a separate deployer hotfix; 033
  keeps SR-014 with regression tests. New open questions Q8–Q11 in
  research.md carry chosen defaults (no [NEEDS CLARIFICATION] markers).
  Out of scope: v3 FortiGate audit profile, `replace_strategy`
  alternatives and SSL-offload objects (Q9).
