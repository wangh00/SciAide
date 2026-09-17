# Repository Artifacts

Disposable repository-local output belongs under `.tmp/`:

- review exports and diagnostic dumps;
- UI screenshots and smoke-test run data;
- development server logs;
- temporary databases and other generated test files.

The `.tmp/` directory is ignored by Git and may be removed between runs. Do not place disposable files in the repository root.

Runtime cache has a separate ownership boundary. Application-wide cache remains under `~/.sciaide/cache`; project
attachments, parse/index data, and run-derived files remain under `<Workspace>/.sciaide/cache` or its private `tmp` directory.
Formal build outputs such as `build/bin/SciAide.exe` remain under `build/` and are not moved into `.tmp/`.
