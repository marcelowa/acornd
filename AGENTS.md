# Acornd

A local agent console: a Go gateway (`gateway/`), a bun worker per workspace (`worker/`), and a
React interface (`ui/`). The layers and the wire between them are ADR 0002.

## Before you change something

- The rules are `docs/engineering/standards.md`. Read "Working with an agent" first: do not
  scaffold or reorganise, and make the smallest change that satisfies the rule.
- Decisions are `docs/adr/`. If a change goes against one, say so before you make it.
- The gateway's import boundary is `gateway/internal/arch`. Adding a row there is a decision, so
  say why in the row.

## Before you say it is done

- `make check` is the fast tier: compiler, bundle, unit tests.
- `make test` is the default tier: lint, coverage and mutation floors, every suite. Floors only
  move up.
- When the work is finished, run the `deslop` prompt (`.agents/prompts/deslop.md`) on it.

## Commits and words

- Commit messages follow the `conventional-commits` skill.
- Docs, comments and replies follow the `plain-words` skill.

Skills, agents and prompts live in `.agents/`. Tool-specific folders only link to them.
