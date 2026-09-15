# Mounts

`internal/mountfs` ports upstream `monty-fs`.

- A mount opens its host directory once with `os.OpenRoot`; every operation resolves relative to that descriptor. Renaming the directory later does not change what the mount serves.
- A table is built per feed. The longest virtual prefix wins. A rename across mounts is refused.
- Path policy runs before routing: length limits, NUL bytes, drive and UNC segments. Existence checks answer `False` instead of raising.
- Symlinks: in-mount relative targets are followed, absolute targets are refused, and overlay mode refuses symlinks in any component.
- Special files are opened non-blocking and rejected with `PermissionError`.
- `read-only` refuses writes; `read-write` writes through; `overlay` keeps copy-on-write state in memory for one feed (files, lazy references to real files, directories and whiteouts).
- A memory budget (default 100 MB) covers overlay data and transient results. `WriteBytesLimit` caps bytes written per feed.
- Errors carry POSIX errnos and CPython's messages, and never a host path.
