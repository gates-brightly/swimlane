# swim cache: keep dependency downloads between rounds

Status: in progress (phase one built: presets, `swim cache`, lint, doctor, docs)

## Summary

A lane declares the tool caches it uses, and swim points each tool's own
download cache at a per-repo, per-user directory that outlives the round:

```bash
# Round: plan the orders stack
# Cache: terraform @infra/orders/.terraform.lock.hcl
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3

stage check
run "tf init" terraform -chdir=infra/orders init -input=false   # providers come from the cache
run "tf plan" terraform -chdir=infra/orders plan -detailed-exitcode
summary
```

Four presets: `terraform`, `npm`, `pip`, `go`. swim sets the tool's cache
variable (`TF_PLUGIN_CACHE_DIR`, `npm_config_cache`, `PIP_CACHE_DIR`,
`GOMODCACHE`), so `terraform init`, `npm ci`, `pip install` and `go mod
download` skip the network for what they already have. **The tool still
checks everything it takes from the cache against the lockfile, and the
lockfile is pinned in the approval.** The cache only speeds a round up; it
never decides what code runs.

```
$ swim run 3
 swim 3  PASS  plan the orders stack   0:41   (was 3:05 without the cache)

$ swim cache ls
swimlane-3f2a9c  ~/Library/Caches/swim/swimlane-3f2a9c   1.7 GB of 10 GB
  terraform   612 MB   last used 2m ago   by swim 3, swim 4
  npm         1.1 GB   last used 1d ago   by swim 1
```

