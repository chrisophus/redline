This repository is redline, a Go command-line tool that reviews a code change. `README.md` says
what it does and `redline-design.md` says why it is built the way it is.

- `AGENTS.md` is the writing style for everything you write here: docs, comments, the changelog
  and your reply. Follow it.
- Every change needs a test next to it. Run `go test` for the packages you changed, and say in
  your reply what you ran and what it said.
- `go test ./internal/boundary/` holds two rules of the architecture: redline links no language
  toolchain, and what a context provider knows stays behind the envelope. If it fails, change your
  approach. Do not change the test.
- Add an entry to `CHANGELOG.md` under "Unreleased" for a change a user of the tool would notice.
- CI also runs golangci-lint, which you cannot run here. Keep new code in the style of the code
  around it, and expect a lint failure to come back to you as a later message.
- You cannot change `.github/` or `.sandbox/`. If the issue needs a change there, make the rest of
  the change and say what is left for a person.
