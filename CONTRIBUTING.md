# Contributing to hue-exporter

Thanks for your interest in improving `hue-exporter`.

## Code of Conduct

This project follows our [Code of Conduct](.github/CODE_OF_CONDUCT.md). By
participating, you are expected to uphold it.

## Before opening an issue

- Search existing issues first to avoid duplicates.
- Use [SUPPORT.md](SUPPORT.md) for general support expectations.
- For security concerns, follow [SECURITY.md](SECURITY.md) and do not file a
  public issue.

## Development setup

1. Install Go (version from `go.mod`).
2. Fork and clone the repository.
3. Run checks locally:

```bash
go test ./...
go vet ./...
go build ./...
```

## Pull request process

1. Create a branch from `main`.
2. Keep the scope focused and include tests for behavior changes.
3. Update docs when user-facing behavior or configuration changes.
4. Ensure CI is green before requesting review.
5. Open a PR using the repository template and complete its checklist.

## Commit guidance

- Use clear, descriptive commit messages.
- Prefer small, reviewable commits.
- Address review feedback promptly.