Caching installed trees (`node_modules`, `.terraform/`, a virtualenv) is
deliberately left out. See [Later: snapshots](#later-snapshots-if-measured).

## Motivation

- **Rounds start from nothing.** Every `terraform init` downloads its
  providers again, every `npm ci` refetches the registry, every `pip
  install` rebuilds wheels. On a VPN that's minutes per round, and rounds are
  meant to be cheap to rerun. Parallel lanes multiply it: four lanes each
  initialising the AWS provider download it four times.
- **Lanes cache by hand today, badly.** A round that wants speed writes to
  `~/.terraform.d/plugin-cache` or reuses `node_modules` in the working tree.
  That's shared across repos, invisible to `swim plan`, unsafe when two
  lanes run at once, and never cleaned up.
- **The integrity story has to survive it.** swim's promise is that what
  runs is what was approved (*Integrity* in `swimlane.md`). A cache that
  hands a round code nobody pinned breaks that quietly. Caching only
  downloads the tool checks against a pinned lockfile keeps the check where it
  already is.

## Assumptions checked

| Assumption in the ask | Holds? | Consequence |
|---|---|---|
| `/tmp/swim_cache/<repo>/<key>` | **Partly.** `/tmp` is shared by every user on the machine. Another local user could create `/tmp/swim_cache` first and plant files in it, or symlink into somewhere the lane writes. macOS also clears `/tmp` after 3 days of disuse, and many Linux hosts on reboot. | Default to the per-user cache directory: `$XDG_CACHE_HOME/swim` (`~/.cache/swim`), or `~/Library/Caches/swim` on macOS, mode `0700`. `cache_dir` in config can point elsewhere. A path under `/tmp` gets a per-user suffix (`/tmp/swim_cache-<uid>`), and swim refuses a cache directory it doesn't own or that others can write. |
| A repo slug names the repo | **Not uniquely.** Two checkouts called `api` collide. A worktree has a different path but wants the same cache. | Slug = basename + the first 6 hex characters of a hash of the `origin` remote URL, or of the root path when there's no remote. Worktrees and clones of one repo share; same-named repos don't. |
| Each entry needs a `<key>` | **Not for tool caches.** They're already safe across platforms and versions: Terraform's plugin cache is laid out per platform, npm's cache holds platform-neutral tarballs (native builds happen at install time), pip tags wheels by platform and Python version, and Go's module cache is platform-independent. | One directory per preset per repo, no key. Keys only matter for installed trees, which are left out (snapshots, below). |
| Parallel lanes can share a cache | **Depends on the tool.** npm's, pip's and Go's module caches are safe to share. Terraform's plugin cache is documented as **not** safe for concurrent `init`. | Each preset says whether it's shared or exclusive. swim takes a machine-wide lock on an exclusive cache around each step that uses it, so two lanes' `terraform init` queue instead of corrupting it. |
| Caching installs is like caching downloads | **No.** A download cache is checked on use: npm against lockfile integrity hashes, Terraform against `.terraform.lock.hcl` checksums, Go against `go.sum`, pip with `--require-hashes`. An installed tree (`node_modules`, a venv, `.terraform/`) or a build cache (`GOCACHE`) is just run. | Presets cache downloads only. `GOCACHE` isn't set by the `go` preset for the same reason. |

## Behaviour

### Declaring a cache

- **`# Cache: <preset> @<lockfile> [...]`** in the header (any runtime;
  `cache:` in `lane.N.yml`). Several are allowed:
  `# Cache: npm @web/package-lock.json terraform @infra/orders/.terraform.lock.hcl`.
- **Each preset needs its lockfile.** The `@` file is required: `swim lint`
  makes a missing one an error, and the lane refuses to start. Without a
  lockfile, Terraform trusts whatever is in its plugin cache and records it,
  and npm and pip have nothing to check against. Requiring the lockfile is
  what keeps a cached file from becoming code nobody approved.
- **The `@` files are pinned into the manifest** (as `includes`), like
  `Matrix: @files`.
  Change the lockfile and the approval goes stale, so the approval covers
  exactly what the tool will accept from the cache.
- **Undeclared caches don't exist for the lane.** Its environment carries
  only the presets it declares. `swim plan` and `swim lint` show what each
  lane caches.
- **A pip lockfile must carry hashes.** `swim lint` makes a requirements
  file with no `--hash=` entries an error: pip has nothing to check its cache
  against without them.
- **Run directly** (`bash lane.N.sh`), a lane gets the same variables from
  `lane_init` (`swim _cache-env N`), and is refused the same way. It doesn't
  hold its caches against eviction, as only `swim run` keeps a lane's process
  holding files.

### Presets

| Name | swim sets | Lockfile | Mode | Tool checks with |
|---|---|---|---|---|
| `terraform` | `TF_PLUGIN_CACHE_DIR` | `.terraform.lock.hcl` | exclusive per step | provider checksums in the lock file |
| `npm` | `npm_config_cache` | `package-lock.json` (or `npm-shrinkwrap.json`) | shared | `integrity` hashes in the lockfile |
| `pip` | `PIP_CACHE_DIR` | a requirements file with `--hash` entries | shared | `--require-hashes` |
| `go` | `GOMODCACHE` | `go.sum` | shared | `go.sum` (and `GOFLAGS=-mod=readonly`, so a step can't rewrite it) |

- **Set for the whole lane**, before the script starts, in every step's
  environment. A step's command never needs to mention the cache.
  `SWIM_CACHE_<PRESET>` carries the same path, for scripts that want it.
- **Exclusive presets** lock per step, and only for the steps that write
  the cache: for `terraform`, a step whose command runs `terraform init` or
  `terraform get` (found in its words, so `bash -c "cd x && terraform init"`
  counts). `plan` and `apply` only read the providers `init` linked, so
  they never wait. A step waiting on the lock says so in the lane's output.
  With a warm cache, `init` holds it for seconds.
- **Lint warnings** for commands that bypass the check in a lane that
  caches them: `npm install` (not `npm ci`), `pip install` without
  `--require-hashes` (or `-r` of a hashed file), and
  `TF_PLUGIN_CACHE_MAY_BREAK_DEPENDENCY_LOCK_FILE` set anywhere.
- **More presets** (Bundler, Cargo, Gradle) are additions to one table. Each
  needs the tool to check what it takes from its cache against a lockfile,
  or it isn't a preset.
- **Terraform's links.** Terraform links providers from its cache into
  `.terraform/`. A provider evicted from the cache leaves a broken link
  until the next `init`, which rounds run anyway. Eviction never removes a
  cache a running lane holds (below).

### Managing it

```
swim cache ls [--yaml]                   # this repo's caches: size, last use, by which lanes
swim cache rm PRESET|--all               # delete (asks at a terminal)
swim cache prune [--older D] [--max SIZE]
swim cache path [PRESET] [--ci]          # where it is
```

- **A size cap:** `cache_max` (config, default 10 GB per repo). At the end
  of a run, if the repo is over it, swim removes whole preset directories,
  least recently used first. A tool simply refetches what it needs. It never
  removes a cache a running lane holds.
- **`swim doctor`** reports cache size, a cache directory with the wrong
  owner or permissions, and caches unused for 30 days.
- **`swim clean` / `swim prune`** ([clean.md](clean.md)) don't touch the
  cache. It isn't in the repo, and it isn't the record.

### CI and other machines

- **The cache is per machine.** A worker on another host has its own.
- **`swim ci`**: CI runners start empty, so the cache only helps if CI
  restores the directory. `swim cache path --ci` prints `cache_dir` and a
  key per preset (the preset plus a hash of its lockfiles), so a GitHub
  Actions `actions/cache` step or a GitLab `cache:` block can restore and
  save it around `swim ci`. swim doesn't talk to CI caches itself.

## Decisions

- **Snapshots are deferred.** Presets cover `terraform init`, `npm ci`,
  `pip install` and `go mod download` with the tool still checking every
  file. With a warm cache, `terraform init` links providers in seconds and
  pip installs from cached wheels. The remaining gap is `npm ci` unpacking a
  large tree. That doesn't justify the one part of this feature that runs
  files nothing checks. Measure first: see the next section.
- **Presets get no chain field.** The tool checks what it takes from the
  cache, and the lockfile is already pinned in the manifest the chain
  records. A chain field would add nothing `swim verify` can check.
- **The slug without a remote hashes the root path.** A moved checkout
  starts with a cold cache. That costs one slow run, which isn't worth a
  repo id committed to every repo's `swim.yml` (and a copied repo would then
  share the id). If it ever matters, `cache_slug` in config overrides it.
- **Built in, not a module.** Presets need things a module can't do
  without core changes: exporting variables before the script starts, the
  machine-wide lock around a step, pinning `@` files into the manifest, and
  refusing a lane whose cache directory fails its checks. There's also no
  modules spec yet, and this shouldn't wait for one. A remote cache sync (S3,
  the hub) is the natural first cache *module* later, on top of this layout.

