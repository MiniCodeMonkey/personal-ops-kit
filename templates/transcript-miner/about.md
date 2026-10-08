Weekly sweep of your Claude Code session transcripts under `~/.claude*/projects`, looking for things that should become permanent: corrections given more than once (a memory or a `CLAUDE.md` rule) and workflows walked through repeatedly (a skill).

**How it works**

- **verify** (plain code) closes open suggestions whose fix now exists.
- **distill** (plain code) reduces the week's transcripts to a small digest of what you typed, with repeat counts.
- **judge** is a single model pass over the digest that writes the suggestions and the memo. An empty digest ends the run at *no-change* with no model call.
- *Open in Claude Code* on the memo starts a session with the memo as its brief, so adopting a tip is one paste away.
