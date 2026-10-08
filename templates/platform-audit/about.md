The platform watching itself, monthly. Reads every job's run ledger and reports what actually happened: runs per job, outcome mix, gated nights, failure streaks, and anything that quietly stopped producing.

**How it works**

- Pure code, no model call, so it can never be gated by the quota it is reporting on.
- Flags are the point: a job skipped for three weeks or failing every other day shows up here even if its notifications were all dismissed.
