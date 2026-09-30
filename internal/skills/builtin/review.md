---
description: "Review code changes for bugs: uncommitted work, this branch, a pull request, or a commit range"
argument-hint: [PR number | branch | commit range] [what to focus on]
disable-model-invocation: true
---
Review the code changes below and report what is wrong with them. This is a review: do not edit files, commit, or run anything that changes the project. If the user wants the findings fixed, they will ask afterwards.

{{CHANGES}}

The user's arguments: "$ARGUMENTS". Arguments that name a pull request, branch, commit or range were already used to collect the changes above; treat the rest as what to focus on.

## How to review

1. Understand the change first: what it is meant to do, and which behavior it alters.
2. Read beyond the diff. Open the changed files and the code that calls or is called by the changed lines. Most real bugs are in how a change meets code the diff doesn't show.
3. Look for defects, in this order of importance:
   - Wrong behavior: logic errors, inverted or missing conditions, off-by-one, wrong operator or variable, results computed and not used.
   - Unhandled cases: empty, nil or zero inputs, errors ignored or swallowed, partial failure, cleanup skipped on an early return.
   - Broken callers: a changed signature, return value, default or invariant that code elsewhere still relies on.
   - Concurrency and state: races, deadlocks, shared state changed without its lock, stale caches, ordering assumptions.
   - Resources: leaks of files, connections, goroutines or memory; unbounded growth; missing timeouts.
   - Data: loss or corruption, non-atomic updates, migrations that can't run twice or can't be rolled back.
   - Tests: changed behavior with no test, or tests that would pass even if the change were wrong.
4. Check each suspicion against the code before you report it. Trace the path that triggers it. Drop anything you can't back up with a concrete input or sequence that goes wrong; a guess wastes the author's time.
5. Leave out style, naming and formatting, anything a linter or compiler reports, and problems in code the change didn't touch, unless the change makes them worse.

If the changes above were cut short or a file's content matters and isn't shown, read it with your tools rather than reviewing blind.

## What to report

List the findings, most serious first. For each one:

- **Where:** `path/to/file.go:123`
- **What is wrong**, in a sentence.
- **How it fails:** the concrete input, state or sequence, and what happens.
- **Fix:** the change you would make, briefly.

Mark each finding **high** (wrong results, data loss, a crash or a security hole in normal use), **medium** (fails in a plausible edge case) or **low** (unlikely, or minor).

Finish with one or two sentences: whether the change is safe to merge as it is, and what you could not verify. If you found nothing, say so plainly and name what you checked; do not invent findings to fill the list.
