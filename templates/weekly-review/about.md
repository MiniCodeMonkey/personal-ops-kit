Monday-morning memo compiling everything your jobs recorded during the week: open loops, stale items, events, and the transcript miner's open suggestions. A weekly review with the gathering step automated.

**How it works**

- `weeklyreview` (plain code) folds every job's shared entries into one bounded digest; a single model pass narrates it.
- An empty digest ends the run at *no-change* with no model call.
- It grows with you: any job you add that appends shared entries shows up here with no changes to this job.
