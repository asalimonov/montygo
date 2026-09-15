# Brainstorm: second-stage API feedback

Date: 2026-09-15. Skill: go-brainstorm.
Status: research complete; decisions approved for the v0.2.0 implementation.
Target draft: [second-stage API target](20260915-second-stage-api-feedback-target.md).

## Initial input

> Need to process the second stage feedback to improve montygo API.
> The previous refactoring by feedback was done in the current branch, the latest commit.
> Analyze the feedback and you can read ../montygo-test project where it was created.
> Need to find weak point again and propose in the target architecture for the current project.

Input report: `../montygo-test/docs/reports/20260915-montygo-v0.1.0-api-review.md`.
Reviewed implementation: `08b27a0`, “Major refactoring after test apps, reviews, feedback”.
Previous implementation: `cf99bdd`.

During the research stage only brainstorm documents were changed. Diagnostic
programs were written under `/tmp/montygo-api-review.iyRMcQ`; implementation and
consumer files were unchanged at that point. Subsequent implementation progress
is recorded in `docs/reports/20260915-v0.2.0-implementation.md`.
The consumer already had modified `README.md`, `runner.go`, and an untracked
`docs/reports/` when inspected. Those are the user's work.

`project.txt`, `./architecture`, and applicable `AGENTS.md` files were not found.
The actual architecture directory is `docs/architecture`; `CLAUDE.md` also
describes repository conventions and validation requirements.

## Research scope and evidence

Read the full input report, root README sections about lifecycle/hosts, the
consumer runner and extensions, consumer storage and README, and these current
architecture documents: overview, session, pool, versioning, testing.
Compared the earlier feedback report and prior brainstorm decisions/target
with `session.go`, `run.go`, `snapshot.go`, `answer.go`, `future.go`, `host.go`,
`function.go`, `classinstance.go`, `convert.go`, `options.go`, `errors.go`,
`pool.go`, and the internal checkout/pool implementations.

The previous target contradicts itself: section 4.4 says interrupt touches
only lifecycle state, then section 4.5 explicitly permits snapshot abort under
`s.mu`. The implementation follows the latter. The defect therefore cannot be
attributed solely to failure to follow a sound target design.

The pinned upstream protobuf describes `AbortFeed` as ending a pending
suspension uncatchably and returning the session to ready with an `Error` event.
Source: `../monty/crates/monty-proto/proto/monty/v1/monty.proto:507`.
It is not a control message that can safely be sent during arbitrary Python
execution. Serialization of protocol turns MUST remain an invariant.

### Executed checks

```sh
GOTOOLCHAIN=local go test -race -count=1 -timeout 90s \
  -run '^(TestInterrupt|TestSessionLifecycle|TestResourceBounds)$' .
GOTOOLCHAIN=local go run -race /tmp/montygo-api-review.iyRMcQ/main.go native
GOTOOLCHAIN=local go run -race /tmp/montygo-api-review.iyRMcQ/main.go wasm
```

The selected existing tests passed in 3.819 s on the default native/wasm matrix.
The external probe reproduced F1–F6 below on both native and wasm, including a
final wasm rerun of the idle-CloseNow capacity case. No race detector report occurred. These are incorrect
state transitions, not necessarily unsynchronized memory accesses.

Probe output:

```text
immediate: Interrupt=<nil>; callback started AFTER successful interrupt
cleanup interrupt: KeyboardInterrupt
old Run.Interrupt terminated next run: KeyboardInterrupt
after snapshot interrupt, fresh feed: 4 / <nil>
old snapshot Resume after fresh feed: monty worker protocol error: no suspended call to resume; session Err=monty worker protocol error: no suspended call to resume
failed gather: ValueError: fail; pending after feed=1
Close after CloseNow blocks beyond 20ms context while callback lives
idle CloseNow without later turn/Close: pool stats={Starting:0 Active:1 Idle:0 Retiring:0}
```

The probe restricts Go execution to one P for the immediate-interrupt example,
uses channels to identify host entry, and releases deliberately blocked callbacks
before exiting. It exercises the public API without modifying the repository.
The exact scheduling is diagnostic evidence; the implementation regression suite
MUST additionally force the pre-registration and preparation windows with barriers.

No Docker/network suite was run during this review. Cross-backend design claims
come from shared Go code; WebSocket regression tests remain acceptance work.

## Findings

