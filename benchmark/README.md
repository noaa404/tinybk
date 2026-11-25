# Benchmark

This directory contains tinybk benchmarks. They serve different purposes and
must not be conflated.

## Runtime benchmark

A deterministic benchmark for latency, output size, and correctness of the real
CLI. It does not call a model API and is suitable for local regression checks.

Entry point:

```bash
BK_BENCH=1 go test -run '^TestRuntimeBenchmark$' -count=1 -v
```

The source remains at the repository root as `bench_test.go` because it shares
the browser and fixture harness with the E2E tests.

See [runtime/README.md](runtime/README.md).

## Agent-loop benchmark

A manual benchmark that compares the end-to-end cost of completing browser tasks
through an agent using tinybk versus PinchTab.

It requires:

- `pi`
- a configured model provider
- real model API calls
- a local Chromium binary

It is intentionally not part of required CI because it costs money and is
model-dependent.

See [agent-loop/README.md](agent-loop/README.md).

## Results

The agent-loop results are curated snapshots, not automatically regenerated CI
artifacts:

- `agent-loop/results/mvp-deepseek-flash.json`
- `agent-loop/results/long-tasks.json`

The runtime benchmark writes a local `bench-results.json` file at the repository
root. That file is generated output and is ignored by Git.
