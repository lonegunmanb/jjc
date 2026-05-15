// Package app — concurrency model and lock-ordering invariant.
//
// Several types in this package own their own mutex. To avoid deadlocks
// the codebase observes a single global ordering: when a goroutine
// needs to hold more than one of these locks at the same time, it must
// acquire them in the order listed below (top → bottom). Releasing in
// the reverse order is conventional but not required.
//
//  1. Dispatcher.mu               — guards the per-card workerHandle map
//                                    plus dispatcher-wide flags.
//  2. workerHandle (per-card)     — currently no explicit mutex; the
//                                    handle is read-only after
//                                    construction. The inbox channel
//                                    plus the kill / done channels
//                                    handle synchronisation.
//  3. ActivityTracker.mu          — guards the per-card status struct,
//                                    activity ring, and tool-call name
//                                    lookup maps.
//  4. CopilotRunner.dedupMu       — guards the recent-action.id set.
//  5. CopilotRunner.clientMu      — guards the SDK *copilot.Client.
//  6. CopilotRunner.auditMu       — guards lazy creation of the audit
//                                    directory.
//  7. WorkDirPreparer.mu          — guards the WorkDirHook slice.
//  8. GlobalEventLog.mu           — guards the bounded ring of routing
//                                    events surfaced to the TUI.
//  9. prompttmpl.Renderer (no mu) — immutable after construction; the
//                                    files map is read-only and the
//                                    temp dir is removed via Cleanup.
//
// Practical rules:
//
//   - Never call into a lower-numbered lock while holding a
//     higher-numbered one.
//   - Snapshot APIs (Dispatcher.Snapshot / ListCards) must read
//     channel state (inbox depth) AND capture downstream pointers
//     while holding Dispatcher.mu so a racing Stop / DeleteWorker
//     cannot tear the worker down beneath the read.
//   - Per-card serialisation runs inside the runWorker goroutine;
//     it touches ActivityTracker.mu freely and may call into the SDK
//     (which uses its own internal locks) but must not re-enter the
//     Dispatcher.
//   - Channel sends to handle.inbox always race with Dispatcher.Stop
//     closing handle.kill; the enqueue select must include `<-kill`
//     to avoid a send-to-closed-channel panic.
//
// Adding a new mutex anywhere in this package: extend the list above,
// pick a slot consistent with the call graph, and reference this file
// from the new type's doc comment.
package app
