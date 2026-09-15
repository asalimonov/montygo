# In-memory OS helpers

Package `osaccess` ports the Python binding's `os_access.py`.

- `Ops` has one method per OS call name. `Dispatch` routes `(name, args, kwargs)` to it and maps `ErrNotImplemented` to `monty.NotHandled`, so an embedding `Base` declines everything it does not override.
- `OSAccess` is an insertion-ordered in-memory tree of `File`s (`MemoryFile`, `CallbackFile`, or custom implementations). Directories are inferred from file paths; relative file paths are rebased onto the root directory.
- Errors are `monty.Raise` values with CPython's exception types and errno messages, so they cross into the sandbox typed.
- An `OSAccess` holds a mutex and may be shared by concurrent sessions. A `CallbackFile` callback MUST NOT call back into the same `OSAccess`.
- Renaming a directory into its own subtree raises `OSError: [Errno 22]` instead of corrupting the tree.
