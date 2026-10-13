#!/usr/bin/env node
// swim: syntax 2
// Round:   {{.Goal}}
// Job:     {{.Job}}
// After:
// Owner:   {{.Owner}}
// Created: {{.Date}}
// Guards:
// Timeout:
//
// Header keys are those of a bash lane (swim help new); they can live in a
// sidecar lane.{{.Lane}}.yml instead (round:, job:, after:, guards:, ...).
//
// Goal:
//   {{.Goal}}
//
// swim runs this file inside its wrapper. Each step is a swim command:
//   swim run "<label>" -- cmd ...   a step (PASS/FAIL, output in the log)
//   swim gate "<label>" -- cmd ...  a step whose failure ends the round
//   swim guard FLAG "<action>"      exit 0 if the operator approves
//   swim stage NAME                 snapshot, check, change, verify
//   swim drift "<what>" / swim stop "<why>"
// A failed gate or swim stop ends this program even if it carries on.
// A program that never calls swim run is logged as one step.
//
// Run:   swim run {{.Lane}}    (or pinned to this job: swim run {{.Job}})
// $SWIM_JOB holds the job id while the round runs; tag resources with it.
{{.Import}}

// swim runs a swim in-lane command and returns its exit code.
const swim = (...args{{.ArgsType}}){{.RetType}} =>
  spawnSync(process.env.SWIM_BIN || "swim", args, { stdio: "inherit" }).status ?? 1;

swim("stage", "snapshot");
swim("snapshot", "current state", "--", "echo", "replace with a read-only describe/list command");

swim("stage", "check");
swim("run", "precheck", "--", "true");
swim("gate", "target is ours (tagged)", "--", "true");

swim("stage", "change");
// One guard flag per destructive action; list it under Guards: above.
// if (swim("guard", "EXAMPLE_ALLOW_DELETE", "delete X ({{.Date}}: why it is safe)") === 0) {
//   swim("run", "delete X", "--", "echo", "would delete X");
// }

swim("stage", "verify");
swim("run", "verify", "--", "true");
