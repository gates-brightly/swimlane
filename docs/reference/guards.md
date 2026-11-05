# Guards

```bash
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (2026-10-09: tf owns it)"; then
  run "sst remove" npx sst remove --stage prod
fi
```

The action string is what the person approves, often all they read: one
line with the action, the exact target, the scope (how many, from what a
check counted), whether it can be undone, the date and the reason. "delete
the 3 @test.example rows from prod users (snapshot keeps them) 2026-10-29:
load-test cleanup", not "cleanup". Can't say it in a line? Split the guard.
swim strips escape sequences, control characters and bidi overrides from
it.

## How a guard is decided

In this order:

1. **The flag:** `FLAG=1` in the environment `swim run` started with
   approves; `FLAG=0` declines without asking. A flag a lane sets for
   itself doesn't count. Env files can't hold one.
2. **Signed approvals** of this exact [manifest](manifest.md), plus standing
   approvals of whoever queued the batch, counted against `lookup.yml`'s
   rule for the guard.
3. **The person at the terminal:** `swim 2 requires human approval of guard
   FLAG ... the lane says: <action>  Y/N [N]`. Or in the live view: `y`/`n`
   on a lane that's asking, or ahead of time on one that hasn't (this run
   only).
4. **`-y`** (`swim all -y`, `swim watch -y`): approves every question; the
   log, `swim status`, the chain and `swim ci`'s summary say so.

Anything else is a **dry run**: the step is skipped, the log says how to
approve, and the round ends **SKIP**, not PASS: lanes after it don't start,
`swim run` exits 3 (`swim ci`: 0), `swim all` runs it again next time.

Under `swim run` the launcher decides, not the lane: it reads `approvals/`
and `lookup.yml` once as the run starts, refuses a flag the header doesn't
declare, lets each approval be used once, and writes every approval to the
chain itself. A lane can't approve itself. `approvals: flags` in config
never asks.

## Signed approvals

```sh
swim approve 4 --guards FIN_ALLOW_DROP     # this exact manifest; co-approvers run the same
swim approve --auto bob [--guards A,B]     # standing: every job bob queues
swim revoke 4 / swim revoke --auto bob
swim approve                               # every approval, its state and signers
swim watch --approved                      # run scripts once they're fully approved
```

Approvals are committed files under `approvals/`, each an ordered,
signed log of approve and revoke events, so approvers sign from their own
machines. `lookup.yml` (committed) says whose key is whose and what each
guard needs:

```yaml
users:
  zach:  {keys: ["ssh-ed25519 AAAA... zach@laptop"]}
  alice: {keys: ["ssh-ed25519 AAAA... alice@work"]}
groups:
  platform-admin: [zach, alice]
require:
  - guards: [FIN_ALLOW_*]
    group: platform-admin
    count: 2              # two different people
standing:
  max: 90d                # the longest a standing approval may last
```

Script and standing approvals count together. `swim keys` shows who's who.

## Lifecycle

`swim approve` and `swim verify` show each approval's state:

| | |
|---|---|
| **active** | It counts. |
| **expired** | Past `--expires`. Standing approvals last 30 days by default, never past `standing.max`. |
| **revoked** | Every signer's latest event revokes. |
| **stale** | What it approves changed. A script approval binds its manifest (script, pinned files, `swim.yml` policy, `lookup.yml`'s rules); a standing approval binds each guard's rule when signed. Stricter or looser, a rule change needs a new signature. `swim approve` says why: `stale: includes lib/orders.sh changed`. |
| **insufficient** | Its signers don't meet the rule, or aren't in `lookup.yml`. |

Who is who is read live: a key removed from `lookup.yml` stops counting at
once.

## Decisions

A guard asks "may I?"; a decision asks "which way?".

```bash
# Decisions: ROLLOUT  how api 2.4.1 goes out
decide ROLLOUT "How should api 2.4.1 roll out?" \
  "canary: 10% for 15 minutes, then the rest" "all at once" "don't deploy"
case $DECISION in
  1) run "canary" deploy api 2.4.1 --percent 10 ;;
  2) run "deploy" deploy api 2.4.1 --percent 100 ;;
  3) stop "not deploying" ;;
esac
```

- 2 to 9 choices. Answered by `ROLLOUT=2` in `swim run`'s environment, a
  batch's `--decide ROLLOUT=2`, the prompt, or a number key in the live
  view. `-y` answers none, and there is no default.
- Unanswered (no one to ask, a value that isn't a choice, or set by the
  lane itself): `SKIP ... decision needed`, and the round ends there as a
  dry run.
- Undeclared in `# Decisions:`: refused. Call it on its own line, not in
  `$(...)` or a pipe.
- Recorded: `DECIDED ROLLOUT: 2. all at once` in the log, a `decide` entry
  in the chain.
- A decision steers; it never approves. Guard the destructive branch.
