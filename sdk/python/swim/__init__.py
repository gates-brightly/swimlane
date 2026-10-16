"""swim: native steps for lane scripts in Python (swim.lane/v1).

    import swim

    with swim.round() as r:
        r.stage("check")
        r.gate("tf plan", ["terraform", "plan", "-detailed-exitcode"])
        with r.step("orders table is empty", gate=True) as s:   # runs in-process
            n = count_items()
            s.log(f"items: {n}")
            s.require(n == 0, "table still has items")
        r.stage("change")
        if r.guard("ORDERS_ALLOW_DROP", "drop orders table (tf owns it now)"):
            r.run("drop table", ["aws", "dynamodb", "delete-table", "--table-name", "orders"])

A lane split into jobs ("# swim job 1.1" comments, each with its own header)
runs once per job; jobs() calls that job's function:

    swim.jobs({
        "1.1": lambda r: r.gate("fmt", ["gofmt", "-l", "."]),
        "1.2": lint,
    })

swim decides everything (logging, masking, approvals, timeouts, the chain);
this module only talks to it over the lane's socket. Run the script with
`swim run N`: outside a round there is nothing to talk to.
"""

import json
import os
import socket
import subprocess

__all__ = ["round", "jobs", "Round", "Step", "RoundEnded", "SwimError", "PROTOCOL", "secret"]

PROTOCOL = "swim.lane/v1"
__version__ = "1.0.0"


class SwimError(Exception):
    """swim refused a call (a usage error), or isn't there."""


class RoundEnded(BaseException):
    """The round is over: a failed gate, stop, a refused step, an operator
    stop or a time limit. A BaseException, so `except Exception` doesn't
    swallow it: let it unwind the program."""

    def __init__(self, code, reason=""):
        super().__init__(f"swim: the round ended (exit {code}){': ' + reason if reason else ''}")
        self.code = code
        self.reason = reason


class _StepFailed(Exception):
    pass


class Step:
    """An in-process step: the program runs it, swim logs it."""

    def __init__(self, r, label, gate):
        self._r, self.label, self.gate = r, label, gate
        self._id = None
        self._message = ""

    def log(self, text):
        """Add a line to the step's output (masked by swim)."""
        self._r._call("step_output", step=self._id, text=str(text))

    def require(self, ok, message="requirement not met"):
        """Fail the step unless ok."""
        if not ok:
            self._message = message
            raise _StepFailed(message)

    def __enter__(self):
        res = self._r._call("begin_step", label=self.label, gate=self.gate)
        if not res.get("started", True):
            raise RoundEnded(130, "the lane is stopping")
        self._id = res["step"]
        return self

    def __exit__(self, kind, exc, tb):
        if kind is not None and issubclass(kind, BaseException) and not issubclass(kind, Exception):
            return False  # RoundEnded, KeyboardInterrupt: not this step's result
        ok = kind is None
        message = self._message or ("" if ok else f"{kind.__name__}: {exc}")
        self.passed = ok
        self._r._call("end_step", step=self._id, ok=ok, message=message)
        # A failed require is the step's result; any other exception is
        # recorded as its failure and goes on up.
        return kind is not None and issubclass(kind, _StepFailed)


