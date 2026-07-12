# Cutover checklist: laptop indexer -> ECS

Moving the indexer's write path off a laptop/Mac onto ECS. Neo4j is the shared
state; only one indexer may ever point at it at a time (see warning below).

## Preconditions

- [ ] GitHub App or PAT stored in AWS Secrets Manager (maps to `GIT_TOKEN`)
- [ ] Neo4j auth password stored in Secrets Manager (maps to `NEO4J_PASSWORD`)
- [ ] ECR repos created for `query-service`, `indexer`, `web`; images built and pushed
      ```
      aws ecr create-repository --repository-name graph-platform/query-service
      aws ecr create-repository --repository-name graph-platform/indexer
      aws ecr create-repository --repository-name graph-platform/web
      docker build -f deploy/Dockerfile.query-service -t <acct>.dkr.ecr.<region>.amazonaws.com/graph-platform/query-service:<tag> .
      docker push <acct>.dkr.ecr.<region>.amazonaws.com/graph-platform/query-service:<tag>
      # repeat for indexer, web
      ```
- [ ] ECS cluster exists; task definitions reference the pushed image tags
- [ ] EFS volumes provisioned and mounted in the task defs:
      - neo4j data volume -> `/data` on the neo4j task
      - indexer workdir volume -> `/app/workdir` on the indexer task
- [ ] Internal ALB target group health check points at `GET /ready`, not
      `/health` - `/ready` checks Neo4j connectivity, so the ALB stops
      routing here if the database is unreachable; `/health` is pure liveness
      and stays "ok" even then.
      ```
      aws elbv2 describe-target-health --target-group-arn <tg-arn>
      ```

## A. Freeze

Stop the laptop/Mac from writing before ECS writes anything.

- [ ] Stop the running `--interval` indexer process on the laptop
      ```
      # find it
      ps aux | grep '[i]ndexer --interval'
      kill <pid>          # SIGTERM
      ```
      This is an immediate cancellation, not a graceful drain: SIGTERM cancels
      the working context right away, and any in-flight subprocess (git,
      graphify) is killed mid-command, not waited on. Whatever repo was
      indexing when the signal landed does not have its state advanced (it's
      recorded as canceled, not success or failure), so re-running later is
      safe - it just re-does that one repo from scratch.
- [ ] Confirm no indexer process still holds the workdir lock
      ```
      lsof workdir/state.json    # empty output = clear
      ```
- [ ] Record the last successful run from its logs (repo, commit, timestamp) - this
      is the known-good state the snapshot in step B captures.

## B. Snapshot

Take the on-demand backup before ECS's first write. The sweep step deletes
graph nodes that no longer match the current commit and has no test coverage
for the ECS path yet - this snapshot is the undo button if it deletes wrong.
This step's snapshot is taken with Neo4j idle (nothing has written yet), which
is what makes it trustworthy - see the crash-consistency note in step E before
relying on any *later* snapshot the same way.

- [ ] On-demand AWS Backup of the Neo4j EFS volume
      ```
      aws backup start-backup-job \
        --backup-vault-name <vault> \
        --resource-arn <efs-neo4j-arn> \
        --iam-role-arn <backup-role-arn>
      ```
- [ ] Wait for `COMPLETED` state before proceeding
      ```
      aws backup describe-backup-job --backup-job-id <job-id>
      ```

## C. Start

Small blast radius first: 10-20 repos, not the whole org.

> Note: if this deploy carries a graph schema bump (see `GraphSchemaVersion`),
> every repo re-indexes automatically on its first cycle even where HEAD is
> unchanged - expect the first pass to take as long as a full `--force` run.

- [ ] Point the ECS indexer task's config at a trimmed repo list (10-20 repos)
- [ ] Run one-shot (no `--interval` yet)
      ```
      aws ecs run-task --cluster <cluster> --task-definition indexer \
        --overrides '{"containerOverrides":[{"name":"indexer","command":["/usr/local/bin/indexer","--config","config/repos.yaml","--all"]}]}'
      ```
- [ ] Tail logs live
      ```
      aws logs tail /ecs/indexer --follow
      ```

## D. Verify

- [ ] For each repo in the run, the summary line shows `status=success`
- [ ] For each repo, **no** `[MISMATCH: N in graph]` marker in the summary.
      A mismatch means node_key collisions or silent data loss - stop here and
      investigate before indexing anything else. Do not proceed to more repos
      with an unresolved mismatch.
- [ ] Spot-check query-service through the ALB
      ```
      curl -H "Authorization: Bearer $QUERY_AUTH_TOKEN" https://<alb-host>/search?q=<known-symbol>
      curl -H "Authorization: Bearer $QUERY_AUTH_TOKEN" https://<alb-host>/overview/<one-of-the-piloted-repos>
      ```
- [ ] Load the web UI through Zscaler, confirm pages render and results come back
- [ ] From one pilot laptop, confirm the MCP binary works against the central URL
      (not a local `graphify-out/`)

## E. Steady state

- [ ] Switch the indexer task to `--interval` mode (continuous)
      ```
      aws ecs update-service --cluster <cluster> --service indexer \
        --task-definition indexer:<revision-with-interval-flag>
      ```
- [ ] Enable the CloudWatch index-lag alarm
- [ ] Enable the daily EFS backup schedule (AWS Backup plan, not just the
      on-demand snapshot from step B). Note: unlike step B's snapshot (taken
      with Neo4j idle), these daily snapshots are taken while the database is
      live and writing - EFS snapshots of a running Neo4j are not guaranteed
      crash-consistent. Treat them as best-effort, not a guaranteed restore
      point; the real recovery path for a corrupted graph is a clean
      re-index from source (repos + extractors), not a live snapshot restore.
- [ ] Widen the repo set gradually - not the full org in one step. Re-run
      step D's mismatch check after each widening.

## Rollback

- [ ] Stop the ECS indexer service
      ```
      aws ecs update-service --cluster <cluster> --service indexer --desired-count 0
      ```
- [ ] Restore the Neo4j EFS volume from the step B snapshot
      ```
      aws backup start-restore-job --recovery-point-arn <recovery-point-arn> \
        --iam-role-arn <backup-role-arn> --resource-type EFS
      ```
- [ ] Resume the laptop indexer as the interim writer (`--interval`) until the
      ECS issue is root-caused

---

> **Single writer only.** At no point may two indexers point at the same
> Neo4j instance. Two writers racing silently deletes nodes the other writer
> still needs. This is now enforced in the database: each indexer acquires a
> writer lease (`IndexerLease` node, `--lease-ttl`, default 15m) on startup,
> renews it before every repo (both one-shot and `--interval` mode), and a
> background heartbeat renews it every `ttl/4` (giving up after 3 straight failures, before expiry) so a single long repo
> can't outlive the TTL between renewals; a second indexer refuses to start
> (or stops immediately on its next renewal) while the lease is held.
> The freeze step below remains as belt-and-braces - don't rely on the lease
> alone to skip it. If a crashed indexer leaves a stuck lease (rare - it
> self-expires after the TTL), `--steal-lease` takes it unconditionally for
> operator recovery; use it deliberately, not as a routine workaround. Before
> starting the ECS indexer (step C), confirm the laptop one is fully stopped
> (step A). Before resuming the laptop one (rollback), confirm the ECS
> service is at desired-count 0.