| ID | Priority | Evidence | Weak point and consequence |
|---|---|---|---|
| F1 | P0 | Reproduced on native/wasm; `run.go:14`, `session.go:128,267,415` | `Go` publishes a handle before registering its execution. An immediate interrupt succeeds without stopping anything. The report's adjacent preparation window also blocks on `s.mu`, outside context control. |
| F2 | P0 | Reproduced on native/wasm; `run.go:33` | A completed run delegates interrupt to its session and stops the next run. A handle has no stable execution identity. |
| F3 | P0 | Reproduced on native/wasm; `snapshot.go:116`, `session.go:128,310` | Snapshot abort is kept in a session-wide consumable error slot. A new feed clears it; resuming the old snapshot sends an invalid resume and poisons the healthy session. When another compatible suspension exists, lack of owner validation can target that suspension instead. The latter case is a code-derived risk, not a reproduced claim. |
| F4 | P1 | Reproduced on native/wasm; `answer.go:registerFuture/answerResolveFutures`, `session.go:endFeed` | A failed feed leaves a future in `life.pending`. Cleanup happens only on delivered resolutions or session loss. Counts/capacity leak across feeds; a map keyed by `*Future` also undercounts two distinct pending call IDs sharing a future. |
| F5 | P1 | Reproduced on native/wasm; `session.go:Close/CloseNow` | `CloseNow` does not set `s.closed`; subsequent `Close(ctx)` still waits on `s.mu`. An uncooperative host callback makes it exceed its deadline, despite the documented no-op guarantee. |
| F6 | P0 | Reproduced on native/wasm; `session.go:CloseNow`, `internal/pool/checkout.go:657` | Idle `CloseNow` kills without `Abandon` or a failing turn, then untracks the session. The dead worker remains Active and consumes pool capacity. The internal `KillWorker` comment explicitly requires the omitted follow-up. |
| F7 | P0 | Code-derived; `session.go:Interrupt` after timer expiry | The kill path rechecks only `hostBusy`, without checking target identity, completion, or `inFeed`. If the old feed ends as the timer wins, it can kill an idle worker or a new feed. Identity check followed by an unlocked kill is still insufficient unless the session is fenced terminal before ownership can advance. |
| F8 | P1 | Code-derived; `answer.go:callHost/answerOsCall`, `snapshot.go:run`, `session.go:LoadSnapshot` | Interruption handling is concentrated around selected callbacks. Restore startup, automatic name lookups, mount servicing, and the transition between callback return and Resume do not share one execution-aware cancellation boundary. |
| F9 | P1 | Code-derived; `session.go:attach/mapError/failedLoad` | Worker watcher, active turn mapping, and local close can race to publish different terminal causes. The watcher always constructs an idle failure, including during a turn. `mapError` can return a different error from the one already stored in `Err`. |
| F10 | P1 | Code-derived; `session.go:Interrupt/abortContext` | Multiple interrupts overwrite the reason and own separate timers. A caller timeout can stop escalation by returning before the timer expires. The fixed 5 s detached abort budget also is not the interrupt caller's deadline. |
| F11 | P2 | Code-derived; `host.go:writeFuncStub`, `function.go:reflectFunction.Call` | Stubs imply named keyword arguments are accepted, while ordinary reflected functions reject them. Parameter labels alone would strengthen that incorrect promise unless stubs use positional-only parameters. Host name validation also permits Python keywords. |
| F12 | P2 | Code-derived; `answer.go:lookupEntry`, `host.go:lookup` | Every host name lookup copies the full registry. Resolving N distinct entries can perform O(N²) copying. The registry is live while objects and stubs were captured earlier, so post-checkout mutation has an unclear contract. |

### What should not be “fixed” by adding promises

1. `Run.Done` cannot promise that the consumer's watcher has updated its own
   state. The consumer owns that ordering. Keep a completion channel or make
   runner finalization idempotent under its own lock.
