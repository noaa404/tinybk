# Benchmark MVP

Real-agent-loop browser benchmark

This is intentionally small. It measures the cost of completing a browser task
through an agent, not the byte size of a single tool response.

## Methodology

- Same machine, Chromium build, agent runtime, provider, and model for both lanes.
- One fresh `pi` process per run (`--no-session`), with a fresh workdir.
- Each lane has its own browser wrapper:
  - `tinybk`: CDP-attached Chrome started by the harness.
  - `PinchTab`: isolated server, profile, home, and session.
- Each task starts from a reset page.
- Correctness requires both:
  1. the expected answer in the model's final response, and
  2. page-generated events in the local fixture server.
- Token use is read from pi's JSON event stream (`turn_end.message.usage`),
  including cache reads and provider-reported cost.
- The harness rejects runs where the agent used a non-`./browser` command.
- Both lanes use minimal command wrappers written for this benchmark. The
  PinchTab guide is not an official recommendation and does not exercise its
  full feature set. This measures agent-loop cost, not general product
  capability or performance.

## Tasks

| Task | Goal |
|---|---|
| task1 | Read an order code from a page |
| task2 | Click Approve and report the status |
| task3 | Fill three fields, submit, and report the status |
| task4 | Follow a link and report an activation code |
| task5 | Longer order flow: start, fill three fields, submit, report code |
| task6 | Conditional review flow: read status, choose the correct action, submit, report code |

## Files

- `runner.py`: orchestration, browser setup, agent execution, usage parsing, and reporting.
- `fixtures/server.py`: local fixture HTTP server and page-event recorder.
- `fixtures/tasks.json`: task URLs, prompts, expected answers, and event assertions.
- `fixtures/pages/*.html`: task pages and page-level JavaScript.
- `results/mvp-deepseek-flash.json`: full result set from the first 4-task MVP run.
- `results/long-tasks.json`: supplementary 2-task long-workflow run.

The fixture pages are intentionally separate from the runner so reviewers can
inspect and modify task behavior without editing Python orchestration.

## Run

Prerequisites:

- Linux with Chromium
- tinybk binary
- PinchTab binary
- pi with a configured DeepSeek provider
- Python 3 standard library

Example:

```bash
ulimit -n 65536

python3 runner.py \
  --tinybk /path/to/tinybk \
  --pinchtab /path/to/pinchtab \
  --pi /path/to/pi \
  --chromium /usr/bin/chromium \
  --provider deepseek \
  --model deepseek-flash \
  --reps 3 \
  --workdir /tmp/tinybk-benchmark-run \
  --out results/local.json
```

The command prints a summary and writes a consolidated JSON result file.

For the long tasks only:

```bash
python3 runner.py ... --tasks task5,task6 --reps 3 --out results/long-tasks.json
```

## MVP result

Environment:

```text
Linux x86_64
Chromium 158.0.8127.88
pi 1.6.0
DeepSeek deepseek-flash
tinybk 9d7815e (local benchmark build)
PinchTab 0.15.2
thinking off
4 tasks x 2 lanes x 3 repetitions = 24 runs
```

| Metric | tinybk | PinchTab |
|---|---:|---:|
| Success | 12/12 | 12/12 |
| Total tokens | 47,811 | 72,798 |
| Median tokens/run | 3,641 | 5,182 |
| Total cost | $0.005119 | $0.007238 |
| Median cost/run | $0.000396 | $0.000544 |
| API calls | 37 | 51 |
| Tool calls | 25 | 39 |
| Median wall time | 3.33s | 4.78s |

Per task (median tokens):

| Task | tinybk | PinchTab |
|---|---:|---:|
| task1 | 2,288 | 2,233 |
| task2 | 3,623 | 5,094 |
| task3 | 5,980 | 7,069 |
| task4 | 3,659 | 6,933 |


## Long-task supplement

A second MVP pass added two longer tasks:

- `task5`: click Start order -> fill three fields -> submit -> report order code.
- `task6`: read status -> choose the conditional review path -> fill note -> submit -> report code.

Environment was the same as the first run: pi 1.6.0, DeepSeek
`deepseek-flash`, thinking off, Chromium 158, 3 repetitions per lane.

| Metric | tinybk | PinchTab |
|---|---:|---:|
| Runs | 6 | 6 |
| Success | 6/6 | 6/6 |
| Total tokens | 51,154 | 62,018 |
| Median tokens/run | 7,582 | 10,792 |
| Total cost | $0.005617 | $0.006060 |
| Median cost/run | $0.000921 | $0.001059 |
| API calls | 33 | 40 |
| Tool calls | 27 | 35 |
| Median wall time | 5.76s | 7.80s |

Per task (median tokens):

| Task | tinybk | PinchTab |
|---|---:|---:|
| task5 | 7,611 | 10,693 |
| task6 | 7,485 | 10,890 |

The long tasks preserve the direction of the first run: tinybk used fewer
API/tool turns. The gap is smaller than in the short navigation task, but
still material. This remains an MVP sample, not a statistical claim.

## Caveats

- MVP only: one model, three repetitions per task, six deterministic local tasks across two result sets.
- Model output is nondeterministic. One PinchTab task3 run used many extra
  selector attempts and is visible as an outlier in the raw results.
- Command guides are necessarily tool-specific and are not identical text.
- This does not measure installation, long-running operation, SPA behavior,
  large pages, concurrency, or Windows.
- The result is directional evidence, not a statistically robust claim.
- **Additional test cases, independent runs, and corrections are welcome** via
  [GitHub issues](https://github.com/noaa404/tinybk/issues).

## Interpretation

The important result is that compact single-response output is not the same as
low end-to-end token cost. PinchTab often returned smaller individual responses,
but the agent needed more calls to discover refs and confirm state. In this MVP,
tinybk used fewer API turns and fewer tool calls on multi-step tasks, which led to
lower total token and cost usage.
