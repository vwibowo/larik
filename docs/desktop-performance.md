# Desktop performance measurements — 2026-11-07

Measurements used an Apple M3, macOS 27.0.1, and Go 1.27.3. The sidebar fixture has 20 open projects with 500 or 2,000 sessions; one in ten titles contains the active search term. Each benchmark renders a 280×800 sidebar frame. The Changes fixture has 100 modified tracked files, with the first 60 visible. These are local, repeatable microbenchmarks, not whole-app frame-rate or battery measurements. Timings are rounded results of three benchmark runs with a 300 ms target per case.

## Sidebar

| Sessions | Search | Before, time/frame | After, time/frame | Before, bytes/frame | After, bytes/frame |
|---:|---|---:|---:|---:|---:|
| 500 | empty | 454 µs | 365 µs | 898 KB | 471 KB |
| 500 | active | 290 µs | 222 µs | 196 KB | 109 KB |
| 2,000 | empty | 968 µs | 539 µs | 4.66 MB | 1.89 MB |
| 2,000 | active | 642 µs | 435 µs | 779 KB | 400 KB |

The empty-search matcher alone fell from about 457 µs and 2.77 MB at 2,000 sessions to about 6 ns with no allocations. The remaining frame cost includes rendering and other sidebar work. Search matches are now calculated once per frame and reused for selection and results. A path-to-project map replaces repeated project scans when showing search hits.

## Changes pane idle work

With 100 modified files but no new edits, a full `readChanges` costs about 73 ms and 565 KB every two seconds. The local fingerprint check costs about 25 ms and 280 KB on the same fixture. A one-iteration run with a Git wrapper counted eight Git subprocesses for the warmed full read versus three for the fingerprint. That lowers the measured steady-state Git work from about four to 1.5 subprocesses per second while this pane is open. The fingerprint still checks Git status, branch, HEAD, index metadata, and changed-file metadata. Edits or pane actions trigger a full read; a full read also runs every 30 seconds to catch preserved timestamps or configuration changes.

The fingerprint allocates more Go objects than the full read in this fixture (about 1,822 versus 1,580 per operation), despite using fewer bytes and less wall time. Further work could reduce those allocations, but the observed idle time and subprocess reduction was the reason for this change.

An isolated macOS Desktop process was sampled for five seconds with `sample`: its main thread was waiting for events and its physical footprint was 90.7 MB at sampling time. The automation surface attached to an already-running user Larik window, so the isolated process could not be verified with its Changes pane visible. This sample is therefore a sanity check of an idle process, not a before-and-after CPU result for the pane. The repeatable Git benchmark above is the evidence for the polling change.

## Indexing and remote requests

The session index still refreshes every 1.5 seconds because it supplies live session status; its frequency was not changed without a targeted measurement. Each connected remote server currently receives an index request on that interval. Remote Changes panes still request a full read every two seconds. Remote latency, data size, and status freshness vary by server, so remote index and Changes traffic need a separate representative profile before their cadence changes.

Reproduce the local benchmarks with:

```bash
go test ./internal/desktop -run '^$' -bench 'Benchmark(SidebarFrame|SearchMatches|ChangesIdle|ChangesProbeIdle)$' -benchmem -count=3 -benchtime=300ms
```
