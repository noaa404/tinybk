package main

// End-to-end tests: the real CLI binary against real Chrome and local httptest pages.
// Run: go test -v -timeout 5m
// Prerequisite: macOS Chrome at the standard path; the suite skips if absent.
// Isolation: test port 37923 and a temporary profile.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const cdpPort = "37923" // Test-only port; 37922 is reserved for the daily Chrome.

const cdpAddr = "http://127.0.0.1:" + cdpPort

var (
	tinybkBin string
	baseURL   string
	chrome    *exec.Cmd
	srv       *httptest.Server
	tmpDir    string
)

const page1 = `<!doctype html>
<html><head><meta charset="utf-8"><title>测试首页</title></head><body>
<h1>测试首页</h1>
<a id="nav" href="/second">去第二页</a>
<input id="box" type="text">
<button id="btn" onclick="document.getElementById('out').textContent='按钮已点击'">点我</button>
<a id="anchor" href="#bottom">页内锚点</a>
<div id="out"></div>
<div style="height:3000px">占高内容</div>
<div id="bottom">页面底部</div>
</body></html>`

const page2 = `<!doctype html>
<html><head><meta charset="utf-8"><title>第二页</title></head><body>
<h1>第二页</h1>
<a id="home" href="/">回首页</a>
</body></html>`

const labelPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>按名字找</title></head><body>
<h1>按名字找元素</h1>
<a id="nav2" href="/second">去第二页</a>
<input id="q" type="search" placeholder="搜索商品">
<button id="b1" onclick="document.getElementById('out2').textContent='甲'">提交</button>
<button id="b2" onclick="document.getElementById('out2').textContent='乙'">提交</button>
<button id="close" aria-label="关闭" onclick="document.getElementById('out2').textContent='已关闭'">×</button>
<button id="help" aria-label="帮助中心" onclick="document.getElementById('out2').textContent='帮助'">?</button>
<div id="out2"></div>
</body></html>`

const smoothPage = `<!doctype html>
<html style="scroll-behavior:smooth"><head><meta charset="utf-8"><title>平滑滚动</title></head><body>
<div style="height:3000px">占高内容</div>
<a id="more" href="/second">more</a>
<a id="pop" href="/second" target="_blank">新窗口</a>
</body></html>`

const mapFilterPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>map 检索</title></head><body>
<div id="q1">
  <button id="q1-next" onclick="document.title='q1-next'">下一题</button>
  <div id="q1-a" role="radio" tabindex="0" onclick="document.title='q1-a'">A 连接</div>
  <div id="q1-b" role="radio" tabindex="0" onclick="document.title='q1-b'">B 断开</div>
</div>
<div id="q2">
  <button id="q2-next" onclick="document.title='q2-next'">下一题</button>
  <div id="q2-a" role="radio" tabindex="0" onclick="document.title='q2-a'">A 连接</div>
  <div id="q2-b" role="radio" tabindex="0" onclick="document.title='q2-b'">B 断开</div>
</div>
<input id="native-check" type="checkbox" aria-label="同意">
<button id="aria-close" aria-label="关闭">×</button>
</body></html>`

const iframeOuterPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>iframe 外层</title></head><body>
<button id="outer-btn">外层按钮</button>
<iframe id="sameframe" src="/iframe-inner" style="display:block;width:480px;height:180px;border:3px solid #333;padding:7px;margin:30px"></iframe>
</body></html>`

const iframeInnerPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>iframe 内层</title></head><body style="margin:12px">
<button id="inner-btn" onclick="parent.document.title='inner-clicked'">确认</button>
<input id="inner-input">
</body></html>`

const dragPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>拖拽测试</title>
<style>
#track { position: relative; width: 320px; height: 50px; margin: 40px; background: #ddd; }
#thumb { position: absolute; left: 0; top: 5px; width: 40px; height: 40px; background: #06c; }
</style></head><body>
<div id="track"><div id="thumb"></div></div>
<script>
window.__drag = {down: 0, moves: 0, up: 0};
const track = document.getElementById('track');
const thumb = document.getElementById('thumb');
let dragging = false;
thumb.addEventListener('pointerdown', () => { dragging = true; window.__drag.down++; });
window.addEventListener('pointermove', (e) => {
  if (!dragging) return;
  window.__drag.moves++;
  const r = track.getBoundingClientRect();
  const x = Math.max(0, Math.min(r.width - 40, e.clientX - r.left - 20));
  thumb.style.left = x + 'px';
});
window.addEventListener('pointerup', () => { if (dragging) { dragging = false; window.__drag.up++; } });
</script>
</body></html>`

const uploadPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>上传测试</title></head><body>
<input id="visible-file" type="file" multiple>
<input id="hidden-file" type="file" style="display:none">
<input id="not-file" type="text">
<script>
window.__changes = [];
for (const id of ['visible-file', 'hidden-file']) {
  const el = document.getElementById(id);
  el.addEventListener('change', () => {
    window.__changes.push({id: id, count: el.files.length, names: Array.from(el.files).map((f) => f.name)});
  });
}
</script>
</body></html>`

const spaPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>慢渲染</title></head><body>
<h1>列表页</h1>
<a id="go" href="/spa-detail" onclick="event.preventDefault(); history.pushState({}, '', '/spa-detail'); setTimeout(function () { document.getElementById('view').textContent = '详情已加载'; document.getElementById('loading').remove(); }, 800)">详情</a>
<div id="view">列表内容</div>
<div id="loading">加载中</div>
</body></html>`

func findChrome() string {
	if p := os.Getenv("BK_CHROME"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/brave-browser",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// TestMain starts a dedicated headless Chrome unless a test CDP endpoint is already available.
func TestMain(m *testing.M) {
	chromePath := findChrome()
	if chromePath == "" {
		msg := "no Chrome/Chromium found; set BK_CHROME to a browser binary"
		if os.Getenv("BK_REQUIRE_E2E") != "" {
			fmt.Fprintln(os.Stderr, "fatal e2e: "+msg)
			os.Exit(1)
		}
		fmt.Println("skip e2e: " + msg)
		os.Exit(0)
	}
	var err error
	tmpDir, err = os.MkdirTemp("", "tinybk-e2e-*")
	must(err)

	tinybkBin = filepath.Join(tmpDir, "tinybk")
	build := exec.Command("go", "build", "-o", tinybkBin, ".")
	var buildErr bytes.Buffer
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		fmt.Println("编译 tinybk 失败:", buildErr.String())
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/second" {
			io.WriteString(w, page2)
			return
		}
		if r.URL.Path == "/label" {
			io.WriteString(w, labelPage)
			return
		}
		if r.URL.Path == "/smooth" {
			io.WriteString(w, smoothPage)
			return
		}
		if r.URL.Path == "/iframe" {
			io.WriteString(w, iframeOuterPage)
			return
		}
		if r.URL.Path == "/iframe-inner" {
			io.WriteString(w, iframeInnerPage)
			return
		}
		if r.URL.Path == "/upload" {
			io.WriteString(w, uploadPage)
			return
		}
		if r.URL.Path == "/drag" {
			io.WriteString(w, dragPage)
			return
		}
		if r.URL.Path == "/map-filter" {
			io.WriteString(w, mapFilterPage)
			return
		}
		if r.URL.Path == "/spa" || r.URL.Path == "/spa-detail" {
			io.WriteString(w, spaPage)
			return
		}
		io.WriteString(w, page1)
	})
	srv = httptest.NewServer(mux)
	baseURL = srv.URL

	if cdpReachable(cdpAddr) {
		fmt.Println("复用已运行的 Chrome:", cdpAddr)
	} else {

		profile := filepath.Join(tmpDir, "profile")
		chromeArgs := []string{
			"--headless=new",
			"--remote-debugging-port=" + cdpPort,
			"--user-data-dir=" + profile,
			"--no-first-run", "--no-default-browser-check",
			"--disable-backgrounding-occluded-windows",
			"--disable-renderer-backgrounding",
			"--disable-background-timer-throttling",
			"--window-size=1280,900",
		}

		if os.Getenv("BK_E2E_NO_SANDBOX") != "" {
			chromeArgs = append(chromeArgs,
				"--no-sandbox", "--disable-gpu", "--disable-crash-reporter")
		}
		chromeArgs = append(chromeArgs, "about:blank")
		chrome = exec.Command(chromePath, chromeArgs...)
		if err := chrome.Start(); err != nil {
			fmt.Println("启动 Chrome 失败:", err)
			os.Exit(1)
		}

		deadline := time.Now().Add(20 * time.Second)
		for {
			resp, err := http.Get(cdpAddr + "/json/version")
			if err == nil {
				resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				fmt.Println("Chrome CDP 未就绪（端口", cdpPort, "可能被占用）")
				chrome.Process.Kill()
				os.Exit(1)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	activateFirstPage()

	code := m.Run()

	if chrome != nil {
		chrome.Process.Kill()
		chrome.Wait()
	}
	srv.Close()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

func cdpReachable(addr string) bool {
	resp, err := http.Get(addr + "/json/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func activateFirstPage() {
	resp, err := http.Get(cdpAddr + "/json/list")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var entries []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return
	}
	for _, e := range entries {
		if e.Type != "page" {
			continue
		}
		out, err := exec.Command(tinybkBin, "--cdp", cdpAddr, "tab", "activate", e.ID).CombinedOutput()
		if err != nil {
			fmt.Printf("激活 %s 失败（输入类用例可能报 tab_not_visible）: %v %s\n", e.ID, err, out)
		}
		return
	}
}

func must(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runTinyBKFull(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(tinybkBin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("执行 tinybk %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errOut.String(), code
}

func runTinyBK(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, _, code := runTinyBKFull(t, args...)
	return out, code
}

type tinybkRawResult struct {
	out  string
	code int
	err  error
}

func runTinyBKRaw(args ...string) tinybkRawResult {
	cmd := exec.Command(tinybkBin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return tinybkRawResult{out: out.String(), code: ee.ExitCode()}
		}
		return tinybkRawResult{out: out.String(), code: -1, err: err}
	}
	return tinybkRawResult{out: out.String(), code: 0}
}

func okJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, code := runTinyBK(t, args...)
	if code != 0 {
		t.Fatalf("tinybk %v 应成功，exit=%d，输出=%s", args, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("tinybk %v 输出非 JSON: %v\n%s", args, err, out)
	}
	if msg, ok := m["error"]; ok {
		t.Fatalf("tinybk %v 返回 error: %v", args, msg)
	}
	return m
}

func failJSON(t *testing.T, wantErr string, args ...string) map[string]any {
	t.Helper()
	out, code := runTinyBK(t, args...)
	if code != 1 {
		t.Fatalf("tinybk %v 应失败 exit=1，实际 exit=%d，输出=%s", args, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("tinybk %v 失败输出非 JSON: %v\n%s", args, err, out)
	}
	msg, _ := m["error"].(string)
	if wantErr != "" && !strings.Contains(msg, wantErr) {
		t.Fatalf("tinybk %v 错误应含 %q，实际 %q", args, wantErr, msg)
	}
	return m
}

func usageJSON(t *testing.T, wantErr string, args ...string) map[string]any {
	t.Helper()
	out, code := runTinyBK(t, args...)
	if code != 2 {
		t.Fatalf("tinybk %v 应为用法错误 exit=2，实际 exit=%d，输出=%s", args, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("tinybk %v 用法错误输出非 JSON: %v\n%s", args, err, out)
	}
	if m["code"] != "usage" {
		t.Fatalf("tinybk %v 用法错误应带 code=usage，实际 %v", args, m)
	}
	msg, _ := m["error"].(string)
	if wantErr != "" && !strings.Contains(msg, wantErr) {
		t.Fatalf("tinybk %v 错误应含 %q，实际 %q", args, wantErr, msg)
	}
	return m
}

func loadPage1(t *testing.T) {
	t.Helper()
	okJSON(t, "--cdp", cdpAddr, "goto", baseURL+"/")
}

func evalRaw(t *testing.T, expr string) any {
	t.Helper()
	m := okJSON(t, "--cdp", cdpAddr, "eval", expr)
	v, ok := m["result"]
	if !ok {
		t.Fatalf("eval %q 缺 result 字段: %v", expr, m)
	}
	return v
}

func wantTrue(t *testing.T, expr string) {
	t.Helper()
	if v := evalRaw(t, expr); v != true {
		t.Fatalf("eval %q 应为 true，实际 %v", expr, v)
	}
}

func tabURL(t *testing.T, tabs []any, i int) string {
	t.Helper()
	it, ok := tabs[i].(map[string]any)
	if !ok {
		t.Fatalf("tabs[%d] 形状异常: %v", i, tabs[i])
	}
	s, _ := it["url"].(string)
	return s
}

func tabID(t *testing.T, tabs []any, i int) string {
	t.Helper()
	it, ok := tabs[i].(map[string]any)
	if !ok {
		t.Fatalf("tabs[%d] 形状异常: %v", i, tabs[i])
	}
	s, _ := it["id"].(string)
	if s == "" {
		t.Fatalf("tabs[%d] 缺稳定 id: %v", i, tabs[i])
	}
	return s
}

func tabShort(t *testing.T, tabs []any, i int) string {
	t.Helper()
	it, ok := tabs[i].(map[string]any)
	if !ok {
		t.Fatalf("tabs[%d] 形状异常: %v", i, tabs[i])
	}
	s, _ := it["short"].(string)
	if len(s) != targetPrefixMin {
		t.Fatalf("tabs[%d].short 应为 %d 位，实际 %q: %v", i, targetPrefixMin, s, tabs[i])
	}
	if id, _ := it["id"].(string); !strings.HasPrefix(id, s) {
		t.Fatalf("tabs[%d].short 应为 id 前缀: id=%q short=%q", i, id, s)
	}
	return s
}

func cdpNewTab(t *testing.T, url string) string {
	t.Helper()
	req, err := http.NewRequest("PUT", cdpAddr+"/json/new?"+url, nil)
	if err != nil {
		t.Fatalf("构造新建标签页请求: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("新建标签页: %v", err)
	}
	defer resp.Body.Close()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("解析新建标签页响应: %v", err)
	}
	return v.ID
}

func cdpCloseTab(t *testing.T, id string) {
	t.Helper()
	resp, err := http.Get(cdpAddr + "/json/close/" + id)
	if err != nil {
		t.Fatalf("关闭标签页: %v", err)
	}
	resp.Body.Close()
}

func cdpCloseByURL(t *testing.T, url string) {
	t.Helper()
	closed := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(cdpAddr + "/json/list")
		if err != nil {
			t.Fatalf("列出标签页: %v", err)
		}
		var entries []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			URL  string `json:"url"`
		}
		err = json.NewDecoder(resp.Body).Decode(&entries)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("解析标签页列表: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Type != "page" || e.URL != url {
				continue
			}
			found = true
			if !closed[e.ID] {
				closed[e.ID] = true
				cdpCloseTab(t, e.ID)
			}
		}
		if !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("关闭 %s 超时", url)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func createdTab(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	c, ok := m["created"].(map[string]any)
	if !ok {
		t.Fatalf("应返回 created: %v", m)
	}
	return c
}

func TestHelp(t *testing.T) {
	out, code := runTinyBK(t, "help")
	if code != 0 {
		t.Fatalf("help 应 exit 0，实际 %d", code)
	}
	if !strings.Contains(out, "Usage: tinybk") {
		t.Fatalf("help 应为用法文本，实际: %s", out)
	}
	if json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatalf("help 应为纯文本而非 JSON: %s", out)
	}
	for _, want := range []string{
		"Command details",
		"tinybk help frames",
		"tinybk help map",
		"tinybk help click",
		"tinybk help upload",
		"tinybk help drag",
		"tinybk help press",
		"tinybk help shot",
		"tinybk help wait",
		"click [--force]",
		"upload [--frame VALUE]",
		"drag [--steps N]",
		"map [--frame VALUE] [--within VALUE]",
		"press [--times N] <key|mod+key>",
		"wait [--timeout ms]",
		"eval [--no-wait]",
		"tab | tab new [url]",
		"shot [dir]",
		"--shot-dir DIR",
		"--role button --name Login",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("主 help 应一次暴露完整能力签名，缺少 %q: %s", want, out)
		}
	}
	if d, c := strings.Index(out, "Command details"), strings.Index(out, "\nCommands:\n"); d < 0 || c < 0 || d > c {
		t.Fatalf("命令详情入口应在命令表之前醒目出现: d=%d c=%d\n%s", d, c, out)
	}

	out, code = runTinyBK(t, "help", "map")
	if code != 0 {
		t.Fatalf("help map 应 exit 0，实际 %d", code)
	}
	for _, want := range []string{"Examples:", "--within", "choice =", "matched_total", "next_offset"} {
		if !strings.Contains(out, want) {
			t.Fatalf("map help 应包含完整示例/语义/输出，缺少 %q: %s", want, out)
		}
	}

	out, code = runTinyBK(t, "help", "press")
	if code != 0 {
		t.Fatalf("help press 应 exit 0，实际 %d", code)
	}
	for _, want := range []string{"Control+A", "Shift+Tab", "F1..F12", "Meta/Cmd", "Browser-level shortcuts"} {
		if !strings.Contains(out, want) {
			t.Fatalf("press help 应包含快捷键能力，缺少 %q: %s", want, out)
		}
	}

	out, code = runTinyBK(t, "help", "shot")
	if code != 0 {
		t.Fatalf("help shot 应 exit 0，实际 %d", code)
	}
	for _, want := range []string{"current working directory", "--shot-dir", "shot ./artifacts/shots", "absolute path"} {
		if !strings.Contains(out, want) {
			t.Fatalf("shot help 应包含目录规则，缺少 %q: %s", want, out)
		}
	}

	out, code = runTinyBK(t, "help", "upload")
	if code != 0 {
		t.Fatalf("help upload 应 exit 0，实际 %d", code)
	}
	for _, want := range []string{"DOM.setFileInputFiles", "Hidden inputs", "label= is not supported", "input.files", "file>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("upload help 应包含文件输入能力，缺少 %q: %s", want, out)
		}
	}

	out, code = runTinyBK(t, "help", "drag")
	if code != 0 {
		t.Fatalf("help drag 应 exit 0，实际 %d", code)
	}
	for _, want := range []string{"pointer/mouse drag", "label= is not supported", "both be in the viewport", "HTML5 DataTransfer", "--steps"} {
		if !strings.Contains(out, want) {
			t.Fatalf("drag help 应包含拖拽能力，缺少 %q: %s", want, out)
		}
	}

	out, code = runTinyBK(t)
	if code != 2 {
		t.Fatalf("无参数应 exit 2，实际 %d，输出=%s", code, out)
	}
	if !strings.Contains(out, "missing command") {
		t.Fatalf("无参数应提示缺命令: %s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	a := []string{"--cdp", cdpAddr}

	m := usageJSON(t, "unknown command", append(a, "frobnicate")...)
	if m["action"] != "frobnicate" {
		t.Fatalf("错误输出应带 action=frobnicate: %v", m)
	}
	usageJSON(t, "missing command", "--cdp", cdpAddr)
	usageJSON(t, "Usage: tinybk goto", append(a, "goto")...)
	usageJSON(t, "Usage: tinybk type", append(a, "type", "#box")...)
	usageJSON(t, "Usage: tinybk click", append(a, "click")...)
	usageJSON(t, "Usage: tinybk press", append(a, "press")...)
	usageJSON(t, "Usage: tinybk scroll", append(a, "scroll")...)
	usageJSON(t, "Usage: tinybk wait", append(a, "wait")...)
	usageJSON(t, "Usage: tinybk eval", append(a, "eval")...)
	usageJSON(t, "--timeout", append(a, "wait", "--timeout", "0", "#out")...)
	usageJSON(t, "invalid scroll target", append(a, "scroll", "left")...)
	usageJSON(t, "chord is missing a base key", append(a, "press", "Ctrl")...)
	usageJSON(t, "--times", append(a, "press", "--times", "0", "Enter")...)
	usageJSON(t, "Usage", append(a, "tab", "xyz")...)

	failJSON(t, "tab 99 not found", append(a, "--tab", "99", "see")...)

	out, errOut, code := runTinyBKFull(t, "--nope", "see")
	if code != 2 {
		t.Fatalf("未知参数应 exit 2，实际 %d，输出=%s", code, out)
	}
	var bad map[string]any
	if err := json.Unmarshal([]byte(out), &bad); err != nil {
		t.Fatalf("未知参数应回 JSON: %v\n%s", err, out)
	}
	if bad["code"] != "usage" || !strings.Contains(out, "unknown option") {
		t.Fatalf("未知参数应报 usage，实际 %v", bad)
	}
	if strings.TrimSpace(errOut) != "" {
		t.Fatalf("未知参数不应写 stderr，实际 %q", errOut)
	}
}

func TestGlobalsAnywhere(t *testing.T) {

	loadPage1(t)

	m := okJSON(t, "--cdp", cdpAddr, "see", "--text", "0")
	if _, ok := m["text"]; ok {
		t.Fatalf("--text 0 不应输出正文: %v", m)
	}
	m = okJSON(t, "--cdp", cdpAddr, "goto", baseURL+"/", "--text", "0")
	if _, ok := m["text"]; ok {
		t.Fatalf("goto 后的 --text 0 也应生效: %v", m)
	}

	okJSON(t, "--cdp", cdpAddr, "type", "--", "#box", "--text")
	if v := evalRaw(t, "document.getElementById('box').value"); v != "--text" {
		t.Fatalf("-- 之后的 --text 应作为文本输入，实际 value=%v", v)
	}

	_, _, code := runTinyBKFull(t, "--cdp", cdpAddr, "--target", "deadbeef", "see")
	if code != 1 {
		t.Fatalf("不存在的 --target 应 exit 1，实际 %d", code)
	}
}

func TestBackNoHistory(t *testing.T) {
	id := cdpNewTab(t, "about:blank")
	defer cdpCloseTab(t, id)

	m := okJSON(t, "--cdp", cdpAddr, "tab")
	tabs, _ := m["tabs"].([]any)
	idx := -1
	for i := range tabs {
		if tabURL(t, tabs, i) == "about:blank" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("新标签页不在列表中: %v", tabs)
	}
	failJSON(t, "no earlier history entry", "--cdp", cdpAddr, "--tab", strconv.Itoa(idx), "back")
}

func TestDoctor(t *testing.T) {
	m := okJSON(t, "--cdp", cdpAddr, "doctor")
	if m["action"] != "doctor" {
		t.Fatalf("action 应为 doctor: %v", m)
	}
	if m["cdp"] != cdpAddr {
		t.Fatalf("cdp 应回显端点: %v", m)
	}
	if s, _ := m["browser"].(string); s == "" {
		t.Fatalf("browser 应带版本串: %v", m)
	}
	if tabs, _ := m["tabs"].([]any); len(tabs) == 0 {
		t.Fatalf("tabs 不应为空: %v", m)
	}
}

func TestGotoAndSee(t *testing.T) {
	a := []string{"--cdp", cdpAddr}

	m := okJSON(t, append(a, "goto", baseURL+"/")...)
	if m["action"] != "goto" {
		t.Fatalf("action 应为 goto: %v", m)
	}
	if m["url"] != baseURL+"/" {
		t.Fatalf("url 应为 %s: %v", baseURL+"/", m)
	}
	if m["title"] != "测试首页" {
		t.Fatalf("title 应为 测试首页: %v", m)
	}
	if txt, _ := m["text"].(string); !strings.Contains(txt, "测试首页") {
		t.Fatalf("text 应含正文: %v", m)
	}
	if s, _ := m["short"].(string); len(s) != targetPrefixMin {
		t.Fatalf("goto 应回显 %d 位 short: %v", targetPrefixMin, m)
	}
	if _, has := m["target_id"]; has {
		t.Fatalf("动作结果不应再带完整 target_id: %v", m)
	}

	m = okJSON(t, append(a, "see")...)
	if m["action"] != "see" {
		t.Fatalf("action 应为 see: %v", m)
	}
	if s, _ := m["short"].(string); len(s) != targetPrefixMin {
		t.Fatalf("see 应回显 %d 位 short: %v", targetPrefixMin, m)
	}
	if txt, _ := m["text"].(string); !strings.Contains(txt, "页面底部") {
		t.Fatalf("see 正文应覆盖页尾: %v", m)
	}

	m = okJSON(t, append(a, "--text", "0", "see")...)
	if _, has := m["text"]; has {
		t.Fatalf("--text 0 不应带 text 字段: %v", m)
	}
}

func TestMap(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "map")...)
	items, _ := m["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("应枚举 4 个元素，实际 %d: %v", len(items), items)
	}
	want := []map[string]string{
		{"tag": "a", "id": "nav", "href": "/second"},
		{"tag": "input", "id": "box"},
		{"tag": "button", "id": "btn", "text": "点我"},
		{"tag": "a", "id": "anchor", "href": "#bottom"},
	}
	seen := map[string]bool{}
	for i, w := range want {
		it, _ := items[i].(map[string]any)

		ref, _ := it["ref"].(string)
		if !strings.HasPrefix(ref, "@") || len(ref) < 2 {
			t.Fatalf("items[%d].ref 应为 @<id>，实际 %v", i, it["ref"])
		}
		if seen[ref] {
			t.Fatalf("items[%d].ref 与前面的重复: %v", i, ref)
		}
		seen[ref] = true
		for k, v := range w {
			if it[k] != v {
				t.Fatalf("items[%d].%s 应为 %q，实际 %v", i, k, v, it[k])
			}
		}
	}

	m = okJSON(t, append(a, "map", "--limit", "2")...)
	if items, _ = m["items"].([]any); len(items) != 2 {
		t.Fatalf("--limit 2 应截断为 2 项，实际 %v", m["items"])
	}
}

func TestMapQuery(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/map-filter")...)

	m := okJSON(t, append(a, "map", "--within", "#q1", "--role", "radio")...)
	items, _ := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("q1 应有 2 个 radio，实际 %v", m)
	}
	for i, wantID := range []string{"q1-a", "q1-b"} {
		it, _ := items[i].(map[string]any)
		if it["id"] != wantID || it["role"] != "radio" {
			t.Fatalf("items[%d] 应为 %s/radio，实际 %v", i, wantID, it)
		}
	}
	if m["matched_total"] != float64(2) || m["returned"] != float64(2) || m["truncated"] != false {
		t.Fatalf("within+role 元信息不对: %v", m)
	}

	m = okJSON(t, append(a, "map", "--role", "radio", "--name", "连接")...)
	if m["matched_total"] != float64(2) || m["returned"] != float64(2) {
		t.Fatalf("name 过滤应在两题各命中一个选项: %v", m)
	}
	m = okJSON(t, append(a, "map", "--role", "button", "--name", "关闭")...)
	items, _ = m["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("name 过滤应命中 aria-label=关闭，实际 %v", m)
	}
	if it, _ := items[0].(map[string]any); it["id"] != "aria-close" {
		t.Fatalf("命中的应是 aria-close，实际 %v", it)
	}

	m = okJSON(t, append(a, "map", "--role", "checkbox")...)
	items, _ = m["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应命中原生 checkbox，实际 %v", m)
	}
	if it, _ := items[0].(map[string]any); it["id"] != "native-check" || it["role"] != "checkbox" {
		t.Fatalf("checkbox 项不对: %v", it)
	}

	m = okJSON(t, append(a, "map", "--role", "radio", "--limit", "1")...)
	if m["matched_total"] != float64(4) || m["returned"] != float64(1) ||
		m["truncated"] != true || m["next_offset"] != float64(1) {
		t.Fatalf("limit 截断元信息不对: %v", m)
	}
	items, _ = m["items"].([]any)
	if it, _ := items[0].(map[string]any); it["id"] != "q1-a" {
		t.Fatalf("第一页应返回 q1-a，实际 %v", it)
	}
	m = okJSON(t, append(a, "map", "--role", "radio", "--limit", "1", "--offset", "1")...)
	items, _ = m["items"].([]any)
	if it, _ := items[0].(map[string]any); it["id"] != "q1-b" {
		t.Fatalf("offset=1 应返回 q1-b，实际 %v", it)
	}
	if m["matched_total"] != float64(4) || m["returned"] != float64(1) || m["truncated"] != true {
		t.Fatalf("offset 页元信息不对: %v", m)
	}

	m = okJSON(t, append(a, "map", "--role", "radio", "--limit", "1", "--offset", "3")...)
	if m["matched_total"] != float64(4) || m["returned"] != float64(1) || m["truncated"] != false {
		t.Fatalf("最后一页元信息不对: %v", m)
	}
}

func TestFramesSameOrigin(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/iframe")...)

	m := okJSON(t, append(a, "frames")...)
	frames, _ := m["frames"].([]any)
	if len(frames) != 2 {
		t.Fatalf("应有顶层 + 1 个 iframe，实际 %v", m)
	}
	var childFrame, childShort string
	for _, raw := range frames {
		f, _ := raw.(map[string]any)
		if f["depth"] == float64(0) {
			if f["same_origin"] != true || f["accessible"] != true {
				t.Fatalf("顶层 frame 诊断字段不对: %v", f)
			}
		}
		if f["depth"] == float64(1) {
			childFrame, _ = f["frame"].(string)
			childShort, _ = f["short"].(string)
			if url, _ := f["url"].(string); !strings.Contains(url, "/iframe-inner") {
				t.Fatalf("子 frame URL 不对: %v", f)
			}
			if f["same_origin"] != true || f["accessible"] != true || f["oopif"] != false {
				t.Fatalf("同源 iframe 诊断字段不对: %v", f)
			}
		}
	}
	if childFrame == "" || len(childShort) != targetPrefixMin {
		t.Fatalf("子 frame 身份缺失: %v", m)
	}

	m = okJSON(t, append(a, "map")...)
	if items, _ := m["items"].([]any); len(items) == 0 {
		t.Fatalf("顶层 map 不应为空: %v", m)
	}
	for _, raw := range m["items"].([]any) {
		if it, _ := raw.(map[string]any); it["id"] == "inner-btn" {
			t.Fatalf("默认 map 不应穿透 iframe: %v", it)
		}
	}

	m = okJSON(t, append(a, "map", "--frame", childShort, "--name", "确认")...)
	if m["frame"] != childFrame {
		t.Fatalf("map 应回显完整 frame，实际 %v", m)
	}
	items, _ := m["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("iframe 内应命中 1 个按钮，实际 %v", m)
	}
	it, _ := items[0].(map[string]any)
	innerRef, _ := it["ref"].(string)
	if it["id"] != "inner-btn" || innerRef == "" {
		t.Fatalf("iframe 命中项不对: %v", it)
	}

	okJSON(t, append(a, "click", "--frame", childShort, "label=确认")...)
	wantTrue(t, `document.title==='inner-clicked'`)

	okJSON(t, append(a, "eval", "document.title='reset'")...)
	okJSON(t, append(a, "click", "ref="+innerRef)...)
	wantTrue(t, `document.title==='inner-clicked'`)
}

func TestRefIdentity(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "map")...)
	items, _ := m["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("应枚举 4 个元素，实际 %v", items)
	}
	btn, _ := items[2].(map[string]any)
	ref, _ := btn["ref"].(string)
	if !strings.HasPrefix(ref, "@") {
		t.Fatalf("button#btn 的 ref 应为 @<id>，实际 %q", ref)
	}
	target := "ref=" + ref

	evalRaw(t, `(() => {
	  const el = document.createElement('a');
	  el.href = '#top'; el.textContent = '新插入';
	  document.body.prepend(el);
	  return true;
	})()`)

	m = okJSON(t, append(a, "click", target)...)
	if tg, _ := m["target"].(map[string]any); tg["id"] != "btn" {
		t.Fatalf("插入元素后 %s 应仍命中 button#btn: %v", target, tg)
	}

	usageJSON(t, "invalid target", append(a, "click", "ref=e2")...)
	usageJSON(t, "invalid target", append(a, "click", "ref=2")...)

	loadPage1(t)
	failJSON(t, "is stale", append(a, "click", target)...)
}

func TestEval(t *testing.T) {
	loadPage1(t)

	if v := evalRaw(t, "1+1"); v != float64(2) {
		t.Fatalf("eval 1+1 应为 2，实际 %v", v)
	}
	if v := evalRaw(t, "document.title"); v != "测试首页" {
		t.Fatalf("eval title 应为 测试首页，实际 %v", v)
	}
	wantTrue(t, "Array.isArray([1,2])")
}

func TestEvalNoWait(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	start := time.Now()
	m := okJSON(t, append(a, "eval", "--no-wait",
		`(async()=>{await new Promise(r=>setTimeout(r,3000));return 1})()`)...)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("--no-wait 不应等 Promise，实际耗时 %v", d)
	}
	if _, ok := m["result"]; !ok {
		t.Fatalf("--no-wait 仍应给出 result: %v", m)
	}
	if v := okJSON(t, append(a, "eval", "--no-wait", "1+1")...); v["result"] != float64(2) {
		t.Fatalf("--no-wait 不影响同步表达式: %v", v)
	}
}

func TestEvalTimeout(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)
	failJSON(t, "eval timed out", append(a, "eval", "new Promise(()=>{})")...)
}

func TestClick(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	items := okJSON(t, append(a, "map")...)["items"].([]any)
	ref, _ := items[2].(map[string]any)["ref"].(string)
	m := okJSON(t, append(a, "click", "ref="+ref)...)
	cl, _ := m["target"].(map[string]any)
	if cl["id"] != "btn" || cl["tag"] != "button" {
		t.Fatalf("target 证据应为 button#btn: %v", cl)
	}
	wantTrue(t, `document.getElementById('out').textContent==='按钮已点击'`)

	m = okJSON(t, append(a, "click", "#btn")...)
	if cl, _ = m["target"].(map[string]any); cl["id"] != "btn" {
		t.Fatalf("CSS 定位点击应命中 btn: %v", cl)
	}

	usageJSON(t, "invalid target", append(a, "click", "ref=e99")...)
	usageJSON(t, "invalid target", append(a, "click", "ref=e1x")...)
	failJSON(t, "is stale", append(a, "click", "ref=@zzzz")...)
	failJSON(t, "not found", append(a, "click", "#no-such-element")...)
	usageJSON(t, "label must not be empty", append(a, "click", "label=")...)
}

func TestClickHitTest(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)
	okJSON(t, append(a, "eval", `(() => {
  const o = document.createElement('div');
  o.id = 'veil';
  o.style.cssText = 'position:fixed;inset:0;z-index:9999';
  document.body.appendChild(o);
  return true;
})()`)...)

	failJSON(t, "covered by div#veil", append(a, "click", "#btn")...)
	wantTrue(t, `document.getElementById('out').textContent===''`)

	m := okJSON(t, append(a, "click", "--force", "#btn")...)
	if cl, _ := m["target"].(map[string]any); cl["id"] != "btn" {
		t.Fatalf("--force 也应回目标证据: %v", m)
	}
}

func TestClickByLabel(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/label")...)

	m := okJSON(t, append(a, "click", "label=关闭")...)
	tg, _ := m["target"].(map[string]any)
	if tg["id"] != "close" || tg["label"] != "关闭" {
		t.Fatalf("label=关闭 应命中 button#close: %v", tg)
	}
	wantTrue(t, `document.getElementById('out2').textContent==='已关闭'`)

	m = okJSON(t, append(a, "click", "label=帮助中")...)
	if tg, _ = m["target"].(map[string]any); tg["id"] != "help" || tg["label"] != "帮助中心" {
		t.Fatalf("label=帮助中 应前缀命中 button#help: %v", tg)
	}
	wantTrue(t, `document.getElementById('out2').textContent==='帮助'`)

	m = failJSON(t, "2 same-tier matches", append(a, "click", "label=提交")...)
	lm, _ := m["label"].(map[string]any)
	if lm["tier"] != "exact" || lm["ambiguous_total"] != float64(2) {
		t.Fatalf("歧义应带回候选结构: %v", lm)
	}
	wantTrue(t, `document.getElementById('out2').textContent==='帮助'`)

	m = failJSON(t, "搜索商品", append(a, "click", "label=商品")...)
	lm, _ = m["label"].(map[string]any)
	if sug, _ := lm["suggestions"].([]any); len(sug) != 1 {
		t.Fatalf("子串命中应只出现在 suggestions: %v", lm)
	}

	failJSON(t, "matched nothing", append(a, "click", "label=根本不存在的按钮")...)
}

// Smooth scrolling must settle before coordinates are read.
func TestClickAfterSmoothScroll(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	click := func(target string) {
		t.Helper()
		okJSON(t, append(a, "click", target)...)
		w := okJSON(t, append(a, "wait", "--timeout", "5000", "#home")...)
		if w["url"] != baseURL+"/second" {
			t.Fatalf("%s 单击应直接进入 /second: %v", target, w)
		}
	}
	okJSON(t, append(a, "goto", baseURL+"/smooth")...)
	click("label=more")
	okJSON(t, append(a, "goto", baseURL+"/smooth")...)
	click("#more")

	okJSON(t, append(a, "goto", baseURL+"/smooth")...)
	ref := ""
	if items, _ := okJSON(t, append(a, "map")...)["items"].([]any); len(items) > 0 {
		ref, _ = items[0].(map[string]any)["ref"].(string)
	}
	if ref == "" {
		t.Fatalf("map 应列出 a#more 的 ref")
	}
	click("ref=" + ref)
}

func TestClickNewTab(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/smooth")...)

	m := okJSON(t, append(a, "click", "#more")...)
	if _, ok := m["new_tab"]; ok {
		t.Fatalf("普通导航点击不应带 new_tab: %v", m)
	}
	okJSON(t, append(a, "goto", baseURL+"/smooth")...)

	m = okJSON(t, append(a, "click", "#pop")...)
	nt, _ := m["new_tab"].(map[string]any)
	if nt == nil || nt["url"] != baseURL+"/second" {
		t.Fatalf("应报告 _blank 开出的新标签页: %v", m)
	}
	if idx, _ := nt["index"].(float64); idx < 0 {
		t.Fatalf("new_tab.index 应为有效序号: %v", nt)
	}
	if tg, _ := m["target"].(map[string]any); tg["blank"] != true {
		t.Fatalf("target 应带 blank 证据: %v", m)
	}

	cdpCloseByURL(t, baseURL+"/second")
}

func TestType(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "type", "#box", "你好 tinybk")...)
	if cl, _ := m["target"].(map[string]any); cl["id"] != "box" {
		t.Fatalf("type 应作用到 box: %v", cl)
	}
	wantTrue(t, `document.getElementById('box').value==='你好 tinybk'`)
}

func TestTypeRejectsLabel(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/label")...)

	usageJSON(t, "type does not support label=", append(a, "type", "label=搜索商品", "咖啡")...)
	wantTrue(t, `document.getElementById('q').value===''`)

	m := okJSON(t, append(a, "type", "#q", "咖啡")...)
	if m["value"] != "咖啡" {
		t.Fatalf("CSS 定位输入后 value 应为 咖啡: %v", m)
	}
}

func TestPress(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	okJSON(t, append(a, "type", "#box", "abcd")...)
	okJSON(t, append(a, "press", "--times", "2", "Backspace")...)
	wantTrue(t, `document.getElementById('box').value==='ab'`)

	okJSON(t, append(a, "press", "z")...)
	wantTrue(t, `document.getElementById('box').value==='abz'`)

	m := okJSON(t, append(a, "press", "Enter")...)
	if _, ok := m["new_tab"]; ok {
		t.Fatalf("普通按键不应报 new_tab: %v", m)
	}
}

func TestDragPointer(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/drag")...)

	m := okJSON(t, append(a, "drag", "--steps", "8", "#thumb", "#track")...)
	if m["steps"] != float64(8) {
		t.Fatalf("drag 应回显 steps: %v", m)
	}
	from, _ := m["from"].(map[string]any)
	to, _ := m["to"].(map[string]any)
	if from["id"] != "thumb" || to["id"] != "track" {
		t.Fatalf("drag 源/目标证据不对: %v", m)
	}
	wantTrue(t, `window.__drag.down>=1 && window.__drag.moves>=8 && window.__drag.up>=1`)
	wantTrue(t, `parseFloat(document.getElementById('thumb').style.left)>0`)

	usageJSON(t, "drag does not support label=", append(a, "drag", "label=源", "#track")...)
}

func TestUpload(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/upload")...)

	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(f1, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}

	m := okJSON(t, append(a, "map")...)
	items, _ := m["items"].([]any)
	ref := ""
	for _, raw := range items {
		it, _ := raw.(map[string]any)
		if it["id"] == "visible-file" {
			ref, _ = it["ref"].(string)
		}
	}
	if ref == "" {
		t.Fatalf("map 应包含可见 file input: %v", m)
	}
	m = okJSON(t, append(a, "upload", "ref="+ref, f1, f2)...)
	if m["file_count"] != float64(2) {
		t.Fatalf("upload 应设置 2 个文件: %v", m)
	}
	wantTrue(t, `document.getElementById('visible-file').files.length===2 &&
