# swim

Operator-in-the-loop automation. An AI planner and its workers write bash
lane scripts; a human operator runs them with `swim run`, in environments the
session can't reach; every step and result lands in a log and in
`.swim/status.yml` for the session to read back. See `swimlane.md` for the
product brief.

## Install

```sh
make install                 # go install with version stamped; or: make build (-> bin/swim)
```

The module path is `swim`. Change it in `go.mod` to the repo's real import
path before publishing, so `go install <path>/cmd/swim@latest` works.

## Quick start

```sh
cd your-repo
swim init                              # config entry, .gitignore block, .swim/status.yml
swim new 1 "Cut over orders-api"       # writes lane.1.sh from the template; edit its steps
swim plan                              # dry run: tree of what swim all would do, guard flags, skips
swim all                               # runs every pending lane (same as `swim run`); live pinned view
swim status                            # last state of every lane (or: cat .swim/status.yml)
swim note "orders table kept: still read by billing"   # record a decision in .swim.log
swim log                               # project history (.swim.log): one line per top-level action
swim log 2                             # lane 2's current log; --all adds its archives
swim log 3f2a9c1e                      # just the rounds of one job
swim archive 1 && swim stub 1 "orders done"   # archive name defaults to the job id
```

Dependencies are usually declared by the round itself, in the lane script header:

```sh
# Round: Diamond join: build report from child A and child B
# After: 2 3          # wait for swim 2 and swim 3 to pass (lane numbers or job ids)
```

Ordering that holds for every round, and other settings, live in `~/.config/swim/config.yml`:

```yaml
defaults:
  lanes: 4
  header_env: [STAGE, AWS_PROFILE]
repos:
  /path/to/your-repo:
    deps: {2: [1], 4: [2]}   # swim 2 waits for swim 1, swim 4 for swim 2
```

## For AI agents

Run `swim --help`. It covers the planner and worker protocol, the lane script
library (`run`, `gate`, `snapshot`, `guard`, `confirm`, ...), the safety
rules, the log grammar and the `status.yml` schema.

## Development

```sh
make check                            # gofmt, vet, unit + end-to-end tests
make test-unit                        # skip the e2e suite
go test ./internal/display -update    # regenerate panel golden files
```

`internal/assets/lib.sh` must stay compatible with macOS bash 3.2. A test
checks for bash 4 features, but the end-to-end suite runs with whatever
`bash` is on PATH, so also run it on a Mac before releasing.
