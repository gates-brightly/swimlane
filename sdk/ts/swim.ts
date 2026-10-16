// swim: native steps for lane scripts in TypeScript and JavaScript (Node,
// Bun, Deno), over the swim.lane/v1 protocol.
//
//   import { round } from "@gates-brightly/swim";
//
//   await round(async (r) => {
//     await r.stage("check");
//     await r.gate("tf plan", ["terraform", "plan", "-detailed-exitcode"]);
//     await r.step("orders table is empty", async (s) => {   // in-process
//       s.log(`items: ${n}`);
//       s.require(n === 0, "table still has items");
//     }, { gate: true });
//     await r.stage("change");
//     if (await r.guard("ORDERS_ALLOW_DROP", "drop orders table")) {
//       await r.run("drop table", ["aws", "dynamodb", "delete-table", "--table-name", "orders"]);
//     }
//   });
//
// A lane split into jobs ("// swim job 1.1" comments, each with its own
// header) runs once per job; jobs() calls that job's function:
//
//   await jobs({ "1.1": async (r) => { await r.gate("fmt", ["npm", "run", "fmt:check"]); }, "1.2": lint });
//
// swim decides everything; this module only talks to it over the lane's
// socket. Run the lane with `swim run N`. Written with erasable types only,
// so `node --experimental-strip-types` runs it as is.

import { execFile } from "node:child_process";
import { createConnection, type Socket } from "node:net";
import process from "node:process";

export const PROTOCOL = "swim.lane/v1";
export const VERSION = "1.0.0";

/** swim refused a call (a usage error), or isn't there. */
export class SwimError extends Error {}

/** The round is over: let it unwind the program (round() exits with code). */
export class RoundEnded extends Error {
  code: number;
  constructor(code: number, reason = "") {
    super(`swim: the round ended (exit ${code})${reason ? ": " + reason : ""}`);
    this.code = code;
  }
}

class StepFailed extends Error {}

export interface StepOptions {
  timeout?: string;
  retry?: number;
  backoff?: string;
  retryOn?: number[];
  soft?: boolean;
}

/** A module call's options: a step's, plus its label, gate and env. */
export interface ModuleOptions extends StepOptions {
  label?: string;
  gate?: boolean;
  env?: boolean;
}

type Result = Record<string, unknown>;
type Handler = (outcome: Result) => Promise<void> | void;

/** An in-process step: the program runs it, swim logs it. */
export class Step {
  private lines: Promise<unknown>[] = [];
  private r: Round;
  readonly id: number;
  message = "";
  constructor(r: Round, id: number) {
    this.r = r;
    this.id = id;
  }
  /** Add a line to the step's output (masked by swim). */
  log(text: string): void {
    this.lines.push(this.r.call("step_output", { step: this.id, text: String(text) }));
  }
  /** Fail the step unless ok. */
  require(ok: boolean, message = "requirement not met"): void {
    if (!ok) {
      this.message = message;
      throw new StepFailed(message);
    }
  }
  async flush(): Promise<void> {
    await Promise.all(this.lines);
  }
}

/** The round swim started for this lane. */
export class Round {
  private sock: Socket;
  private buf = "";
  private nextId = 0;
  private waiting = new Map<number, (resp: Result) => void>();
  private queue: Promise<unknown> = Promise.resolve();
  private handlers = new Map<string, Handler>();
  info: Result = {};

  private constructor(sock: Socket) {
    this.sock = sock;
    sock.setEncoding("utf8");
    sock.on("data", (chunk: string) => {
      this.buf += chunk;
      let i;
      while ((i = this.buf.indexOf("\n")) >= 0) {
        const line = this.buf.slice(0, i);
        this.buf = this.buf.slice(i + 1);
        const resp = JSON.parse(line) as Result;
        const done = this.waiting.get(resp.id as number);
        if (done) {
          this.waiting.delete(resp.id as number);
          done(resp);
        }
      }
    });
  }

  static async open(): Promise<Round> {
    const path = process.env.SWIM_SOCKET;
    if (!path) {
      throw new SwimError("not inside a swim round (SWIM_SOCKET is unset): run this lane with `swim run N`");
    }
    const sock = await new Promise<Socket>((resolve, reject) => {
      const s = createConnection(path, () => resolve(s));
      s.on("error", reject);
    });
    const r = new Round(sock);
    r.info = await r.call("hello", { protocol: PROTOCOL, sdk: "ts " + VERSION });
    return r;
  }

