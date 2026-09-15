# Session, drive loop and host objects

## Session state

- A session is used by one goroutine at a time; its mutex is held for a whole call, including host callbacks. A callback MUST NOT call back into the same session.
- `broken` poisons the session after a crash, a protocol error or a host failure that left the worker suspended.
- `driven` makes `LoadSession` and `LoadSnapshot` valid only on a fresh session.

## Drive loop

`FeedRun` answers suspensions until the turn completes (`answer.go`):

- **FunctionCall**: a host function from `ExternalLookup`, or a method on a registered wrapper when `ObjectID` is set. A returned `*Future` becomes a sandbox future, or is awaited directly when the worker allows an eager await.
- **NameLookup**: a function entry resolves to an external function, a value entry to the value, an absent name stays undefined. With `ObjectID` set it is a lazy attribute of a wrapper.
- **OsCall**: mounts first, then the OS handler, then the worker's default.
- **ResolveFutures**: waits for at least one pending future and delivers every settled one.

`FeedStart` surfaces each suspension as a single-use snapshot; `ResumeAuto` runs the same answerer for one step.

## Print

Print segments stream to the feed's print target during the turn. A target error is kept and returned at the turn boundary; on a suspension it poisons the session.

## Value conversion

- `prepareValue` normalizes Go values into `internal/value`: Go ints become `int64`, slices become lists, maps become dicts with sorted keys, wrappers become wire markers and are registered.
- `restoreValue` maps instance markers back to the original host object, or to `*ClassProxy`.
- Conversion failures of inputs fail host-side; failures of host return values raise inside the sandbox.

## Host objects

- `ClassInstance` and `ClassType` are wrappers with attribute and method policies.
- Every wrapper sent into a session is stored by id in the session's instance store until the session closes. Reusing an id for a different object is rejected.
- Reflection exposes exported fields and methods under snake_case names or a `monty` field tag. Unexported members are never reachable.
