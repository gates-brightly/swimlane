# Modules: fetched modules, and what's left

Status: in progress. Built and documented (MODULES in `swim guide`, the
README's Modules section): `# Modules:` (script syntax 4), `mod`,
`mod_env`, `mod --evidence`, `swim mod`, gate modules, per-call effects
(`read_only_for`, `destructive_for`), repo modules in bash and Go (built
as the lane is staged), the manifest pins, `swim.yml` `modules:`, `swim
modules ls|show|new|check|lock|get`, version minimums and the lock's pins
of built-ins, plan, approve, lint (`module`, `module-effects`,
`module-lock`), doctor (`module-go`, `module-version`, `module-fetched`),
op `module` in the SDKs, ten built-ins, and fetched modules (phase three,
below) except signatures.

Left: signed versions of fetched modules (`modules.require_signed`), and
the gap below.

## Rules phase three keeps

- **Go and bash only.** SDK lanes call them through op `module`.
- **Never fetched at run time.** What runs is in the manifest, by hash,
  before approval.
- **A module's configuration is in its call** (`mod NAME args`, `--bin`,
  `-- CMD ARGS`), never in a swim setting or a `SWIM_*` variable: the
  approved script says what runs.
- **Effects are declared, not enforced.** A module runs as the lane.
- **No module decides what a round does.** A module is a step; the script
  decides. A Claude call only writes an artifact.

## Phase three: fetched just in time

Built: source addresses, `swim modules get [--upgrade]`, the lock's
`modules: fetched:` pins (version, commit, files' hash), the store under
`.swim/modules/<hash>/` checked at run time, address globs in `allow:`,
the manifest pinning a fetched module like a repo one (so an upgrade
stales its approvals), and doctor's `module-fetched`. Not yet: signatures.

Like Terraform modules: a source address, a fetch step before anything
runs, and a lockfile. Never a fetch at run time.

```bash
# Modules: github.com/org/swim-modules//pagerduty@2
```

- **`swim modules get`** resolves each source address to an exact version
  (a tag matching the major, or a commit), downloads it, checks its tree
  hash, and stores it content-addressed in `.swim/modules/<tree sha>/`. It
  records the result in `.swim.lock`:
  ```yaml
  modules:
    github.com/org/swim-modules//pagerduty:
      version: 2.3.1
      commit: 9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e
      sha256: 5be1…            # the module tree
      signed_by: zach          # optional: checked against trust.yml
  ```
- **Run time only reads the lock.** A lane naming a fetched module that
  isn't in `.swim/modules/` with the locked hash doesn't start: `module
  pagerduty 2.3.1 isn't here: swim modules get`. A queued or scheduled
  batch pins the module's hash with the rest of its manifest.
- **Changing the lock is a reviewed diff.** `.swim.lock` is committed, so an
  upgrade (`swim modules get --upgrade pagerduty`) shows up in review, and
  stales every approval of a lane that uses the module.
- **Signatures, optional:** a source can publish an SSH signature over each
  version's tree hash. With `modules.require_signed: true` in `swim.yml`,
  only versions signed by a key in `trust.yml` are fetched, the same trust
  `swim verify` uses.
- **Go modules fetched this way** are built locally, as repo modules are.
- **Policy:** `swim.yml` `modules: allow:` takes source globs
  (`github.com/org/swim-modules//*`) beside names and `./paths`.

## Gaps found while building

- **A `mod_env` value a program echoes itself**, outside a step, isn't
  masked on the terminal or in swim run's view, like any secret a script
  prints itself (the guide says so). The wrapper's capture of a program's
  own output picks up `mod_env`'s values (since the commit that closed the
  other gaps), though it's logged only for a program that runs no steps.

Closed: `swim mod` for programs without an SDK; e2e tests for a compiled
Go SDK lane and a sidecar's `modules:`; a Go repo module built as the lane
is staged and recorded under the manifest's `local: built:`; stage and
evidence detection reading quoted text (the header contract, `e186f33`).

## Design (phase three)

| Area | Change |
|---|---|
| `internal/module` | Source addresses in `ParseRef`; resolution against the lock; the content-addressed store under `.swim/modules/<tree sha>/`. |
| `internal/cli` | `swim modules get [--upgrade NAME]`: resolve, download (git), check the tree hash and the signature, write the lock. |
| `.swim.lock`, `internal/version` | The `modules:` section. It adds a key, so no syntax bump unless its shape needs one. |
| `internal/config` | `modules: require_signed`, and source globs in `allow:`, in the policy hash. |
| `internal/manifest` | A fetched module pinned like a repo one: `source` is its address, its files staged from the store. |
| `internal/doctor` | A locked module missing from the store, a lock entry no lane uses. |

## Open questions

- **Prebuilt Go binaries for fetched modules.** Building needs Go on every
  machine that runs the lane; prebuilt per-platform binaries, each hashed
  in the lock, need none, but the approval then covers a binary nobody
  reviewed, only its source's claim. Leaning: build, as now.
- **Built-in module upgrades.** A swim release that changes a built-in
  stales every approval of a lane using it: correct, but noisy. Version
  built-ins apart from swim, with old versions kept for a release or two?

## Testing

- `swim modules get` writes the lock; a lane whose fetched module is
  missing doesn't start; a tampered module tree is refused; an unsigned
  version is refused under `require_signed`; an upgrade stales approvals.

## Out of scope

- Running GitHub Actions (`uses:`) or emulating them. Modules may cover the
  same needs, written for swim.
- Modules in Python or TypeScript. SDK lanes in those languages call Go
  and bash modules through the lane protocol.
- A module registry server. Phase three fetches from git hosts by source
  address.
- Modules that decide what a round does, Claude-based or otherwise.
- Sandboxing modules. They run as the lane, with its permissions; scoped
  credentials and OS isolation are the brief's answer to that, not this.
