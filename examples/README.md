# submend examples

Two runnable scripts, both offline and self-contained. Neither touches your
global git configuration.

## make-broken-repo.sh

Fabricates a superproject with three submodules exhibiting the classic
failure modes: one uninitialized (SM01), one with URL drift after an
upstream move that is also checked out ahead of the recorded commit
(SM02 + SM04), and one with uncommitted edits (SM07).

```bash
bash examples/make-broken-repo.sh /tmp/submend-demo
submend doctor /tmp/submend-demo/super
submend fix    /tmp/submend-demo/super
submend undo   /tmp/submend-demo/super
```

Because `submend fix` writes an undo journal, you can flip between the
broken and mended states as often as you like — the demo is a sandbox for
building trust in the fix/undo loop before running it on a real repo.

## doctor-gate.sh

A pre-push-style gate: runs `submend doctor --format json`, extracts the
error/warning counts, and exits non-zero when anything needs attention.
Drop it into a hooks directory or a task runner:

```bash
bash examples/doctor-gate.sh /path/to/your/repo
```

The JSON envelope is versioned (`schema_version: 1`), so the field names
this script greps for are a stable contract.
