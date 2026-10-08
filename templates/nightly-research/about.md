Picks one research lane per night from `lanes/`, hands a self-contained brief to a headless Claude Code session inside the OS sandbox, and leaves a self-contained HTML memo. It is **gated**: it skips the night when your weekly Claude usage is already above the threshold in `job.env`.

**How it works**

- `lanepick` (plain code) chooses tonight's lane, favouring lanes that have gone longest without a memo.
- The session picks one narrow question inside the lane, researches it, runs real calculations in its sandboxed shell, and writes the memo.
- To add a lane, drop a markdown file into `lanes/`. The file name is the lane's name; the body is the brief. Optional frontmatter: `weight` (higher comes round sooner) and `months` (`all`, `4-9`, `11-2`, or `3,4,5`).