class Round:
    """The round swim started for this lane."""

    def __init__(self):
        self._sock_path = os.environ.get("SWIM_SOCKET")
        self._token = os.environ.get("SWIM_TOKEN", "")
        if not self._sock_path:
            raise SwimError("not inside a swim round (SWIM_SOCKET is unset): run this lane with `swim run N`")
        self._sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self._sock.connect(self._sock_path)
        self._file = self._sock.makefile("rwb")
        self._id = 0
        self._handlers = {}
        self.info = self._call("hello", protocol=PROTOCOL, sdk="python " + __version__)

    @property
    def job(self):
        """This round's job in a lane of jobs ("1.5.9"), else None."""
        return self.info.get("job_path") or None

    def _call(self, op, **args):
        self._id += 1
        line = json.dumps({"id": self._id, "token": self._token, "op": op, "args": args}) + "\n"
        self._file.write(line.encode())
        self._file.flush()
        reply = self._file.readline()
        if not reply:
            raise SwimError("swim closed the lane's socket")
        resp = json.loads(reply)
        if not resp.get("ok"):
            raise SwimError(resp.get("error") or f"swim refused {op}")
        if resp.get("round_ended") and op not in ("outcome", "handler"):
            raise RoundEnded(resp.get("code", 1), resp.get("reason", ""))
        return resp.get("result") or {}

    # Steps run by swim.
    def _step(self, op, label, argv, timeout=None, retry=None, backoff=None, retry_on=None, soft=False,
              expect_fail=None, key=None, target=None):
        opts = []
        if timeout is not None:
            opts += ["--timeout", str(timeout)]
        if retry is not None:
            opts += ["--retry", str(retry)]
        if backoff is not None:
            opts += ["--backoff", str(backoff)]
        if retry_on is not None:
            opts += ["--retry-on", ",".join(str(c) for c in retry_on)]
        if soft:
            opts.append("--soft")
        # expect_fail=True: the step should fail (any non-zero exit);
        # expect_fail=[1, 2]: with one of these codes.
        # key and target are for swim observe: the step's name across
        # rounds, and what it talks to.
        if key:
            opts += ["--key", str(key)]
        if target:
            opts += ["--target", str(target)]
        if expect_fail is True:
            opts.append("--expect-fail")
        elif expect_fail:
            opts.append("--expect-fail=" + ",".join(str(c) for c in expect_fail))
        return self._call(op, label=label, argv=[str(a) for a in argv], opts=opts).get("passed", False)

    def run(self, label, argv, **opts):
        """A step: True if it passed. A failure doesn't stop the round."""
        return self._step("run", label, argv, **opts)

    def gate(self, label, argv, **opts):
        """A step whose failure ends the round (raises RoundEnded)."""
        return self._step("gate", label, argv, **opts)

    def snapshot(self, label, argv, **opts):
        """A read-only step whose output is saved under .swim/snapshots/."""
        return self._step("snapshot", label, argv, **opts)

    def evidence(self, label, argv, **opts):
        """A check step whose output an approval can cover (the plan)."""
        return self._step("evidence", label, argv, **opts)

    def module(self, name, *args, env=False, label=None, gate=False, evidence=False, **opts):
        """A command of a module the lane declares (# Modules:), as a step:
        True if it passed. env=True takes the variables it gives the lane
        (credentials: aws-session) into this program's environment and every
        later step's, masked. Options as for run, gate=True, and
        evidence=True (its output is the round's evidence: tf plan)."""
        o = []
        if opts.get("timeout") is not None:
            o += ["--timeout", str(opts["timeout"])]
        if opts.get("retry") is not None:
            o += ["--retry", str(opts["retry"])]
        if opts.get("backoff") is not None:
            o += ["--backoff", str(opts["backoff"])]
        if opts.get("retry_on") is not None:
            o += ["--retry-on", ",".join(str(c) for c in opts["retry_on"])]
        if opts.get("soft"):
            o.append("--soft")
        res = self._call("module", name=name, args=[str(a) for a in args], opts=o,
                         label=label or "", gate=bool(gate), evidence=bool(evidence), env=bool(env))
        for k, v in (res.get("env") or {}).items():
            os.environ[k] = v
        return res.get("passed", False)

    def module_env(self, name, *args, **kw):
        """module(name, *args, env=True)."""
        return self.module(name, *args, env=True, **kw)

    def step(self, label, gate=False):
        """An in-process step, as a context manager (see Step)."""
        return Step(self, label, gate)

    def stage(self, name):
        self._call("stage", name=name)

    def guard(self, flag, action):
        """True if the operator approves the action; False is a dry run."""
        return self._call("guard", flag=flag, action=action).get("approved", False)

    def confirm(self, question):
        """True if the operator confirms; no ends the round."""
        return self._call("confirm", question=question).get("confirmed", False)

    def drift(self, what):
        self._call("drift", what=what)

    def stop(self, reason="stopped"):
        """Record STOP and end the round (raises RoundEnded)."""
        self._call("stop", reason=reason)

    def msg(self, text, progress=None):
        """The lane's status message; progress is (n, m)."""
        p = f"{progress[0]}/{progress[1]}" if progress else ""
        self._call("msg", text=text, progress=p)

    def input(self, ref, name):
        """The path (or small value) of a dependency's output."""
        return self._call("input", ref=str(ref), name=name).get("value", "")

    def output(self, key, value):
        self._call("output", key=key, value=str(value))

    # Outcome handlers, as decorators.
    def on_success(self, fn):
        self._handlers["on_success"] = fn
        return fn

    def on_failure(self, fn):
        self._handlers["on_failure"] = fn
        return fn

    def on_exit(self, fn):
        self._handlers["on_exit"] = fn
        return fn

    def _run_handlers(self):
        if not self._handlers:
            return
        out = self._call("outcome")
        first = "on_success" if out.get("outcome") == "pass" else "on_failure"
        for name in (first, "on_exit"):
            fn = self._handlers.get(name)
            if fn is None:
                continue
            self._call("handler", name=name)
            try:
                fn(out)
            except Exception as e:  # handlers run soft
                try:
                    self._call("drift", what=f"{name} raised {type(e).__name__}: {e}")
                except SwimError:
                    pass
            finally:
                self._call("handler", name="")

    def close(self):
        try:
            self._file.close()
        finally:
            self._sock.close()

    def __enter__(self):
        return self

    def __exit__(self, kind, exc, tb):
        try:
            self._run_handlers()
        finally:
            self.close()
        if kind is not None and issubclass(kind, RoundEnded):
            raise SystemExit(exc.code)
        return False