2. Killing a worker cannot stop arbitrary Go callback code or roll back SQLite
   writes. `Run.Done` must mean driver completion, including any synchronous
   callback it invoked; detached work from `Async`/`AsyncContext` is separate.
   Context cancellation requests cooperation and does not wait for work to stop.
   See [Go context documentation](https://pkg.go.dev/context#CancelFunc).
3. Uncatchable `AbortFeed` cannot implement cooperative Python cleanup. Keep
   cooperative stop as a host function or separate user script protocol when
   required; do not change the worker protocol in this revision.
4. A directory replacement does not provide the dependency's commit in Go build
   metadata. The binary's VCS setting is not evidence of montygo's revision.
   Preserve `(devel)` unless the build explicitly stamps montygo's version.
   See [Go build metadata](https://go.dev/pkg/runtime/debug/?m=old#BuildInfo).

## Disposition of the report's eight recommendations

| Report item | Proposed disposition |
|---|---|
| P0 synchronous run registration | Accept, expand to execution identity for Go, FeedRun, FeedStart, LoadSnapshot, and every resume. Include retirement and stale-timer fencing. |
| P1 explicit interrupt outcome | Accept, but the four proposed values are insufficient: add idle, normal completion race, pending deadline result, and driver-completion information. |
| P1 per-call grace | Accept with `*time.Duration`: nil inherits checkout default, zero means immediate escalation, negative fails validation. One execution-owned watchdog; repeated calls may shorten, never extend, escalation. |
| P2 deliberate close versus loss | Recommend separating `ErrSessionClosed` from `ErrSessionLost` in v0.2.0; `Session.Err()!=nil` remains the general unusable test. |
| P2 automatic struct conversion | Prefer a documented per-object `ConvertValue` recipe now. Tags name fields; they should not silently opt a type into serialization. A host-wide conversion policy needs its own explicit type registry and stub model. |
| P2 API decision guide | Accept, with one immediate-start/stop example and a complete deadline/outcome table. |
| P3 Host.Func parameter names | Include an optional naming hook and fix positional-only stub semantics at the same time. No keyword binding change. |
| P3 replacement VCS fallback | Reject runtime guessing. Document build-time use of the existing version script and linker stamp from the dependency checkout. |

## Design options and recommendations

### Stable ownership

Recommended: one live execution per session, registered synchronously, with a
generation/identity retained by its Run and snapshot chain. Reject overlapping
new executions with `ErrSessionBusy`; do not silently queue them.

Alternative: a FIFO of pending runs. This is possible but introduces admission
limits, per-queued-run cancellation, shutdown draining, and ambiguity for
`Session.Interrupt`. The current architecture already documents one goroutine
using a session at a time. A queue is not needed to fix the consumer.

### Interrupt API and deadlines

Recommended: `Interrupt(ctx, InterruptOptions) (InterruptResult, error)`.
Keep `Run.Wait()` and add `WaitContext(ctx)` for a bounded wait. Keep `Go`'s
existing return type; admission failures produce an already-completed Run.

Interrupt publication is synchronous under the short lifecycle lock. Protocol
actions stay with the execution owner; a paused snapshot can transfer ownership
to one abort task. The public call never waits for a protocol mutex.

The per-call context bounds waiting for the result. Once accepted, the stop
request and its watchdog persist even if that caller stops waiting. If callers
need immediate force escalation, they pass `Grace: DurationPtr(0)`.

When a kill is necessary, report that the session is terminal even if an
uncooperative Go callback still prevents driver completion. Do not falsely close
`Run.Done`. An interrupt outcome must distinguish these two facts.

### Resource ownership

Futures belong to an execution and are indexed by wire call ID, not pointer.
End-of-execution cleanup drops all subscriptions and cancels the callback
context; it does not settle a caller-owned Future that may be shared elsewhere.
Idle or active worker retirement must have exactly one accounting owner and
must not depend on a future public method call.

## User discussion

Q1, asked asynchronously: should this revision allow a breaking v0.2.0 cleanup,
or preserve v0.1.0 signatures through additive methods?

Answer: **Allow breaking cleanup for v0.2.0.** Received after the user resumed
the analysis with “Continue”. The target uses one revised Interrupt API.

Q2, asked asynchronously: reject overlapping executions, or add a FIFO queue?
Answer: **Reject with ErrSessionBusy.** The target rejects new executions while
a feed or snapshot chain owns the session; no implicit execution queue.

Q3, asked asynchronously: should grace also cover a Go callback that ignores
cancellation, or keep the worker alive until that callback returns?
Answer: **Force worker termination after grace.** Report InterruptKilled with
RunDone=false while the driver remains blocked. Do not fake callback completion.

## NOT CONSIDERED & TODO

| Item | Tag | Proposed handling |
|---|---|---|
| Queued executions and queue admission budgets | postponed | Reject overlap with ErrSessionBusy. Separate feature if requested. |
| Catchable/cooperative Python stop | postponed | Existing host function mechanism; no AbortFeed protocol changes. |
| Forcibly preempting arbitrary Go callbacks | too-complex | Unsupported by this design; contexts remain cooperative. Isolate such code in an application-owned process when hard termination is required. |
| Automatic host-wide tagged struct conversion and generated structural stubs | potential-improvement | Explicit ConvertValue recipe now; design an opt-in type registry separately. |
| Runtime discovery of a replaced dependency's git revision | postponed | Keep truthful unknown/devel; use build-time stamping. |
| Hot-mutating a registered Host | postponed | Configure before checkout; no new hot-reload guarantee. O(1) lookup can be implemented independently. |
| Native/wasm/WebSocket exhaustive race regression matrix | potential-improvement | Required during implementation; this review ran local probes and focused existing tests only. |
| Pool.Shutdown's existing post-deadline 5 s drain policy | postponed | Document separately from strict Run.WaitContext/Interrupt deadlines; do not silently change it in this API correction. |

## Interim conclusion

The central weakness is the absence of a durable owner for an execution and its
resources. Fixing only `beginFeed` timing leaves stale handles, snapshot errors,
kill timers, futures, and checkout accounting exposed to the same class of
mistake. The target specifies one execution identity, one protocol owner, one
terminal publication path, and explicit stop-versus-completion semantics.

The user approved the breaking v0.2.0 API policy, rejection of overlapping
executions, and worker force termination after grace even during a blocked host
callback. The target was confirmed and implemented on the current branch;
implementation evidence is recorded in
`docs/reports/20260915-v0.2.0-implementation.md`.

The final draft includes concrete public/internal structures and signatures,
snapshot ownership and context rules, force-versus-driver-completion publication,
lease retirement, canonical errors, future cleanup, host metadata/conversion
guidance, unchanged database/config/route schemas, detailed flows, failure modes,
consumer migration, regression requirements, and key-method pseudocode.
The brainstorm and target documents passed whitespace checks before
implementation began. The sibling consumer repository was kept read-only.