## Later: snapshots (if measured)

Only if [observe.md](observe.md)'s step timings show `npm ci` (or similar)
with a warm preset cache costing enough rounds enough time. If they come
back, keep them narrow:

- **`cache key` / `cache restore` / `cache save`**, restore and save as
  logged steps, declared in the header (`# Cache: snap:node_modules @web/package-lock.json`).
- **Keyed** by name, the hashes of the `@` files, OS, architecture and
  `--with` strings (`--with "$(node --version)"`). Installed trees are
  platform- and version-specific, unlike download caches.
- **Written once:** saved into a temp directory and renamed into place; a
  key is never overwritten; never saved from an outcome handler; saves from
  a failed round marked and warned about on restore.
- **Per repo only**, never shared across repos even with the same key.
- **A miss is never a failure:** `PASS  cache restore node_modules (miss)`,
  and the bash function returns 1 so the script can branch.
- **In the chain:** a snapshot is exactly the files that ran without being
  approved or checked, so the round entry records them:
  `cache: [{key, tree, saved_by_job}]`. An auditor can then list the rounds
  that ran on unpinned files without parsing logs. The log block (key, tree
  hash, who saved it) is already covered by the chain's log hash.

## Design

| Area | Change |
|---|---|
| `internal/cache` (new) | Directory resolution (config, XDG/macOS default, `/tmp` suffix, ownership and mode checks); repo slug; the presets table; per-cache machine-wide flock; last-use index (`index.yml` per repo); size accounting and whole-preset eviction. |
| `internal/lane` | Parse `# Cache:` in every runtime and `lane.N.yml`. |
| `internal/manifest` (`manifest.go:166`) | Pin `@` files as `from: cache`, like includes. |
| `internal/launcher` / `laneEnv` | Export the declared presets' variables (and `GOFLAGS=-mod=readonly` for `go`); refuse to start a lane whose cache directory fails its checks, or whose preset has no lockfile (a clear error naming the path and its owner). Record last use and evict at run end. |
| `internal/cli/commands.go` (`swim step`) | Take an exclusive preset's lock around each step of a lane that declares it. Show the wait in status and the live view. |
| `internal/cli/cache.go` (new) | `swim cache ls|rm|prune|path`, `--yaml`, `--ci`. |
| `internal/lint` | Unknown preset; missing or absent `@` lockfile (error); the bypass warnings above. |
| plan, status | What each lane caches; lock waits. |
| `internal/doctor`, `internal/config` | `cache_dir`, `cache_max`, `cache_slug`; the doctor checks above. |
| `internal/help`, README | A CACHE section after OUTPUTS; `# Cache:` in the header list; the four presets and what each checks; the lint warnings; the CI recipe. |

