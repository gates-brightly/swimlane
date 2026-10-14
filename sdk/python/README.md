# swim-lanes

Native steps for swim lane scripts in Python. Install with
`pip install swim-lanes` and import it as `swim`. It has no dependencies.

```python
import swim

with swim.round() as r:
    r.stage("check")
    r.gate("tf plan", ["terraform", "plan", "-detailed-exitcode"])
    with r.step("orders table is empty", gate=True) as s:  # in-process
        s.log("items: 0")
        s.require(True, "table still has items")

    @r.on_failure
    def rollback(outcome):
        r.run("sst deploy", ["npx", "sst", "deploy"])
```

The SDK talks to swim over the lane's socket (protocol `swim.lane/v1`, see
`proto/swim/lane/v1/README.md`), and swim decides everything. Run the lane
with `swim run N`.

Secrets stored with `swim secret set` come by name, inside or outside a
round: `password = swim.secret.get("db_password")`. swim masks every
secret as `***(name)` in what it writes.

A lane split into jobs marks them in comments (`# swim job 1.1`, each with
its own `# Round:` and other header lines); each job runs the program once,
and `swim.jobs()` calls the function for it with the round. Keys are job
ids (`"1.2"`) or places under the lane (`"2"`); the closest one wins, so
`"1.2"` handles 1.2.1 when nothing names it. `r.job` is the job's id.

```python
swim.jobs({"1.1": fmt, "1.2": lint})
```
