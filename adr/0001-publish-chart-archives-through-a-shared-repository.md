# 1. Publish chart archives through a shared repository

Date: 2026-11-02

## Status

Accepted.

## Context and Problem Statement

Fjord needs a public Helm chart, and later applications should use the same charts.nerdswhofish.com endpoint.
Joey requested application repositories publish packages with a key to a shared Helm repository whose automation syncs R2.

## Considered Options

1. Commit archives to a shared repository, then synchronize R2
2. Give each application R2 publishing credentials
3. Publish only OCI charts

## Decision Outcome

Chosen: **option 1**.

Keep chart source in each application repository and publish packaged charts into this repository using a short-lived GitHub App token.
Validate archives and immutable versions centrally, upload archives before index.yaml, and serialize R2 synchronization.

## Consequences

### Good

- Applications own their chart changes and releases.
- R2 credentials stay in one repository.
- A standard Helm repository URL serves every chart.

### Bad

- Packaged binaries accumulate in Git history.
- Publishing depends on the central workflow and serializes index updates.
- Application repositories holding the App key must be trusted; the private key can mint tokens within its installation grants.

### Rejected because

- Direct R2 publishing does not meet the requested central synchronization flow and distributes storage credentials.
- OCI-only publishing does not provide the requested standard helm repo add endpoint.
