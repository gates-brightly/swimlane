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

The runner has no config of its own, so `swim ci` runs on swim's
defaults ([config](config.md)): a lane needs a `#!` line, not a
`runners:` entry. Commit the lane scripts the job runs (`!/lane.1.sh` in `.gitignore`). `--changed[=BASE]`
runs only rounds whose script changed, plus unpassed lanes they wait on;
`--rerun` runs passed ones too. This repo's own CI is `swim ci` over its `lane.*.sh`.
