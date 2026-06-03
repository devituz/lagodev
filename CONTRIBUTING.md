# Contributing to lagodev

Thanks for taking the time to contribute. This guide stays short on
purpose — read it once and you should be unblocked.

## Local setup

You need Go 1.25 or newer.

```bash
git clone https://github.com/devituz/lagodev.git
cd lagodev
go test ./...
```

The repo is a Go workspace (`go.work`) so adapter modules
(`adapters/gin`, `adapters/websocket`, `filesystem/s3`, …) resolve to
your local source automatically — no `replace` directives required.

## Before opening a PR

Run the full check locally:

```bash
make check
```

That target runs `gofmt`, `go vet ./...`, and `go test ./...`. CI runs
the same thing — if it passes locally it should pass in CI.

Looking at the diff after `make check`? Run `make fmt` if `gofmt`
flagged anything.

## PR conventions

- **Commit messages**: [Conventional
  Commits](https://www.conventionalcommits.org/) —
  `feat(web): add CSRF middleware`, `fix(orm): correct nil pointer in
  HasMany`, `docs: clarify migration ordering`. The `release` workflow
  generates changelog entries from these prefixes.
- **One feature per PR.** Refactors that only touch the changed area
  are fine; pre-emptive cleanups belong in their own PR.
- **Tests are mandatory** for new features and bug fixes (a regression
  test that fails on `main` and passes with your change).
- **Documentation**: a new public API needs at least a one-paragraph
  update to the relevant doc in `docs/` (or a code-level Go doc comment
  if the feature is internal-only).

## Where to start

- Issues labeled
  [`good first issue`](https://github.com/devituz/lagodev/labels/good%20first%20issue)
  are scoped to a single file or small subsystem and have explicit
  acceptance criteria. Pick one and comment that you're taking it.
- Issues labeled
  [`help wanted`](https://github.com/devituz/lagodev/labels/help%20wanted)
  are larger — chat in the issue thread before sending a big PR so we
  agree on the design first.

## Security

**Do not** open a public issue for vulnerabilities — see
[SECURITY.md](SECURITY.md) for the private reporting process.

## Code of conduct

Be kind. Assume good intent. If a review feels harsh, ask for
clarification rather than withdrawing the PR. We're all here to make
the library better.