  /** One call; calls are sent one at a time, in order. */
  call(op: string, args: Result = {}): Promise<Result> {
    const run = async (): Promise<Result> => {
      const id = ++this.nextId;
      const resp = await new Promise<Result>((resolve) => {
        this.waiting.set(id, resolve);
        this.sock.write(JSON.stringify({ id, token: process.env.SWIM_TOKEN ?? "", op, args }) + "\n");
      });
      if (!resp.ok) throw new SwimError((resp.error as string) || `swim refused ${op}`);
      if (resp.round_ended && op !== "outcome" && op !== "handler") {
        throw new RoundEnded((resp.code as number) ?? 1, (resp.reason as string) ?? "");
      }
      return (resp.result as Result) ?? {};
    };
    const p = this.queue.then(run, run);
    this.queue = p.catch(() => undefined);
    return p;
  }

  private async swimStep(op: string, label: string, argv: string[], o: StepOptions = {}): Promise<boolean> {
    const opts: string[] = [];
    if (o.timeout) opts.push("--timeout", o.timeout);
    if (o.retry !== undefined) opts.push("--retry", String(o.retry));
    if (o.backoff) opts.push("--backoff", o.backoff);
    if (o.retryOn) opts.push("--retry-on", o.retryOn.join(","));
    if (o.soft) opts.push("--soft");
    const res = await this.call(op, { label, argv: argv.map(String), opts });
    return Boolean(res.passed);
  }

  /** A step: true if it passed. A failure doesn't stop the round. */
  run(label: string, argv: string[], o?: StepOptions) { return this.swimStep("run", label, argv, o); }
  /** A step whose failure ends the round (throws RoundEnded). */
  gate(label: string, argv: string[], o?: StepOptions) { return this.swimStep("gate", label, argv, o); }
  /** A read-only step whose output is saved under .swim/snapshots/. */
  snapshot(label: string, argv: string[], o?: StepOptions) { return this.swimStep("snapshot", label, argv, o); }
  /** A check step whose output an approval can cover (the plan). */
  evidence(label: string, argv: string[], o?: StepOptions) { return this.swimStep("evidence", label, argv, o); }

  /** A command of a module the lane declares (# Modules:), as a step: true if it passed. */
  module(name: string, ...args: string[]) { return this.moduleWith({}, name, ...args); }
  /** module, taking the variables it gives the lane (credentials) into process.env and every later step's, masked. */
  moduleEnv(name: string, ...args: string[]) { return this.moduleWith({ env: true }, name, ...args); }
  /** module with options: run's, plus label, gate and env. */
  async moduleWith(o: ModuleOptions, name: string, ...args: string[]): Promise<boolean> {
    const opts: string[] = [];
    if (o.timeout) opts.push("--timeout", o.timeout);
    if (o.retry !== undefined) opts.push("--retry", String(o.retry));
    if (o.backoff) opts.push("--backoff", o.backoff);
    if (o.retryOn) opts.push("--retry-on", o.retryOn.join(","));
    if (o.soft) opts.push("--soft");
    const res = await this.call("module", { name, args: args.map(String), opts, label: o.label ?? "", gate: Boolean(o.gate), env: Boolean(o.env) });
    for (const [k, v] of Object.entries((res.env as Record<string, string>) ?? {})) process.env[k] = v;
    return Boolean(res.passed);
  }

  /** An in-process step: fn runs here, swim logs it. */
  async step(label: string, fn: (s: Step) => Promise<void> | void, o: { gate?: boolean } = {}): Promise<boolean> {
    const res = await this.call("begin_step", { label, gate: Boolean(o.gate) });
    if (res.started === false) throw new RoundEnded(130, "the lane is stopping");
    const s = new Step(this, res.step as number);
    let ok = true;
    let thrown: unknown;
    try {
      await fn(s);
    } catch (e) {
      ok = false;
      if (e instanceof RoundEnded) throw e;
      if (!(e instanceof StepFailed)) thrown = e;
      if (!s.message) s.message = e instanceof Error ? `${e.name}: ${e.message}` : String(e);
    }
    await s.flush();
    await this.call("end_step", { step: s.id, ok, message: s.message });
    if (thrown !== undefined) throw thrown;
    return ok;
  }

  async stage(name: string) { await this.call("stage", { name }); }
  /** True if the operator approves; false is a dry run. */
  async guard(flag: string, action: string) { return Boolean((await this.call("guard", { flag, action })).approved); }
  /** True if the operator confirms; no ends the round. */
  async confirm(question: string) { return Boolean((await this.call("confirm", { question })).confirmed); }
  async drift(what: string) { await this.call("drift", { what }); }
  /** Record STOP and end the round (throws RoundEnded). */
  async stop(reason = "stopped") { await this.call("stop", { reason }); }

