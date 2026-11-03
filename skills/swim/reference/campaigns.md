# Campaigns: rounds that build on each other

Most real work is several rounds: discover, prepare, change, clean up,
often with an expensive setup and a few retries. Each round should start
from what the previous ones left behind.

- **Read the history before you write the next round:**
  - `swim log N --all`: the lane's rounds, archives included.
  - `swim log <job>`: one round by its job id.
  - `swim history N`: who approved and ran what.
  - `swim log`: the project log, including everyone's notes.
- **Name archives so they can be found:** `swim archive N --as
  orders-r3-apply-failed`, not the default job id.
- **Keep a campaign thread in the record:** one `swim note` per decision
  or finding, with a fixed prefix (`swim note "orders cutover: r3 apply
  failed on the IAM role; r4 retries it alone"`). A fresh session finds
  the thread in `swim log`. A plan that should outlive the campaign
  belongs in the project's own docs or tracker. Ask the user where before
  creating a file.
- **Don't redo expensive setup:** put it in its own lane and depend on its
  exact job (`# After: <job>`), and pass results with `# Outputs:` /
  `input`.
- **Each new round re-reads deployed state.** Don't trust the previous
  round's output as current; snapshot again.
