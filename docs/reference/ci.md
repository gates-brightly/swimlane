# CI

`swim ci` is `swim all` for a CI job: it never prompts, folds each lane's
log, turns FAIL and unprompted approvals into annotations, writes a
job summary, and with `--junit FILE` a JUnit report. It detects GitHub
Actions and GitLab CI. A dry-run round counts as a pass (CI can't approve).
swim never commits results back.

```yaml
# .github/workflows/swim.yml
jobs:
  swim:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }          # --changed needs the history
      - run: curl -fsSL https://github.com/gates-brightly/swimlane/releases/latest/download/install.sh | sh
      - run: swim ci --changed --junit swim.xml
        env:
          FIN_ALLOW_DELETE: ${{ secrets.FIN_ALLOW_DELETE }}   # guard flags come from the job
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: swim
          include-hidden-files: true
          path: ".swim/logs\n.swim.log\nswim.xml"
```

Commit `swim.yml` (the runner has no config of its own) and the lane
scripts the job runs (`!/lane.1.sh` in `.gitignore`). `--changed[=BASE]`
runs only rounds whose script changed, plus unpassed lanes they wait on;
`--rerun` runs passed ones too. This repo's own CI is `swim ci` over its `lane.*.sh`.

## Local CI

A lane with `# Signoff: swim/checks` can run on your machine instead, and
swim posts the result where CI's would go:

```sh
git push                  # GitHub only takes a status for a commit it has
swim run checks           # or queue it: swim watch signs off the same way
swim signoff verify .swim/signoffs/<commit>/1.json
```

When the round passes on a clean, committed HEAD, swim signs a statement
(the commit and tree, lane, script and manifest hashes, job, run) with the
key of whoever ran `swim run`, `swim watch` or a local `swim ci`, writes it
to `.swim/signoffs/COMMIT/N.json`, and posts a `success` commit status named
by the header through their `gh`. Require that status in branch protection.
The lane needs no key or `gh` login; approving the script approves the
signoff. `swim ci` in a CI job doesn't sign off. `swim signoff verify`
checks a statement's signature, that this machine's signing key made it,
and that its commit still has the tree it names.

A signoff is an attestation, not a proof: it shows whose key says the lane
passed on that tree, not that it ran honestly. Keep CI running on main to
catch the difference.
