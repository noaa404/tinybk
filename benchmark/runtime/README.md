# Runtime benchmark

The runtime benchmark exercises the real `tinybk` binary against the local E2E
fixtures and headless Chromium. It measures:

- p50 and p95 command latency
- mean latency
- average stdout size
- success rate
- correctness rate

Scenarios are intentionally small:

- `see`
- `map`
- `type`
- `map -> click -> see`

## Run

From the repository root:

```bash
BK_BENCH=1 go test -run '^TestRuntimeBenchmark$' -count=1 -v
```

Optional iteration controls:

```bash
BK_BENCH_ITERS=20 BK_BENCH_WARMUP=3 \
  BK_BENCH=1 go test -run '^TestRuntimeBenchmark$' -count=1 -v
```

The implementation lives in `bench_test.go` at the repository root because it
reuses the shared E2E harness (`TestMain`, local HTTP fixtures, and the compiled
CLI binary). It is skipped unless `BK_BENCH=1` is set.

The run writes `bench-results.json` in the current directory. This generated file
is ignored by Git.
