#!/usr/bin/env bash

# Start a Chromium-based browser with CDP enabled.
# This script does not launch a long-lived daemon; it starts the browser,
# waits for CDP to become reachable, prints connection details, and exits.

set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/start-browser.sh [options]

Options:
  --browser NAME|PATH   chrome, chromium, brave, edge, or an explicit binary path
  --port PORT           CDP port (default: 9222)
  --profile DIR         persistent profile directory (default: $HOME/.tinybk/profile)
  -h, --help            show this help

The browser opens about:blank. Use tinybk for navigation and actions.
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need_value() {
  if [ "$#" -lt 2 ] || [ -z "$2" ]; then
    die "missing value for $1"
  fi
}

browser=""
port="9222"
profile="${HOME:-}/.tinybk/profile"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --browser)
      need_value "$@"
      browser="$2"
      shift 2
      ;;
    --browser=*)
      browser="${1#*=}"
      shift
      ;;
    --port)
      need_value "$@"
      port="$2"
      shift 2
      ;;
    --port=*)
      port="${1#*=}"
      shift
      ;;
    --profile)
      need_value "$@"
      profile="$2"
      shift 2
      ;;
    --profile=*)
      profile="${1#*=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1 (try --help)"
      ;;
  esac
done

case "$port" in
  ''|*[!0-9]*) die "port must be an integer: $port" ;;
esac
if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
  die "port must be between 1 and 65535: $port"
fi

if [ -z "$profile" ]; then
  die "profile directory cannot be empty"
fi
case "$profile" in
  "~/"*) profile="$HOME/${profile#\~/}" ;;
esac
mkdir -p "$profile" || die "cannot create profile directory: $profile"
profile=$(CDPATH= cd -- "$profile" && pwd)

os=$(uname -s 2>/dev/null || true)
case "$os" in
  Darwin|Linux) ;;
  *) die "unsupported operating system: ${os:-unknown} (macOS and Linux only)" ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  die "curl is required to check the CDP endpoint"
fi

cdp_ready() {
  curl -fsS --max-time 1 "http://127.0.0.1:${port}/json/version" >/dev/null 2>&1
}

find_browser() {
  local name="$1"
  local p

  case "$name" in
    chrome)
      if [ "$os" = "Darwin" ]; then
        p="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
        [ -x "$p" ] && { printf '%s\n' "$p"; return 0; }
      fi
      for p in google-chrome google-chrome-stable chrome; do
        command -v "$p" >/dev/null 2>&1 && { command -v "$p"; return 0; }
      done
      ;;
    chromium)
      if [ "$os" = "Darwin" ]; then
        p="/Applications/Chromium.app/Contents/MacOS/Chromium"
        [ -x "$p" ] && { printf '%s\n' "$p"; return 0; }
      fi
      for p in chromium chromium-browser; do
        command -v "$p" >/dev/null 2>&1 && { command -v "$p"; return 0; }
      done
      ;;
    brave)
      if [ "$os" = "Darwin" ]; then
        p="/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"
        [ -x "$p" ] && { printf '%s\n' "$p"; return 0; }
      fi
      for p in brave-browser brave; do
        command -v "$p" >/dev/null 2>&1 && { command -v "$p"; return 0; }
      done
      ;;
    edge)
      if [ "$os" = "Darwin" ]; then
        p="/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
        [ -x "$p" ] && { printf '%s\n' "$p"; return 0; }
      fi
      for p in microsoft-edge microsoft-edge-stable; do
        command -v "$p" >/dev/null 2>&1 && { command -v "$p"; return 0; }
      done
      ;;
    *)
      return 1
      ;;
  esac
  return 1
}

if [ -n "$browser" ]; then
  case "$browser" in
    */*)
      [ -x "$browser" ] || die "browser binary is not executable: $browser"
      browser_bin="$browser"
      ;;
    *)
      browser_key=$(printf '%s' "$browser" | tr '[:upper:]' '[:lower:]')
      browser_bin=$(find_browser "$browser_key" || true)
      [ -n "$browser_bin" ] || die "browser not found: $browser (try chrome, chromium, brave, or edge)"
      ;;
  esac
else
  browser_bin=""
  for candidate in chrome chromium brave edge; do
    browser_bin=$(find_browser "$candidate" || true)
    [ -n "$browser_bin" ] && break
  done
  [ -n "$browser_bin" ] || die "no Chromium-based browser found (try --browser /path/to/binary)"
fi

if cdp_ready; then
  printf 'CDP already available at http://127.0.0.1:%s\n' "$port"
  printf 'Not starting another browser.\n'
  exit 0
fi

log_file="${profile}/tinybk-launch.log"
args=(
  "--remote-debugging-port=${port}"
  "--remote-debugging-address=127.0.0.1"
  "--user-data-dir=${profile}"
  "--no-first-run"
  "--no-default-browser-check"
  "--disable-backgrounding-occluded-windows"
  "--disable-renderer-backgrounding"
  "--disable-background-timer-throttling"
  "about:blank"
)

nohup "$browser_bin" "${args[@]}" >"$log_file" 2>&1 &
pid=$!

deadline=$((SECONDS + 20))
while ! cdp_ready; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    printf 'error: CDP did not become ready within 20s\n' >&2
    printf 'log: %s\n' "$log_file" >&2
    printf 'hint: close any browser already using profile %s and try again\n' "$profile" >&2
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
    fi
    exit 1
  fi
  sleep 0.2
done

printf 'Browser: %s\n' "$(basename "$browser_bin")"
printf 'CDP:     http://127.0.0.1:%s\n' "$port"
printf 'Profile: %s\n' "$profile"
printf 'PID:     %s\n' "$pid"
printf 'Next:    tinybk doctor\n'
