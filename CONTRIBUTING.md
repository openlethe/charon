# Contributing to Charon

Thank you for improving Charon. Changes to this repository may affect authentication, authorization, private memory, or protected-ref governance, so evidence and narrowly scoped changes matter.

## Development setup

Requirements:

- Go 1.25.12 or newer within the supported 1.25 release line;
- Docker with Compose for container validation;
- an isolated Lethe test service for end-to-end Memory Git testing.

Run the local verification set before submitting a change:

```bash
make test
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.22.5 -quiet ./...
docker compose config --quiet
docker build -t charon:review .
```

CI also requires a clean `gofmt -l .` result and validates shell scripts, the isolated E2E Compose file, and the image's non-root user.

## Change discipline

- Preserve the default `memory-git` fail-closed surface.
- Keep Lethe access behind typed adapter methods; do not add a generic proxy.
- Require explicit scopes and project grants at every mutation boundary.
- Use exact-project authority for writes; wildcard grants are read-only.
- Preserve ref ownership, compare-and-swap, idempotency, and protected-ref checks.
- Bind merge approvals to the exact proposal state reviewed.
- Never log credentials, raw memory content, or pairing secrets.
- Add negative tests for authorization failures and stale state.
- Avoid unrelated refactors in security fixes.

## Pull requests

A pull request should explain:

- the user-visible or security problem;
- the affected trust boundary;
- the smallest compatible fix;
- tests and behavior proof;
- deployment or migration impact;
- remaining limitations.

Security-sensitive changes should receive an independent review. Do not include generated binaries, local databases, environment files, tokens, or runtime state.

## Commit and release hygiene

Source commits should remain buildable from a clean checkout. Release binaries are produced by the tagged release workflow and should not be committed into the repository.