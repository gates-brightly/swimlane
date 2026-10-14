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

Secrets stored with `swim secret set` come by name:
`import { secret } from "@gates-brightly/swim"; const password = await secret.get("db_password");`.
swim masks every secret as `***(name)` in what it writes.

A lane split into jobs marks them in comments (`// swim job 1.1`, each with
its own `// Round:` and other header lines); each job runs the program
once, and `jobs()` calls the function for it with the round. Keys are job
ids (`"1.2"`) or places under the lane (`"2"`); the closest one wins.
`r.job` is the job's id.

```ts
import { jobs } from "@gates-brightly/swim";
await jobs({ "1.1": fmt, "1.2": lint });
```