### Implementation steps

1. **`internal/cache` foundations:** directory resolution with the
   ownership and mode checks, and the repo slug. Unit tests: `/tmp` suffix,
   refusal of a directory others can write, worktrees sharing a slug, no
   remote falling back to the root path.
2. **`# Cache:` header and manifest pinning** of the lockfiles; lint errors
   for unknown presets and missing lockfiles.
3. **Presets:** env export, `GOFLAGS` for `go`, the exclusive lock for
   `terraform`. e2e with stub tools that record whether they read their
   cache.
4. **Management:** `ls`, `rm`, `prune`, `path`, the size cap and eviction,
   doctor.
5. **Surfaces:** plan, status, the lint bypass warnings.
6. **Docs**, and the CI recipe for `actions/cache`.

## Testing

- **Unit, resolution:** each OS default; `cache_dir: /tmp/x` becomes
  `/tmp/x-<uid>`; a directory owned by another user, or mode `0777`, is
  refused with its owner named; a symlinked preset directory is refused.
- **Unit, slug:** two clones of one remote share a slug; two repos named
  `api` with different remotes don't; with no remote, moving the checkout
  changes it.
- **e2e, preset:** a lane with `# Cache: npm @package-lock.json` against a
  local registry stub: the first run fetches, the second doesn't, and both
  pass. Editing the lockfile stales a signed approval.
- **e2e, missing lockfile:** `# Cache: terraform` with no `@` file is a lint
  error, and `swim run` refuses the lane.
- **e2e, exclusive lock:** two lanes with `# Cache: terraform` and a stub
  `terraform init` that fails if it isn't alone in the directory: both pass,
  and one waited.
- **e2e, undeclared:** a lane that declares only `npm` has no
  `TF_PLUGIN_CACHE_DIR` in its steps' environment.
- **Eviction:** over `cache_max`, the least recently used preset directory
  goes first, and a cache held by a running lane is never removed.

## Out of scope

- Snapshots of installed trees, for now (see above).
- Build caches that nothing checks (`GOCACHE`, `.terraform/` as a whole).
- Remote or shared caches (S3, a cache server, the hub). A later module
  could sync the cache directory; the layout shouldn't preclude it.
- Caching a step's result (skipping a step whose inputs haven't changed).
  That's memoisation, and it changes what a round does, not how fast.
- Talking to CI providers' cache services.
