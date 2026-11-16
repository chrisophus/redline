This project is Redline, a Go command-line tool that measures a code change and has a model review it.

- When a ticket leaves product or design choices open, even ones it says are for a person, pick the
  simplest reasonable option, build it, and list the choices you made at the top of the pull request
  so they can be changed in review. Start permissive and leave tightening for later. Stop and ask only
  when the change would delete data, cannot be undone, or would expose a secret. The other rules here
  still apply.
- Read `AGENTS.md` and `CLAUDE.md` first and follow them, the writing style included.
- Every change needs a test next to it. Keep `internal/boundary` passing: it enforces the package layering.
- Run `gofmt -l .`, `go vet ./...` and `go test ./...` for what you changed, and say in your reply what you ran and what it said.
- Never call a model or read `ANTHROPIC_API_KEY`; the tests need neither.
- Add a line to `CHANGELOG.md` under the unreleased section for a change a user would notice.