def round():
    """The lane's round, as a context manager."""
    return Round()


def jobs(handlers):
    """Run this lane's job: the function handlers has for it, called with
    the round. Keys are job ids ("1.2") or their place under the lane
    ("2"); the closest one wins, so "1.5" handles 1.5.9 when nothing names
    1.5.9 itself. A job with no function ends the round as failed."""
    with round() as r:
        if r.job is None:
            raise SwimError("this lane has no jobs (no \"# swim job N.K\" lines): use swim.round()")
        fn = _job_handler(handlers, r.job)
        if fn is None:
            r.stop(f"no function for job {r.job} (jobs() has {', '.join(sorted(handlers)) or 'none'})")
            return
        fn(r)


def _job_handler(handlers, job):
    lane, _, path = job.partition(".")
    parts = path.split(".")
    for k in range(len(parts), 0, -1):
        p = ".".join(parts[:k])
        for key in (f"{lane}.{p}", p):
            if key in handlers:
                return handlers[key]
    return None


class _Secrets:
    """Secrets stored with `swim secret set`, by name:

        password = swim.secret.get("db_password")

    Inside a swim run it asks the run's secrets broker (the run decrypted
    them at start, and masks every one as ***(name) in what swim writes);
    outside one it runs `swim secret get`, which decrypts with your key.
    """

    def get(self, name):
        path = os.environ.get("SWIM_SECRETS")
        if not path:
            bin_ = os.environ.get("SWIM_BIN") or "swim"
            r = subprocess.run([bin_, "secret", "get", name], capture_output=True, text=True)
            if r.returncode != 0:
                raise SwimError(r.stderr.strip() or f"swim secret get {name} failed")
            return r.stdout
        req = {
            "token": os.environ.get("SWIM_SECRETS_TOKEN", ""),
            # which lane process asks: a guarded secret goes only to a lane
            # whose guard was approved
            "lane_token": os.environ.get("SWIM_APPROVAL_TOKEN", ""),
            "op": "get",
            "name": name,
            "how": "sdk",
        }
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            s.connect(path)
            f = s.makefile("rwb")
            f.write((json.dumps(req) + "\n").encode())
            f.flush()
            reply = f.readline()
        finally:
            s.close()
        if not reply:
            raise SwimError("the run's secrets broker closed the connection")
        resp = json.loads(reply)
        if not resp.get("ok"):
            raise SwimError(f"secret {name}: {resp.get('error') or 'refused'}")
        return resp.get("value", "")


secret = _Secrets()
