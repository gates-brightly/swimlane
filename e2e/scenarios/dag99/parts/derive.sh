# Run id comes from the parents, not run.id: a node whose ancestry never reaches
# swim 1 can start before swim 1 writes this run's run.id.
RUN_ID=$(e2etool run-id $PARENTS)
