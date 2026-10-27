package launcher

// ExitDryRun is swim run's exit code when no lane failed, but a guarded
// step dry-ran (or a lane waited on one that did): the work is waiting on
// an approval. Like terraform plan -detailed-exitcode's 2, it is neither
// a pass nor a failure; swim ci counts it as a pass, since CI can't
// approve.
const ExitDryRun = 3