  /** This round's job in a lane of jobs ("1.5.9"), else undefined. */
  get job(): string | undefined { return (this.info.job_path as string) || undefined; }
  async msg(text: string, progress?: [number, number]) {
    await this.call("msg", { text, progress: progress ? `${progress[0]}/${progress[1]}` : "" });
  }
  /** The path (or small value) of a dependency's output. */
  async input(ref: string | number, name: string) { return String((await this.call("input", { ref: String(ref), name })).value ?? ""); }
  async output(key: string, value: string) { await this.call("output", { key, value: String(value) }); }

  onSuccess(fn: Handler) { this.handlers.set("on_success", fn); }
  onFailure(fn: Handler) { this.handlers.set("on_failure", fn); }
  onExit(fn: Handler) { this.handlers.set("on_exit", fn); }

  async runHandlers(): Promise<void> {
    if (this.handlers.size === 0) return;
    const out = await this.call("outcome");
    const first = out.outcome === "pass" ? "on_success" : "on_failure";
    for (const name of [first, "on_exit"]) {
      const fn = this.handlers.get(name);
      if (!fn) continue;
      await this.call("handler", { name });
      try {
        await fn(out);
      } catch (e) {
        await this.call("drift", { what: `${name} threw ${e instanceof Error ? e.message : String(e)}` }).catch(() => undefined);
      } finally {
        await this.call("handler", { name: "" });
      }
    }
  }

  close() { this.sock.end(); }
}

/** Run fn as the lane's round; exits with the round's code if it ended. */
export async function round(fn: (r: Round) => Promise<void> | void): Promise<void> {
  const r = await Round.open();
  let ended: RoundEnded | undefined;
  try {
    await fn(r);
  } catch (e) {
    if (e instanceof RoundEnded) ended = e;
    else throw e;
  } finally {
    try {
      await r.runHandlers();
    } finally {
      r.close();
    }
  }
  if (ended) process.exit(ended.code);
}

/**
 * Run this lane's job: the function handlers has for it, called with the
 * round. Keys are job ids ("1.2") or their place under the lane ("2"); the
 * closest one wins, so "1.5" handles 1.5.9 when nothing names 1.5.9 itself.
 * A job with no function ends the round as failed.
 */
export async function jobs(handlers: Record<string, (r: Round) => Promise<void> | void>): Promise<void> {
  await round(async (r) => {
    const job = r.job;
    if (!job) throw new SwimError('this lane has no jobs (no "// swim job N.K" lines): use round()');
    const fn = jobHandler(handlers, job);
    if (!fn) {
      await r.stop(`no function for job ${job} (jobs() has ${Object.keys(handlers).sort().join(", ") || "none"})`);
      return;
    }
    await fn(r);
  });
}

function jobHandler<T>(handlers: Record<string, T>, job: string): T | undefined {
  const dot = job.indexOf(".");
  const lane = job.slice(0, dot), parts = job.slice(dot + 1).split(".");
  for (let k = parts.length; k > 0; k--) {
    const p = parts.slice(0, k).join(".");
    for (const key of [`${lane}.${p}`, p]) {
      if (key in handlers) return handlers[key];
    }
  }
  return undefined;
}

/**
 * Secrets stored with `swim secret set`, by name:
 *
 *   const password = await secret.get("db_password");
 *
 * Inside a swim run it asks the run's secrets broker (the run decrypted
 * them at start, and masks every one as ***(name) in what swim writes);
 * outside one it runs `swim secret get`, which decrypts with your key.
 */
export const secret = {
  get(name: string): Promise<string> {
    const path = process.env.SWIM_SECRETS;
    if (!path) {
      return new Promise((resolve, reject) => {
        execFile(process.env.SWIM_BIN || "swim", ["secret", "get", name], (err, stdout, stderr) => {
          if (err) reject(new SwimError(String(stderr).trim() || `swim secret get ${name} failed`));
          else resolve(String(stdout));
        });
      });
    }
    const req = {
      token: process.env.SWIM_SECRETS_TOKEN ?? "",
      // which lane process asks: a guarded secret goes only to a lane whose
      // guard was approved
      lane_token: process.env.SWIM_APPROVAL_TOKEN ?? "",
      op: "get",
      name,
      how: "sdk",
    };
    return new Promise((resolve, reject) => {
      let buf = "";
      const sock = createConnection(path, () => sock.write(JSON.stringify(req) + "\n"));
      sock.setEncoding("utf8");
      sock.on("data", (chunk: string) => {
        buf += chunk;
        const i = buf.indexOf("\n");
        if (i < 0) return;
        sock.end();
        const resp = JSON.parse(buf.slice(0, i)) as Result;
        if (resp.ok) resolve(String(resp.value ?? ""));
        else reject(new SwimError(`secret ${name}: ${(resp.error as string) || "refused"}`));
      });
      sock.on("error", (e) => reject(new SwimError(`secret ${name}: ${e.message}`)));
    });
  },
};
