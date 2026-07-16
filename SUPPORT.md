# Support

## Questions and bug reports

Use a GitHub issue for reproducible bugs, documentation gaps, and narrowly scoped feature requests. Include:

- Charon version or commit;
- operating system or container runtime;
- `CHARON_MODE` and authentication mode, without secret values;
- relevant sanitized logs;
- expected and observed behavior;
- a minimal reproduction when possible.

Do not include Lethe API keys, HMAC keys, Obols, OAuth tokens, pairing secrets, private memory content, database files, or personal data.

## Security issues

Follow [SECURITY.md](SECURITY.md). Do not publish exploit details in an issue or discussion.

## Scope

Community support covers the current release and `main`. Deployment-specific reverse proxies, identity providers, container platforms, and private Lethe modifications may require reproduction against the repository's documented reference configuration.

Charon is provided without a commercial support guarantee. Maintainers may prioritize security, correctness, and reproducible defects over environment-specific integration requests.