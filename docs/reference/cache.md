# Cache

A lane can keep a tool's download cache between rounds:

```bash
# Cache: terraform @infra/orders/.terraform.lock.hcl
# Cache: npm @web/package-lock.json pip @requirements.txt
```

| Preset | swim sets | The tool checks the cache against |
|---|---|---|
| `terraform` | `TF_PLUGIN_CACHE_DIR` | provider checksums in `.terraform.lock.hcl` |
| `npm` | `npm_config_cache` | integrity hashes in `package-lock.json` |
| `pip` | `PIP_CACHE_DIR` | `--hash` entries in the requirements file |
| `go` | `GOMODCACHE` (and `GOFLAGS=-mod=readonly`) | `go.sum` |

`SWIM_CACHE_<PRESET>` holds the same path.

- **The cache never decides what runs:** only caches a tool checks are
  presets. The lockfile is required and pinned in the
  [manifest](manifest.md), so changing it stales approvals; a lane whose
  lockfile is missing doesn't start. Use `npm ci` and `pip install
  --require-hashes` (lint warns otherwise).
- **Where:** `~/.cache/swim` (macOS `~/Library/Caches/swim`) or `cache_dir`,
  then `<repo>-<hash of its origin>/<preset>`: clones and worktrees of a repo
  share it. Each directory must be yours, mode 0700, not a symlink; under
  `/tmp` it gets your user id. `cache_dir` and `cache_slug` are config.yml
  only.
- **Parallel:** two lanes' `terraform init` take turns; the rest share.
- **Size:** a run trims the repo's caches to `cache_max` (10GB), least
  recently used first, never one a running lane holds. `swim cache ls`,
  `swim cache rm PRESET`, `swim cache prune --older 30d`.
- **CI:** `swim cache path --ci` prints each preset's directory and a key
  (from its lockfiles) for a CI cache step.

## Snapshots

An installed tree nothing checks on use (`node_modules`, a virtualenv) is
saved whole and restored in a later round, keyed by the files it was
installed from:

```bash
# Cache: snap:web/node_modules @web/package-lock.json
cache restore web/node_modules || { run "npm ci" npm ci --prefix web && cache save web/node_modules; }
```

- The key hashes the tree's name, its `@` files (pinned like lockfiles), OS,
  architecture and any `--with` strings (`cache restore TREE --with "$(node
  --version)"`).
- `cache restore` is a step: `hit KEY`, or `miss` (passes, returns 1 so the
  script installs). A tree already in place is left alone (returns 1).
- `cache save` writes a key once; a save from a round with failed steps is
  marked, and its restores say so.
- A restored tree runs unchecked, so each restore goes in the round's chain
  entry. `swim cache ls` lists them (`snap.<tree>`), `swim cache rm
  snap:TREE` removes them; they count toward `cache_max`.
