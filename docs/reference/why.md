# Why swim

**A safety and audit layer for letting AI write production operations work
without letting it run that work.**

## The problems

- **AI can write changes but shouldn't run them:** it lacks the
  credentials, the network or the authority.
- **Copying from chat breaks:** quoting breaks, steps get skipped, output
  comes back partial.
- **Parallel work collides:** sessions overwrite each other's scripts, and
  nobody knows what's running.
- **Approvals aren't deliberate,** and afterwards **there's no proof** of
  which script ran or who approved it.
- **Logs cost tokens,** and **work restarts from zero** when the session
  ends.
- **Each side waits on the other:** the AI stalls on the operator, who is
  interrupted for every step.

## What it is, and isn't

- **One binary, no server.** Lane scripts and state are git-ignored by
  default, so swim lives in your checkout; teammates who do pick it up get
  approvals and the record across machines.
- **The operator's machine, or a swimmer they control, is the only
  executor.** No running from the planner's side.
- **Not secret management:** rounds use your existing credentials.
- **Not a dashboard:** a swimmer is seen from its own terminal, the
  queue and status files, or CI.
- **swim itself never writes to git**; a lane may, behind a guard.

## Compared

| | swim does differently |
|---|---|
| **Agents that act directly** | Where a mistake is cheap (tests, local iteration), let the AI act. Where the credentials or consequences are yours, swim shows the whole plan as a file first, and the AI never holds the keys. |
| **Ansible** | Wants reusable, idempotent playbooks; `--check` covers a whole play, not a step; its output isn't written for an AI. swim is for the one-off script an AI wrote this morning. Call `ansible-playbook` from a guarded step to get both. |
| **Rundeck, AWX** | Servers for curated, shared runbooks. swim's rounds are disposable and run from your shell. |
| **Airflow, Dagster** | Recurring pipelines on workers you deploy. swim's DAG is today's cutover, run once: `# After: 2`, no scheduler. |
| **Temporal** | Durable application workflows you build a product on. swim is something an operator runs. |
| **Ticket or PR approvals** | A swim approval binds the exact script and everything it pins, is SSH-signed, and lands in a chain anyone can verify. |
