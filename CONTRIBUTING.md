# Contributing

Thanks for contributing to `hue-exporter`.

## Development setup

1. Install Go (version from `go.mod`).
2. Clone the repository.
3. Run:

```bash
go test ./...
go vet ./...
go build ./...
```

## Pull request process

1. Create a branch from `main`.
2. Keep changes focused and include/update tests.
3. Ensure CI passes (`Build and test` and Docker workflow checks).
4. Open a pull request using the template.

## Commit and review expectations

- Use clear commit messages.
- Address review comments.
- Keep documentation updated when behavior changes.
