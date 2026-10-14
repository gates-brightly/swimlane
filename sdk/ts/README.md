# @gates-brightly/swim

Native steps for swim lane scripts in TypeScript and JavaScript (Node, Bun
and Deno). It has no dependencies, and its types are erasable, so
`node --experimental-strip-types` (or tsx, bun, deno) runs it as is.

```ts
import { round } from "@gates-brightly/swim";

await round(async (r) => {
  r.onFailure(async () => { await r.run("rollback", ["./rollback.sh"]); });
  await r.stage("check");
  await r.gate("tf plan", ["terraform", "plan", "-detailed-exitcode"]);
  await r.step("orders table is empty", async (s) => {
    s.log("items: 0");
  }, { gate: true });
});
```

The SDK talks to swim over the lane's socket (`swim.lane/v1`, see
`proto/swim/lane/v1/README.md`). Run the lane with `swim run N`.
