# Contributing to Go Micro

Thank you for your interest in contributing to Go Micro! This document provides guidelines and instructions for contributing.

## Code of Conduct

Be respectful, inclusive, and collaborative. We're all here to build great software together.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/go-micro.git`
3. Add upstream remote: `git remote add upstream https://github.com/flylib/go-micro.git`
4. Create a feature branch: `git checkout -b feature/my-feature`

## Development Setup

```bash
# Install dependencies
go mod download

# Install development tools
make install-tools

# Run tests
make test

# Run tests with race detector and coverage
make test-coverage

# Run linter
make lint

# Format code
make fmt
```

See `make help` for all available commands.

## Making Changes

### Code Guidelines

- Follow standard Go conventions (use `gofmt`, `golint`)
- Write clear, descriptive commit messages
- Add tests for new functionality
- Update documentation for API changes
- Keep PRs focused - one feature/fix per PR

### Commit Messages

Use conventional commits format:

```
type(scope): subject

body

footer
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `test`: Test additions/changes
- `refactor`: Code refactoring
- `perf`: Performance improvements
- `chore`: Maintenance tasks

Examples:
```
feat(registry): add kubernetes registry plugin
fix(broker): resolve nats connection leak
docs(examples): add streaming example
```

### Testing

- Write unit tests for all new code
- Ensure existing tests pass
- Add integration tests for plugin implementations
- Test with multiple Go versions (1.20+)

```bash
# Run specific package tests
go test ./registry/...

# Run with verbose output
go test -v ./...

# Run specific test
go test -run TestMyFunction ./pkg/...

# Optional: Use richgo for colored output
go install github.com/kyoh86/richgo@latest
richgo test -v ./...
```

### Documentation

- Update the README of the package or module you change
- Add a runnable example under `examples/` for new features
- Update the root README.md for major features
- Add godoc comments for exported functions/types

## Pull Request Process

1. **Update your branch**
   ```bash
   git fetch upstream
   git rebase upstream/master
   ```

2. **Run tests and linting**
   ```bash
   go test ./...
   golangci-lint run
   ```

3. **Push to your fork**
   ```bash
   git push origin feature/my-feature
   ```

4. **Create Pull Request**
   - Use a descriptive title
   - Reference any related issues
   - Describe what changed and why
   - Add screenshots for UI changes
   - Mark as draft if work in progress

5. **PR Review**
   - Respond to feedback promptly
   - Make requested changes
   - Re-request review after updates

### PR Checklist

- [ ] Tests pass locally
- [ ] Code follows Go conventions
- [ ] Documentation updated
- [ ] Commit messages are clear
- [ ] Branch is up to date with master
- [ ] No merge conflicts

## Adding Plugins

New plugins should:

1. Live in the appropriate interface directory (e.g., `registry/myplugin/`)
2. Implement the interface completely
3. Include comprehensive tests
4. Provide usage examples
5. Document configuration options (env vars, options)
6. Add to plugin documentation

Example structure:
```
registry/myplugin/
├── myplugin.go          # Main implementation
├── myplugin_test.go     # Tests
├── options.go           # Plugin-specific options
└── README.md            # Usage and configuration
```

## Reporting Issues

Before creating an issue:

1. Search existing issues
2. Check documentation
3. Try the latest version

When reporting bugs:
- Use the bug report template
- Include minimal reproduction code
- Specify versions (Go, Go Micro, plugins)
- Provide relevant logs

## Documentation Contributions

Documentation improvements are always welcome!

- Fix typos and grammar
- Improve clarity
- Add missing examples
- Update outdated information

Documentation lives in Markdown next to the code: the root README.md, one README per package or plugin module, and the gateway contract in `gateway/SPEC.md`.

## Community

- GitHub Issues: Bug reports and feature requests
- Questions: open a [question issue](.github/ISSUE_TEMPLATE/question.md)

## Release Process

The repository is not tagged. Consumers use pseudo-versions (`go get github.com/flylib/go-micro/<module>@main`). After pushing changes that modules depend on, maintainers pin every in-repo require to the pushed commit:

```bash
internal/scripts/release-modules.sh --pseudo <pushed-commit>
```

Then update CHANGELOG.md, commit and push.

## Questions?

- Read the [README](README.md)
- Browse [examples](examples/)
- Open a [question issue](.github/ISSUE_TEMPLATE/question.md)

Thank you for contributing to Go Micro! 🎉
