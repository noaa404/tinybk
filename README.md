# tinybk

**A light, token-cheap browser automation CLI for shell-capable AI agents.**

`tinybk` is a single Go binary built on [chromedp](https://github.com/chromedp/chromedp). It attaches to an already-running Chromium-based browser over CDP, performs one action, prints compact JSON, and exits.

**One action per call. Small JSON. No runtime, daemon, extension, or separate browser driver.**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27.2-00ADD8?logo=go&logoColor=white)](go.mod)
![Status](https://img.shields.io/badge/status-alpha-orange)

> Early development. CLI options and JSON fields may change.

## Why tinybk

- **Cheap for agents** — one action per call. Compact JSON, bounded page text, opt-in screenshots, and paginated maps keep context costs low.
- **Uses your browser** — connects directly to Chrome, Chromium, Brave, Edge, or another Chromium-based browser with CDP enabled. No separate automation browser to download or manage.
- **Useful results, not just OK** — action results include the page URL/title and target details where relevant. `click`, `press`, and `drag` report whether the URL or page text changed and new tabs when detected; `type` returns the final input value.
- **Concurrency-aware** — cross-process file locks serialize writes to the same tab while shared reads can run concurrently.

## Who it's for

Any agent that can run shell commands: Claude Code, Codex, Cursor, Gemini CLI, custom agents, or a plain script. It is built for shell-first workflows such as authenticated web tasks, UI checks, form filling, and parallel tab work against an already-open browser.

`tinybk` is intentionally a CLI primitive, not an MCP server, browser launcher, or Playwright replacement. See [Limitations](#limitations) before using it for complex sites.

## Small commands, page-level escape hatch

`tinybk` intentionally exposes a small, predictable command set. Built-in commands cover common interactions and add validation, structured evidence, and cross-process locking. When a page needs something they do not cover, `eval` gives the agent the full page JavaScript environment:

```bash
tinybk eval 'document.querySelector("#details").click()'
```

This is the escape hatch that keeps the CLI small without making it limited. It is useful for open shadow DOM, canvas-driven UI, app-specific state, storage, synthetic events, and workflows that are easier to express directly in JavaScript.

`eval` runs in the page's existing JavaScript context, with the same privileges and restrictions as any script already running on that page. It does not provide the hit testing or structured evidence of built-in commands.

## Requirements

- macOS or Linux (Unix file locks are required; Windows is currently unsupported)
- A running Chromium-based browser with CDP enabled and at least one open tab (default CDP endpoint: `http://127.0.0.1:9222`)
- Go 1.27.2 or newer to build from source

`tinybk` never launches a browser. It only connects to one you already started.

## Install

Install the `tinybk` command:

```bash
go install github.com/noaa404/tinybk@latest
```

Make sure `$(go env GOPATH)/bin` is in `PATH`, then verify:

```bash
tinybk --version
```

From a local checkout, use:

```bash
go install .
```

If you prefer a local build without installing, run `go build -o tinybk .`
and use `./tinybk` in place of `tinybk`.

## Quick start

### Ask your agent to set it up

Send this to an agent with shell access:

```text
Clone https://github.com/noaa404/tinybk, install it with `go install .`,
run `./scripts/start-browser.sh`, then run `tinybk doctor` and show me the output.
```

### Manual setup

```bash
git clone https://github.com/noaa404/tinybk.git
cd tinybk
go install .
```

### Start a browser

The launcher auto-detects Chrome, Chromium, Brave, or Edge, opens `about:blank`,
and waits until CDP is reachable:

```bash
./scripts/start-browser.sh
```

Use `--browser` to choose a browser, `--profile` to choose a persistent profile,
and `--port` to change the CDP port:

```bash
./scripts/start-browser.sh --browser brave
./scripts/start-browser.sh --profile "$HOME/.tinybk/profile"
```

`--browser` also accepts an explicit binary path. If startup fails, check
`<profile>/tinybk-launch.log`.

Verify the connection:

```bash
tinybk doctor
```

Then drive the browser one action at a time:

```bash
tinybk goto https://example.com
tinybk see
tinybk map --role link
tinybk click ref=@p
```

### Example output

IDs, short codes, and refs vary between runs.

```bash
$ tinybk --text 0 see
{"action":"see","short":"2891B","url":"https://example.com/","title":"Example Domain"}
```

```bash
$ tinybk map --role link
{"action":"map","short":"2891B","items":[{"ref":"@p","tag":"a","role":"link","text":"Learn more","href":"https://iana.org/help/example-domains"}],"matched_total":1,"returned":1,"truncated":false}
```

## Commands

Global options can appear anywhere: `--cdp`, `--target`, `--tab`, `--text`, `--lock-timeout`, `--shot`, and `--shot-dir`.

| Command | Description |
| --- | --- |
| `goto <url>` | Navigate to a URL. Adds `https://` when the scheme is missing. |
| `back` | Go back one page. |
| `see` | Observe the current page: URL, title, text, optional screenshot. |
| `map` | Enumerate elements in tinybk's built-in interactive selector/role set. |
| `click <css\|ref=@id\|label=text>` | Click by CSS selector, ref, or a text query matched against the element's name. |
| `type <css\|ref=@id> <text>` | Append text to an input. |
| `press <key\|mod+key>` | Send a key or key combination. |
| `scroll <top\|bottom\|±N>` | Scroll the page. |
| `wait [--url ...] [--contains ...] [--gone ...] [--stable ...]` | Wait for a page condition. |
| `eval <js>` | Evaluate JavaScript in the page. |
| `shot [dir]` | Save a PNG screenshot of the viewport. |
| `drag <source> <target>` | Drag from one element to another. |
| `upload <target> <file>...` | Set files on an `input[type=file]`. |
| `frames` | List the page frame tree. |
| `tab` | List open tabs. |
| `tab new [url]` | Create a background tab. |
| `tab activate <id\|prefix>` | Bring a tab to the foreground. |
| `tab close <id\|prefix>` | Close a tab. |
| `doctor` | Check CDP connectivity and list tabs. |
| `help [command]` | Show command help. |

Run `tinybk help` or `tinybk help <command>` for full flags, examples, and output fields.

Useful details:

- When multiple tabs are open, pass `--target <id|prefix>` to choose one. `tab` returns `id` and `short`; `short` is the first 5 characters of the id. `--target` accepts the full id or any unique prefix of at least 5 characters. `--tab N` is a transient index and is less stable.
- A `ref=@...` value comes from `map` and identifies a DOM node. Re-run `map` after navigation or page updates.
- `click`, `type`, `press`, and `drag` require the target tab to be visible and in the foreground.
- `label=` in `click` is a text query, not an element type. It matches `aria-label`, `innerText`, `value`, `placeholder`, and `title`; exact matches beat prefixes.

## Benchmarks

Two benchmarks are included:

- **Runtime benchmark**: deterministic CLI latency, output size, and correctness
  checks against the real binary. No model API is required. Run with
  `BK_BENCH=1 go test -run '^TestRuntimeBenchmark$' -count=1 -v`.
- **Agent-loop benchmark**: multi-step browser tasks through a fresh agent
  session, measuring tokens, cost, API/tool calls, and wall time. It requires a
  model provider and is run manually, not in required CI.

Current agent-loop MVP across 6 local tasks × 3 repetitions per lane
(pi 1.6.0, DeepSeek `deepseek-flash`, tinybk 9d7815e, PinchTab 0.15.2,
Chromium 158):

| Metric | tinybk | PinchTab |
|---|---:|---:|
| Success | 18/18 | 18/18 |
| Total tokens | 98,965 | 134,816 |
| Median tokens/run | 4,724 | 6,968 |
| Total cost | $0.01074 | $0.01330 |
| API calls | 70 | 91 |
| Tool calls | 52 | 74 |
| Median wall time | 4.31s | 5.99s |

Both lanes were driven through minimal command wrappers written for this
benchmark. The PinchTab guide is not an official recommendation and does not
exercise its full feature set. These are directional results for the tested
tasks and environment, not a general product ranking. See
[benchmark/README.md](benchmark/README.md) for methodology and full results.
**Additional test cases, independent runs, and corrections are welcome** via
[GitHub issues](https://github.com/noaa404/tinybk/issues).

## Multiple browsers and agents

`tinybk` is one binary; each invocation is its own process. To isolate agents,
run one browser instance per agent with a distinct CDP port and profile:

```bash
./scripts/start-browser.sh --port 9222 --profile "$HOME/.tinybk/agents/a"
./scripts/start-browser.sh --port 9223 --profile "$HOME/.tinybk/agents/b"

tinybk --cdp http://127.0.0.1:9222 see
tinybk --cdp http://127.0.0.1:9223 see
```

Never run two browser processes against the same profile. Within one browser,
writes to the same tab are serialized by a file lock. Use separate browser
instances when agents need independent foreground windows or must not wait on
each other.

Keep the same `--cdp` spelling for one browser (`127.0.0.1` vs `localhost`);
tab lifecycle locks use that endpoint string as part of their key.

## Output and exit codes

Command results and errors are JSON on stdout. `help` is the exception: it prints plain text.

| Exit code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Browser, page, target, or lock failure |
| `2` | Invalid command line usage |

Errors include a stable `code` field:

```json
{"action":"see","code":"target_not_found","error":"..."}
```

## Limitations

- **Chromium only.** Firefox and Safari do not use the Chrome DevTools Protocol in the same way.
- **Unix only.** Windows is not supported because cross-process locking uses `flock`.
- **Same-origin iframes only.** Cross-origin frames and OOPIF automation are not supported yet.
- **No Shadow DOM traversal.** `map` and CSS lookup do not enter shadow roots; `eval` can access open shadow roots when addressed explicitly.
- **Minimal command set.** Common page-level gaps can often be handled with `eval`; browser-level features such as native dialogs, network interception, console capture, cross-tab scripting, and video recording have no dedicated commands.
- **No browser-managed download support.** Public URLs can be fetched with `curl`, and page-scoped data can be fetched with `eval`, but browser downloads, save paths, and completion events are not exposed.
- **Viewport screenshots only.** Full-page and element screenshots are not available.
- **Built-in input needs a visible tab.** `tinybk` only sends real mouse and keyboard input to a visible tab. In headed Chrome, hidden tabs can drop those events; failing closed avoids reporting a click or keypress that never happened. Use `eval` for page-level work on background tabs. Headless behavior varies, so there is no reliable background-input mode.

## Security

`tinybk` can control the browser, execute page JavaScript, and upload local files. Use it only with a browser and pages you trust.

- Use a dedicated browser profile: CDP gives full control over that profile and its logged-in sessions.
- Run `eval` only with trusted input.

## License

MIT © 2026 noaa404
