# Env files

Lanes get non-secret context (`STAGE`, `AWS_PROFILE`, table names) from
`KEY=VALUE` files. swim parses them; it never runs them as shell.

```ini
# ~/.config/swim/env.swim: general keys, then [repo PATH] sections
STAGE=dev
[repo ~/dev/orders]
AWS_PROFILE=orders-prod

# <repo>/.env.swim (git-ignored): [lane N] sections only
[lane 2]
TABLE=orders-v2
```

- **Precedence:** the shell, then the lane's section, the repo's section,
  the general keys.
- **Shared values:** nothing committed gives lanes env. A non-secret value
  every operator and CI needs (`LOG_LEVEL=info`) goes in the lane script
  itself (`export LOG_LEVEL=info`), or in the CI job's `env:`.
- **Format:** `KEY=VALUE`, `#` comments, quotes around a value. No `export`,
  `$VAR` or `$(...)`: a line that doesn't parse stops the run, naming the
  file and line.
- **No guard flags:** a key that is a selected lane's guard stops the run.
- **Masked:** every value (6 characters or more) is masked wherever swim
  writes. Still, store `AWS_PROFILE`, not the key.
- **Recorded:** the round header lists the files that applied; status and
  the chain record the env's hash. A queued lane whose env changed is
  skipped.
- `swim env N` shows what a lane gets and where each key comes from. A lane
  run directly with bash doesn't get them.
