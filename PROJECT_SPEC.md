# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, requests a
repeat measurement when tolerance is exceeded, and seals the final evidence
record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, and timestamp.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, tolerance, and
owner. The service verifies that the device exists, is active, and has no
unfinished case. A valid request creates a case in `awaiting_measurement`.

### 3. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service computes the signed deviation from the
nominal value. An in-tolerance measurement moves the case to
`review_ready`; an out-of-tolerance measurement moves it to
`recheck_required`.

### 4. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail.

### 5. Cancel Verification Case

The steward sends `POST /cases/{id}/cancel` when a case was opened against the
wrong device, nominal value, or tolerance, before any measurement is recorded.
Only an `awaiting_measurement` case with no measurements on record may be
canceled. The actor, reason, and cancellation time are retained on the case and
in an audit event. Cancellation is terminal, releases the device's
unfinished-case slot so a fresh case can be opened, and leaves the canceled
record historically readable. Cases that already carry measurements, or that
are in `review_ready`, `recheck_required`, or `sealed`, cannot take this path.

### 6. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, creates exactly one evidence
seal, moves the case to `sealed`, and rejects later mutations.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `awaiting_measurement -> canceled`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.
- `canceled` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, seal again, or be canceled.
- A case can be canceled only while awaiting measurement with zero recorded
  measurements; a canceled case retains its actor, reason, and time, cannot
  accept measurements or reopen, and no longer occupies its device's
  unfinished-case slot.
- Cancellation and measurement of the same case are mutually exclusive; the
  repository lock serializes them so exactly one outcome is ever committed.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/cancel`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal. The cancel check races a cancellation against a first measurement to
prove the lock commits exactly one of the two facts, verifies the device slot
is released only when cancellation wins, and confirms progressed, measured,
reopened, and sealed cases cannot be canceled.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
