# Changelog

All notable changes to Charon are documented here. The project follows semantic versioning once a stable release line is declared.

## Unreleased

### Security

- Require an authorization key before OAuth authorization codes can be issued, generate a fresh high-entropy key for each primary Compose process start, and display the exact client, callback destination, and protected resource on the consent page.
- Fail closed instead of marking legacy proposals approved with a placeholder event ID when Lethe cannot provide idempotent event materialization.
- Require `response_type=code` and S256 PKCE for public browser authorization flows.
- Bind explicit merge review verdicts to the exact proposal snapshot.
- Separate Obol, OAuth, and protected-merge HMAC keys while retaining a legacy migration fallback.
- Reject unauthenticated operation on non-loopback listeners.
- Exclude disabled principals from authentication.
- Add inbound request limits and HTTP server timeouts.
- Strictly reject fractional, negative, duplicate, or non-numeric selected merge operation ordinals.
- Escape all changeset and manifest identifiers before constructing Lethe request paths.
- Enforce the configured response limit on all typed Lethe JSON decoders.
- Reject confidential OAuth clients that have no configured secret.
- Keep the Lethe health-check bearer credential out of process arguments.
- Upgrade the required Go toolchain to 1.25.12.

### Reliability

- Add coordinated graceful shutdown for public and admin servers.
- Serialize and transactionally append the audit ledger.
- Add full audit-chain verification and tamper detection.
- Parse and range-check documented operational limits.
- Remove unbounded in-memory OAuth token records and clean expired authorization codes.

### Deployment

- Run the container as UID/GID 10001.
- Pin builder and runtime base images by digest.
- Add a hardened loopback-only Compose reference with dropped capabilities, a read-only root filesystem, and resource limits.
- Stop generating or printing HMAC signing secrets in the container entrypoint; only the short-lived OAuth browser authorization key is emitted, once, when runtime generation is enabled.
- Preserve the legacy `CHARON_HMAC_KEY` migration fallback in the primary Compose profile.
- Rotate Obols against the Compose named-volume database used by the documented setup flow.
- Remint the bootstrap Obol when the reconciled auto-bootstrap principal changes.
- Update the Memory Git E2E workflow for explicit merge-review verdicts.
- Style the OAuth authorization page with Lethe's visual system, show live submission feedback, return an actionable top-of-page error when the active key does not match, print generated keys in a dedicated first-output startup banner, and permit only validated OAuth callback origins in the form navigation policy.

### Project

- Add continuous integration, static analysis, vulnerability scanning, dependency updates, release verification, checksums, an SPDX SBOM, build provenance, and embedded version/commit/build metadata.
- Add security, contribution, conduct, and support policies.
- Document the required connector refresh and paired reauthorization after the merge-review schema upgrade.