# Safety

- **Secrets are masked**, as a backstop: values of `secret_env` variables,
  names like `*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `*_API_KEY`
  (`secret_env_auto`), and credential shapes (AWS key ids, GitHub and Slack
  tokens, JWTs; `secret_patterns_auto`) become `***` in output, commands,
  snapshots, labels, status and `.swim.log`, including base64 and
  URL-encoded forms. Not output a step writes elsewhere, or values split or
  transformed. Never print secrets: print keys or lengths (`echo
  ${#API_KEY}`). Never list one in `header_env`.
- **git and gh are a lane's to run.** Put one that changes a shared remote
  behind a guard. swim blocks only a repo's `blocked_commands`: in a script,
  the lane doesn't start; built at runtime, the step is refused (`BLOCKED`,
  exit 87) and the round stops. The match is on text, so even an `echo` of
  one is refused.
- **Snapshot before any change.** Back up stateful data before removing it,
  and know how you'd restore it.
- **Read the deployed state** (a stack's `DeletionPolicy`); never assume it
  from code.
- **One guard per destructive or irreversible action**, declared with its
  date and reason. Never default a flag on.
- **Prefer reversible operations** (disable, keep a copy) to deletes when
  ownership is unclear.
- **Probes have no side effects:** no messages, emails, payments or
  mutations; use rejected inputs, health routes, dry runs.
- **Fail closed:** a gate or `confirm` with no answer stops the round.
- **Rounds are safe to rerun.**
- **Running lanes are protected:** swim won't edit, archive or rerun
  one.
- **Approval proves who decided, not that a script is safe.** A guard runs
  in the lane, and an approved script runs with the operator's access. See
  the [trust model](trust-model.md). When something breaks, see
  [recovery](recovery.md); never edit `.swim/chain.jsonl`.