document.getElementById('visible-file').files[0].name==='a.txt' &&
document.getElementById('visible-file').files[1].name==='b.bin'`)

	m = okJSON(t, append(a, "upload", "#hidden-file", f1)...)
	if m["file_count"] != float64(1) {
		t.Fatalf("隐藏 input 上传应成功: %v", m)
	}
	wantTrue(t, `document.getElementById('hidden-file').files.length===1 &&
document.getElementById('hidden-file').files[0].name==='a.txt'`)

	wantTrue(t, `window.__changes.length>=2 && window.__changes.some((x)=>x.id==='hidden-file'&&x.count===1)`)

	failJSON(t, "is not input[type=file]", append(a, "upload", "#not-file", f1)...)
	failJSON(t, "file is unavailable", append(a, "upload", "#visible-file", filepath.Join(dir, "missing.txt"))...)
	usageJSON(t, "upload does not support label=", append(a, "upload", "label=选择文件", f1)...)
}

func TestPressChords(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)
	okJSON(t, append(a, "eval", `(() => {
  window.__keys = [];
  addEventListener('keydown', (e) => window.__keys.push({
    key: e.key, ctrl: e.ctrlKey, shift: e.shiftKey, alt: e.altKey, meta: e.metaKey
  }));
  return true;
})()`)...)
	okJSON(t, append(a, "click", "#box")...)

	okJSON(t, append(a, "press", "Control+A")...)
	wantTrue(t, `(() => {
  const e = window.__keys.at(-1);
  return e && e.key.toLowerCase() === 'a' && e.ctrl && !e.shift;
})()`)

	okJSON(t, append(a, "eval", "window.__keys=[]")...)
	okJSON(t, append(a, "press", "Shift+Tab")...)
	wantTrue(t, `(() => {
  const e = window.__keys.at(-1);
  return e && e.key === 'Tab' && e.shift;
})()`)

	okJSON(t, append(a, "eval", "window.__keys=[]")...)
	okJSON(t, append(a, "press", "Control+Shift+A")...)
	wantTrue(t, `(() => {
  const e = window.__keys.at(-1);
  return e && e.key === 'A' && e.ctrl && e.shift;
})()`)

	okJSON(t, append(a, "eval", "window.__keys=[]")...)
	okJSON(t, append(a, "press", "F2")...)
	wantTrue(t, `(() => {
  const e = window.__keys.at(-1);
  return e && e.key === 'F2';
})()`)

	okJSON(t, append(a, "eval", "window.__keys=[]")...)
	okJSON(t, append(a, "press", "F12")...)
	wantTrue(t, `(() => {
  const e = window.__keys.at(-1);
  return e && e.key === 'F12';
})()`)

	usageJSON(t, "unknown key", append(a, "press", "Control+Foo")...)
}

func TestPressNewTab(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)
	okJSON(t, append(a, "eval", `(() => {
  const f = document.createElement('form');
  f.action = '/second'; f.method = 'post'; f.target = '_blank';
  const i = document.createElement('input');
  i.id = 'fbox';
  f.appendChild(i);
  document.body.appendChild(f);
  return true;
})()`)...)
	okJSON(t, append(a, "click", "#fbox")...)
	defer cdpCloseByURL(t, baseURL+"/second")

	m := okJSON(t, append(a, "press", "Enter")...)
	nt, ok := m["new_tab"].(map[string]any)
	if !ok {
		t.Fatalf("press 让页面开新页时应回 new_tab: %v", m)
	}
	id, _ := nt["id"].(string)
	if id == "" {
		t.Fatalf("new_tab 应带稳定 id: %v", nt)
	}
	t.Cleanup(func() { cdpCloseTab(t, id) })
}

func TestWait(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "wait", "#out")...)
	if m["action"] != "wait" {
		t.Fatalf("action 应为 wait: %v", m)
	}
	if _, ok := m["waited_ms"]; !ok {
		t.Fatalf("应带 waited_ms: %v", m)
	}

	okJSON(t, append(a, "eval", "document.getElementById('out').style.display='none'")...)
	failJSON(t, "wait for", append(a, "wait", "--timeout", "300", "#out")...)
	okJSON(t, append(a, "eval", "document.getElementById('out').style.display=''")...)
	okJSON(t, append(a, "wait", "--timeout", "2000", "#out")...)

	failJSON(t, "wait for", append(a, "wait", "--timeout", "200", "#绝不存在的元素")...)
}

func TestWaitConditions(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/spa")...)

	okJSON(t, append(a, "click", "#go")...)
	okJSON(t, append(a, "wait", "--timeout", "5000", "--url", "/spa-detail")...)
	w := okJSON(t, append(a, "wait", "--timeout", "5000", "--contains", "详情已加载")...)
	if w["url"] != baseURL+"/spa-detail" {
		t.Fatalf("wait 应落在新 URL: %v", w)
	}
	if ms, _ := w["waited_ms"].(float64); ms < 0 {
		t.Fatalf("waited_ms 应为非负数: %v", w)
	}
	wantTrue(t, `document.getElementById('view').textContent==='详情已加载'`)

	okJSON(t, append(a, "wait", "--gone", "#loading")...)

	okJSON(t, append(a, "goto", baseURL+"/spa")...)
	okJSON(t, append(a, "eval", "setTimeout(() => document.getElementById('loading').remove(), 800); true")...)
	w = okJSON(t, append(a, "wait", "--timeout", "5000", "--gone", "#loading")...)
	if ms, _ := w["waited_ms"].(float64); ms < 600 {
		t.Fatalf("--gone 应等满 800ms 的慢渲染，waited_ms=%v", w)
	}

	okJSON(t, append(a, "goto", baseURL+"/spa")...)
	m := okJSON(t, append(a, "wait", "--stable", "100")...)
	if ms, _ := m["waited_ms"].(float64); ms < 100 {
		t.Fatalf("--stable 100 的 waited_ms 应 >= 100: %v", m)
	}

	usageJSON(t, "Usage", append(a, "wait")...)
	usageJSON(t, "Usage", append(a, "wait", "#view", "--url", "x")...)
	usageJSON(t, "Usage", append(a, "wait", "#view", "#loading")...)
	failJSON(t, "wait for", append(a, "wait", "--timeout", "300", "--contains", "绝不存在的文字")...)
}

// click waits a bounded time for SPA URL and body changes.
func TestClickSettlesSPA(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	okJSON(t, append(a, "goto", baseURL+"/spa")...)

	m := okJSON(t, append(a, "click", "#go")...)
	nav, _ := m["nav"].(map[string]any)
	if nav == nil {
		t.Fatalf("click 应带 nav 证据: %v", m)
	}
	if nav["url_changed"] != true || nav["content_changed"] != true {
		t.Fatalf("应报告 URL 与正文都变了: %v", nav)
	}
	if nav["ready_state"] != "complete" {
		t.Fatalf("返回时页面应已就绪: %v", nav)
	}
	if nav["capped"] == true {
		t.Fatalf("SPA 应在上限内落定: %v", nav)
	}
	if ms, _ := nav["waited_ms"].(float64); ms < 600 {
		t.Fatalf("应等到 800ms 后的渲染再返回，waited_ms=%v", nav)
	}
	if m["url"] != baseURL+"/spa-detail" {
		t.Fatalf("click 应返回新 URL: %v", m)
	}
	if txt, _ := m["text"].(string); !strings.Contains(txt, "详情已加载") {
		t.Fatalf("click 返回的正文应是渲染后的新内容: %q", txt)
	}
}

func TestClickNoNav(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "click", "#btn")...)
	nav, _ := m["nav"].(map[string]any)
	if nav == nil {
		t.Fatalf("click 应带 nav 证据: %v", m)
	}
	if nav["url_changed"] != false || nav["content_changed"] != true {
		t.Fatalf("无导航但正文已改，应报告 url_changed=false/content_changed=true: %v", nav)
	}
	if ms, _ := nav["waited_ms"].(float64); ms > float64(navWindow.Milliseconds()+100) {
		t.Fatalf("无导航点击只应花观察窗，waited_ms=%v", ms)
	}
	wantTrue(t, `document.getElementById('out').textContent==='按钮已点击'`)

	m = okJSON(t, append(a, "click", "#box")...)
	nav, _ = m["nav"].(map[string]any)
	if nav["content_changed"] != false {
		t.Fatalf("无变化的点击不应报告 content_changed=true: %v", nav)
	}
}

// Negative scroll deltas must survive global flag parsing.
func TestScroll(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	okJSON(t, append(a, "scroll", "bottom")...)

	wantTrue(t, "window.scrollY + innerHeight >= document.documentElement.scrollHeight - 1")
	okJSON(t, append(a, "scroll", "top")...)
	wantTrue(t, "window.scrollY===0")
	okJSON(t, append(a, "scroll", "+150")...)
	wantTrue(t, "window.scrollY===150")

	okJSON(t, append(a, "scroll", "-50")...)
	wantTrue(t, "window.scrollY===100")
}

func TestShot(t *testing.T) {
	dir := filepath.Join(tmpDir, "shotcwd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 cwd: %v", err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("读 cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切 cwd: %v", err)
	}
	defer os.Chdir(old)

	checkShot := func(m map[string]any, wantDir, field string) string {
		t.Helper()
		p, _ := m[field].(string)
		if p == "" {
			t.Fatalf("%s 应返回路径: %v", field, m)
		}
		if !filepath.IsAbs(p) {
			t.Fatalf("%s 应为绝对路径: %s", field, p)
		}
		gotDir, gerr := filepath.EvalSymlinks(filepath.Dir(p))
		wantResolved, werr := filepath.EvalSymlinks(wantDir)
		if gerr != nil || werr != nil || gotDir != wantResolved {
			t.Fatalf("%s 应落在 %s，实际 %s", field, wantDir, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("截图文件应存在: %v", err)
		}
		if len(data) < 8 || !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) {
			t.Fatalf("截图应为 PNG: %s (%d bytes)", p, len(data))
		}
		return p
	}

	m := okJSON(t, "--cdp", cdpAddr, "--shot", "see")
	defaultDir := filepath.Join(dir, ".tinybk", "shots")
	checkShot(m, defaultDir, "shot")

	customDir := filepath.Join(dir, "custom-shots")
	m = okJSON(t, "--cdp", cdpAddr, "--shot-dir", customDir, "--shot", "see")
	checkShot(m, customDir, "shot")

	m = okJSON(t, "--cdp", cdpAddr, "shot")
	checkShot(m, defaultDir, "path")
	if n, _ := m["bytes"].(float64); n <= 0 {
		t.Fatalf("shot 应返回正数字节数: %v", m)
	}

	argDir := filepath.Join(dir, "arg-shots")
	m = okJSON(t, "--cdp", cdpAddr, "shot", argDir)
	checkShot(m, argDir, "path")

	globalDir := filepath.Join(dir, "global-shots")
	m = okJSON(t, "--cdp", cdpAddr, "--shot-dir", globalDir, "shot")
	checkShot(m, globalDir, "path")
}

// back uses Go-side polling; bfcache restores do not emit loadEventFired.
func TestNavigateBack(t *testing.T) {
	a := []string{"--cdp", cdpAddr}

	okJSON(t, append(a, "goto", baseURL+"/")...)
	m := okJSON(t, append(a, "click", "#nav")...)
	if cl, _ := m["target"].(map[string]any); cl["href"] != "/second" {
		t.Fatalf("target 证据应带 href: %v", cl)
	}

	if m["url"] != baseURL+"/second" {
		t.Fatalf("click 后应已落在 /second: %v", m)
	}
	if nav, _ := m["nav"].(map[string]any); nav == nil || nav["url_changed"] != true {
		t.Fatalf("导航点击应带 nav.url_changed=true: %v", m)
	}
	if txt, _ := m["text"].(string); !strings.Contains(txt, "第二页") {
		t.Fatalf("click 返回的正文应是第二页: %q", txt)
	}

	m = okJSON(t, append(a, "back")...)
	if m["action"] != "back" {
		t.Fatalf("action 应为 back: %v", m)
	}
	if m["url"] != baseURL+"/" {
		t.Fatalf("back 应回到首页: %v", m)
	}
	wantTrue(t, `document.title==='测试首页'`)
}

func TestTabNew(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)
	mt := okJSON(t, append(a, "tab")...)
	homeID := tabID(t, mt["tabs"].([]any), 0)

	m := okJSON(t, append(a, "tab", "new", baseURL+"/smooth")...)
	c := createdTab(t, m)
	id, _ := c["id"].(string)
	short, _ := c["short"].(string)
	if id == "" {
		t.Fatalf("created 应带稳定 id: %v", m)
	}
	if len(short) != targetPrefixMin || !strings.HasPrefix(id, short) {
		t.Fatalf("created.short 应是 id 前 %d 位: %v", targetPrefixMin, m)
	}
	if c["url"] != baseURL+"/smooth" {
		t.Fatalf("created.url 应为新页地址: %v", m)
	}
	if tabs, _ := m["tabs"].([]any); len(tabs) != 2 {
		t.Fatalf("应共有 2 个标签页: %v", m)
	}

	if v := okJSON(t, append(a, "--target", homeID, "eval", "--no-wait", "document.visibilityState")...); v["result"] != "visible" {
		t.Fatalf("tab new 不应把原页踢到后台: %v", v)
	}
	if v := okJSON(t, append(a, "--target", short, "eval", "--no-wait", "document.visibilityState")...); v["result"] != "hidden" {
		t.Fatalf("tab new 应在后台创建新页: %v", v)
	}

	failJSON(t, "no target was specified", append(a, "see")...)

	m = okJSON(t, append(a, "--target", short, "see")...)
	if m["url"] != baseURL+"/smooth" || m["short"] != short {
		t.Fatalf("--target short 应读到后台新页并回显 short: %v", m)
	}
	okJSON(t, append(a, "--target", short, "wait", "#more")...)

	failJSON(t, "not in the foreground", append(a, "--target", short, "click", "#more")...)
	failJSON(t, "not in the foreground", append(a, "--target", id, "type", "#more", "x")...)
	failJSON(t, "not in the foreground", append(a, "--target", short, "press", "Tab")...)
	am := okJSON(t, append(a, "tab", "activate", short)...)
	if act, _ := am["activated"].(map[string]any); act["id"] != id || am["visibility"] != "visible" {
		t.Fatalf("tab activate 应激活并回报 visible: %v", am)
	}
	m = okJSON(t, append(a, "--target", id, "click", "#more")...)
	if m["short"] != short {
		t.Fatalf("激活后点击应回显同一 short: %v", m)
	}

	m = okJSON(t, append(a, "tab", "new")...)
	c = createdTab(t, m)
	blankID, _ := c["id"].(string)
	blankShort, _ := c["short"].(string)
	if blankID == "" || blankShort == "" || c["url"] != "about:blank" {
		t.Fatalf("不带 url 应开 about:blank: %v", m)
	}
	if tabs, _ := m["tabs"].([]any); len(tabs) != 3 {
		t.Fatalf("应共有 3 个标签页: %v", m)
	}

	usageJSON(t, "Usage", append(a, "tab", "new", "a", "b")...)

	okJSON(t, append(a, "tab", "close", blankShort)...)
	okJSON(t, append(a, "tab", "close", id)...)
	if m := okJSON(t, append(a, "see")...); m["url"] != baseURL+"/" {
		t.Fatalf("关闭后应回到单页默认目标: %v", m)
	}
}

func TestTabClose(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "tab", "new", baseURL+"/second")...)
	c := createdTab(t, m)
	id, _ := c["id"].(string)
	short, _ := c["short"].(string)
	if id == "" || len(short) != targetPrefixMin {
		t.Fatalf("created 应带 id 和 short: %v", m)
	}
	okJSON(t, append(a, "--target", short, "wait", "#home")...)

	m = okJSON(t, append(a, "tab", "close", short)...)
	closed, ok := m["closed"].(map[string]any)
	if !ok || closed["id"] != id || closed["short"] != short || closed["url"] != baseURL+"/second" {
		t.Fatalf("closed 应是被关掉的页: %v", m)
	}
	tabs, _ := m["tabs"].([]any)
	if len(tabs) != 1 || tabURL(t, tabs, 0) != baseURL+"/" {
		t.Fatalf("关掉后应只剩主页: %v", m)
	}
	if m := okJSON(t, append(a, "see")...); m["url"] != baseURL+"/" {
		t.Fatalf("关闭后默认应连回主页: %v", m)
	}

	usageJSON(t, "Usage", append(a, "tab", "close")...)
	failJSON(t, "too short", append(a, "tab", "close", "0")...)
	failJSON(t, "not found", append(a, "tab", "close", "bogus")...)

	m = okJSON(t, append(a, "tab")...)
	tabs, _ = m["tabs"].([]any)
	if len(tabs) != 1 {
		t.Fatalf("收尾时应当只有一个标签页: %v", m)
	}
	failJSON(t, "only one tab remains", append(a, "tab", "close", tabID(t, tabs, 0))...)
}

func TestTab(t *testing.T) {
	a := []string{"--cdp", cdpAddr}
	loadPage1(t)

	m := okJSON(t, append(a, "tab")...)
	tabs, _ := m["tabs"].([]any)
	if len(tabs) != 1 {
		t.Fatalf("初始应只有 1 个标签页，实际 %d", len(tabs))
	}
	homeID := tabID(t, tabs, 0)
	homeShort := tabShort(t, tabs, 0)

	id := cdpNewTab(t, baseURL+"/second")
	defer cdpCloseTab(t, id)
	okJSON(t, append(a, "--target", id, "wait", "#home")...)

	m = okJSON(t, append(a, "tab")...)
	tabs, _ = m["tabs"].([]any)
	if len(tabs) != 2 {
		t.Fatalf("开新 tab 后应有 2 个，实际 %d", len(tabs))
	}
	second, secondShort := "", ""
	for i := range tabs {
		if tabURL(t, tabs, i) == baseURL+"/second" {
			second, secondShort = tabID(t, tabs, i), tabShort(t, tabs, i)
		}
	}
	if second != id {
		t.Fatalf("tab 列表里的 id 应与 CDP 返回的 id 一致: got %q want %q (%v)", second, id, tabs)
	}

	failJSON(t, "no target was specified", append(a, "see")...)

	m = okJSON(t, append(a, "--target", strings.ToLower(homeShort), "see")...)
	if m["url"] != baseURL+"/" || m["short"] != homeShort {
		t.Fatalf("--target homeShort 应读到主页并回显 short: %v", m)
	}
	m = okJSON(t, append(a, "--target", secondShort, "see")...)
	if m["url"] != baseURL+"/second" || m["short"] != secondShort {
		t.Fatalf("--target secondShort 应读到第二页并回显 short: %v", m)
	}

	if am := okJSON(t, append(a, "tab", "activate", homeShort)...); am["visibility"] != "visible" {
		t.Fatalf("激活主页后应 visible: %v", am)
	}
	if m := okJSON(t, append(a, "--target", homeID, "see")...); m["url"] != baseURL+"/" {
		t.Fatalf("激活后 --target 应仍为主页: %v", m)
	}

	usageJSON(t, "mutually exclusive", append(a, "--target", homeID, "--tab", "0", "see")...)
	failJSON(t, "too short", append(a, "--target", "dead", "see")...)
	failJSON(t, "not found", append(a, "--target", "deadbeef", "see")...)
	failJSON(t, "not found", append(a, "--tab", "9", "see")...)
	failJSON(t, "not found", append(a, "tab", "activate", "deadbeef")...)
}

// Same-tab writes serialize through the cross-process file lock.
func TestConcurrentClicksSerialized(t *testing.T) {
	a := []string{"--cdp", cdpAddr, "--lock-timeout", "5000"}
	loadPage1(t)
	okJSON(t, append(a, "eval", `(() => {
  window.__n = 0;
  document.getElementById('btn').addEventListener('click', () => window.__n++);
  return true;
})()`)...)

	const n = 5
	ch := make(chan tinybkRawResult, n)
	for i := 0; i < n; i++ {
		args := append(append([]string{}, a...), "click", "#btn")
		go func(args []string) { ch <- runTinyBKRaw(args...) }(args)
	}
	for i := 0; i < n; i++ {
		r := <-ch
		if r.err != nil {
			t.Fatalf("并发 click 启动失败: %v", r.err)
		}
		if r.code != 0 {
			t.Fatalf("并发 click 应成功，实际 code=%d out=%s", r.code, r.out)
		}
	}
	if v := evalRaw(t, "window.__n"); v != float64(n) {
		t.Fatalf("锁串行化后应收到 %d 次 click，实际 %v", n, v)
	}
}

// Shared reads must not observe torn URL/text state during writes.
func TestConcurrentSeeAtomic(t *testing.T) {
	a := []string{"--cdp", cdpAddr, "--lock-timeout", "5000"}
	okJSON(t, append(a, "goto", baseURL+"/")...)

	stop := make(chan struct{})
	done := make(chan struct{})
	var navigated atomic.Int32
	gotoArgs := func(path string) []string {
		return append(append([]string{}, a...), "goto", baseURL+path)
	}
	go func() {
		defer close(done)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			path := "/"
			if i%2 == 1 {
				path = "/second"
			}
			if r := runTinyBKRaw(gotoArgs(path)...); r.code == 0 {
				navigated.Add(1)
			}
			i++
			time.Sleep(10 * time.Millisecond)
		}
	}()

	const workers, per = 8, 3
	type seeResult struct {
		url, text string
		code      int
		raw       string
	}
	ch := make(chan seeResult, workers*per)
	for w := 0; w < workers; w++ {
		go func() {
			for j := 0; j < per; j++ {
				args := append(append([]string{}, a...), "see")
				r := runTinyBKRaw(args...)
				var m map[string]any
				_ = json.Unmarshal([]byte(r.out), &m)
				url, _ := m["url"].(string)
				text, _ := m["text"].(string)
				ch <- seeResult{url: url, text: text, code: r.code, raw: r.out}
			}
		}()
	}

	torn := 0
	for i := 0; i < workers*per; i++ {
		r := <-ch
		if r.code != 0 {
			t.Fatalf("并发 see 应成功，实际 code=%d out=%s", r.code, r.raw)
		}

		switch r.url {
		case baseURL + "/":
			if strings.Contains(r.text, "回首页") {
				torn++
			}
		case baseURL + "/second":
			if strings.Contains(r.text, "点我") {
				torn++
			}
		}
	}
	close(stop)
	<-done
	if navigated.Load() == 0 {
		t.Fatalf("导航进程没有成功执行任何 goto，用例没有真正制造并发")
	}
	if torn != 0 {
		t.Fatalf("see 出现 %d 次 URL/正文撕裂（锁未生效）", torn)
	}
	okJSON(t, append(a, "goto", baseURL+"/")...)
}

// The browser lock makes the last-tab check and close atomic.
func TestConcurrentCloseLastTabs(t *testing.T) {
	a := []string{"--cdp", cdpAddr, "--lock-timeout", "5000"}
	loadPage1(t)
	_ = createdTab(t, okJSON(t, append(a, "tab", "new")...))

	m := okJSON(t, append(a, "tab")...)
	tabs, _ := m["tabs"].([]any)
	if len(tabs) != 2 {
		t.Fatalf("并发 close 用例应恰好 2 个标签页，实际 %d: %v", len(tabs), m)
	}
	ids := []string{tabID(t, tabs, 0), tabID(t, tabs, 1)}

	ch := make(chan tinybkRawResult, len(ids))
	for _, id := range ids {
		args := append(append([]string{}, a...), "tab", "close", id)
		go func(args []string) { ch <- runTinyBKRaw(args...) }(args)
	}
	ok, fail := 0, 0
	for i := 0; i < len(ids); i++ {
		r := <-ch
		if r.err != nil {
			t.Fatalf("并发 close 启动失败: %v", r.err)
		}
		if r.code == 0 {
			ok++
			continue
		}
		fail++
		if !strings.Contains(r.out, "only one tab remains") && !strings.Contains(r.out, "not found") {
			t.Fatalf("失败方应报只剩一个/不存在，实际 %s", r.out)
		}
	}
	if ok != 1 || fail != 1 {
		t.Fatalf("并发关最后两页应一成一败，实际 ok=%d fail=%d", ok, fail)
	}
	m = okJSON(t, append(a, "tab")...)
	tabs, _ = m["tabs"].([]any)
	if len(tabs) != 1 {
		t.Fatalf("并发关最后两页后应剩 1 个，实际 %d: %v", len(tabs), m)
	}
	okJSON(t, append(a, "goto", baseURL+"/")...)
}

// Two closes of the same id: exactly one succeeds.
func TestConcurrentCloseSameID(t *testing.T) {
	a := []string{"--cdp", cdpAddr, "--lock-timeout", "5000"}
	loadPage1(t)
	id1, _ := createdTab(t, okJSON(t, append(a, "tab", "new")...))["id"].(string)
	id2, _ := createdTab(t, okJSON(t, append(a, "tab", "new")...))["id"].(string)
	if id1 == "" || id2 == "" {
		t.Fatalf("created 应带 id")
	}

	ch := make(chan tinybkRawResult, 2)
	for i := 0; i < 2; i++ {
		args := append(append([]string{}, a...), "tab", "close", id1)
		go func(args []string) { ch <- runTinyBKRaw(args...) }(args)
	}
	ok, fail := 0, 0
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			t.Fatalf("并发 close 启动失败: %v", r.err)
		}
		if r.code == 0 {
			ok++
		} else {
			fail++
			if !strings.Contains(r.out, "not found") {
				t.Fatalf("失败方应报目标不存在，实际 %s", r.out)
			}
		}
	}
	if ok != 1 || fail != 1 {
		t.Fatalf("同一 id 并发 close 应一成一败，实际 ok=%d fail=%d", ok, fail)
	}

	m := okJSON(t, append(a, "tab")...)
	tabs, _ := m["tabs"].([]any)
	if len(tabs) != 2 {
		t.Fatalf("3 个页关 1 个后应剩 2 个，实际 %d: %v", len(tabs), m)
	}
	okJSON(t, append(a, "tab", "close", id2)...)
	okJSON(t, append(a, "goto", baseURL+"/")...)
}

// A long eval holds the exclusive lock; short-timeout input returns lock_busy.
func TestLockBusy(t *testing.T) {
	a := []string{"--cdp", cdpAddr, "--lock-timeout", "5000"}
	loadPage1(t)

	ch := make(chan tinybkRawResult, 1)
	evalArgs := append(append([]string{}, a...), "eval", "new Promise(r => setTimeout(r, 1500))")
	go func() { ch <- runTinyBKRaw(evalArgs...) }()
	time.Sleep(300 * time.Millisecond)

	clickArgs := []string{"--cdp", cdpAddr, "--lock-timeout", "100", "click", "#btn"}
	r := runTinyBKRaw(clickArgs...)
	if r.code != 1 || !strings.Contains(r.out, "lock_busy") {
		t.Fatalf("锁被占用时 click 应报 lock_busy，实际 code=%d out=%s", r.code, r.out)
	}
	if lr := <-ch; lr.code != 0 {
		t.Fatalf("长 eval 应成功，实际 code=%d out=%s", lr.code, lr.out)
	}
}
