# Security Policy

Charon is a security boundary between MCP clients and a private Lethe service. Reports involving authentication, project isolation, protected refs, credential handling, audit integrity, or upstream request construction are treated as security issues.

## Supported versions

Security fixes are applied to the latest released minor version and the current `main` branch. Older releases may be asked to upgrade before receiving a patch.

## Reporting a vulnerability

Do not open a public issue with exploit details, credentials, memory content, or deployment identifiers.

Use GitHub's private vulnerability reporting or Security Advisory flow for this repository. Include:

- the affected version or commit;
- deployment mode and authentication mode;
- a minimal reproduction;
- expected and observed authorization behavior;
- impact and prerequisites;
- any proposed mitigation.

Remove API keys, Obols, OAuth tokens, pairing secrets, HMAC keys, memory contents, and personal data from the report.

A maintainer should acknowledge a complete report within seven days. Disclosure timing will be coordinated after impact and a remediation path are confirmed.

## Security expectations

Production deployments should:

- keep Lethe private and reachable only from Charon;
- use `memory-git` mode unless direct compatibility is explicitly required;
- provision separate Obol, OAuth, and merge HMAC keys;
- use an HTTPS public URL through a trusted reverse proxy;
- bind the direct container port to loopback;
- protect the OAuth pairing secret as an operator credential;
- use exact project grants for mutation scopes;
- run `charon ledger verify` as an operational integrity check;
- rotate credentials after suspected exposure;
- keep the Go toolchain, container bases, and dependencies current.

The repository's Compose file is a hardened single-host reference, not a complete multi-tenant security architecture.