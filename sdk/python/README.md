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
