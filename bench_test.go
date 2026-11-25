package main

// Runtime benchmark MVP. It measures the real tinybk binary against the local
// HTTP fixtures and headless Chrome provided by TestMain in e2e_test.go.
//
// Run explicitly:
//
//BK_BENCH=1 go test -run '^TestRuntimeBenchmark$' -count=1 -v
//
// Optional:
//
//BK_BENCH_ITERS=20 BK_BENCH_WARMUP=3

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type benchIteration struct {
	duration    time.Duration
	outputBytes int
	success     bool
	correct     bool
}

type benchScenarioResult struct {
	Name            string  `json:"name"`
	Iterations      int     `json:"iterations"`
	CallsPerRun     int     `json:"calls_per_run"`
	P50MS           float64 `json:"p50_ms"`
	P95MS           float64 `json:"p95_ms"`
	MeanMS          float64 `json:"mean_ms"`
	AvgOutputBytes  float64 `json:"avg_output_bytes"`
	SuccessRate     float64 `json:"success_rate"`
	CorrectnessRate float64 `json:"correctness_rate"`
}

type benchReport struct {
	GeneratedAt   string                `json:"generated_at"`
	TinyBKCommit  string                `json:"tinybk_commit"`
	TinyBKVersion string                `json:"tinybk_version"`
	Chrome        string                `json:"chrome"`
	OS            string                `json:"os"`
	Arch          string                `json:"arch"`
	Iterations    int                   `json:"iterations"`
	Warmup        int                   `json:"warmup"`
	Scenarios     []benchScenarioResult `json:"scenarios"`
}

func TestRuntimeBenchmark(t *testing.T) {
	if os.Getenv("BK_BENCH") == "" {
		t.Skip("set BK_BENCH=1 to run the runtime benchmark")
	}

	iterations := envPositiveInt(t, "BK_BENCH_ITERS", 20)
	warmup := envNonNegativeInt(t, "BK_BENCH_WARMUP", 3)

	scenarios := []struct {
		name        string
		callsPerRun int
		run         func(t *testing.T) benchIteration
	}{
		{name: "see", callsPerRun: 1, run: benchSee},
		{name: "map", callsPerRun: 1, run: benchMap},
		{name: "type", callsPerRun: 1, run: benchType},
		{name: "map_click_see", callsPerRun: 3, run: benchMapClickSee},
	}

	report := benchReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Chrome:      benchChromeVersion(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Iterations:  iterations,
		Warmup:      warmup,
	}
	report.TinyBKCommit = benchGitCommit()
	report.TinyBKVersion = benchBinaryVersion(t)

	var table strings.Builder
	table.WriteString("\n| Scenario | Calls/run | p50 ms | p95 ms | mean ms | avg output B | success | correct |\n")
	table.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|\n")

	for _, sc := range scenarios {
		for i := 0; i < warmup; i++ {
			prepareBenchPage(t)
			_ = sc.run(t)
		}

		runs := make([]benchIteration, 0, iterations)
		for i := 0; i < iterations; i++ {
			prepareBenchPage(t)
			runs = append(runs, sc.run(t))
		}

		res := summarizeBenchScenario(sc.name, sc.callsPerRun, runs)
		report.Scenarios = append(report.Scenarios, res)
		fmt.Fprintf(&table, "| %s | %d | %.2f | %.2f | %.2f | %.0f | %.1f%% | %.1f%% |\n",
			res.Name, res.CallsPerRun, res.P50MS, res.P95MS, res.MeanMS,
			res.AvgOutputBytes, res.SuccessRate*100, res.CorrectnessRate*100)
	}

	t.Log(table.String())
	if err := writeBenchReport(report); err != nil {
		t.Errorf("write bench-results.json: %v", err)
	} else {
		t.Log("wrote bench-results.json")
	}
}

func prepareBenchPage(t *testing.T) {
	t.Helper()
	okJSON(t, "--cdp", cdpAddr, "goto", baseURL+"/")
}

func benchSee(t *testing.T) benchIteration {
	t.Helper()
	out, dur, code := timedTinyBK(t, "--cdp", cdpAddr, "see")
	var m struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	parsed := json.Unmarshal([]byte(out), &m) == nil
	correct := code == 0 && parsed && m.URL == baseURL+"/" && strings.Contains(m.Text, "测试首页")
	return benchIteration{duration: dur, outputBytes: len(out), success: code == 0, correct: correct}
}

func benchMap(t *testing.T) benchIteration {
	t.Helper()
	out, dur, code := timedTinyBK(t, "--cdp", cdpAddr, "map")
	_, ok := parseBenchMap(out)
	return benchIteration{duration: dur, outputBytes: len(out), success: code == 0, correct: code == 0 && ok}
}

func benchType(t *testing.T) benchIteration {
	t.Helper()
	out, dur, code := timedTinyBK(t, "--cdp", cdpAddr, "type", "#box", "benchmark")
	var m struct {
		Value string `json:"value"`
	}
	parsed := json.Unmarshal([]byte(out), &m) == nil
	correct := code == 0 && parsed && m.Value == "benchmark"
	if code == 0 {
		// Verify against the page, outside the timed command.
		got := okJSON(t, "--cdp", cdpAddr, "eval", "document.getElementById('box').value")["result"]
		correct = correct && got == "benchmark"
	}
	return benchIteration{duration: dur, outputBytes: len(out), success: code == 0, correct: correct}
}

func benchMapClickSee(t *testing.T) benchIteration {
	t.Helper()
	var total time.Duration
	var outputBytes int

	mapOut, d, code := timedTinyBK(t, "--cdp", cdpAddr, "map")
	total += d
	outputBytes += len(mapOut)
	ref, ok := parseBenchMap(mapOut)
	if code != 0 || !ok {
		return benchIteration{duration: total, outputBytes: outputBytes, success: false, correct: false}
	}

	clickOut, d, code := timedTinyBK(t, "--cdp", cdpAddr, "click", "ref="+ref)
	total += d
	outputBytes += len(clickOut)
	if code != 0 {
		return benchIteration{duration: total, outputBytes: outputBytes, success: false, correct: false}
	}

	seeOut, d, code := timedTinyBK(t, "--cdp", cdpAddr, "see")
	total += d
	outputBytes += len(seeOut)
	var m struct {
		Text string `json:"text"`
	}
	parsed := json.Unmarshal([]byte(seeOut), &m) == nil
	correct := code == 0 && parsed && strings.Contains(m.Text, "按钮已点击")
	if code == 0 {
		got := okJSON(t, "--cdp", cdpAddr, "eval", "document.getElementById('out').textContent")["result"]
		correct = correct && got == "按钮已点击"
	}
	return benchIteration{
		duration:    total,
		outputBytes: outputBytes,
		success:     code == 0,
		correct:     correct,
	}
}

func parseBenchMap(out string) (string, bool) {
	var m struct {
		Items []struct {
			ID  string `json:"id"`
			Ref string `json:"ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil || len(m.Items) != 4 {
		return "", false
	}
	for _, item := range m.Items {
		if item.ID == "btn" && item.Ref != "" {
			return item.Ref, true
		}
	}
	return "", false
}

func timedTinyBK(t *testing.T, args ...string) (string, time.Duration, int) {
	t.Helper()
	start := time.Now()
	out, code := runTinyBK(t, args...)
	return out, time.Since(start), code
}

func summarizeBenchScenario(name string, callsPerRun int, runs []benchIteration) benchScenarioResult {
	durations := make([]float64, len(runs))
	bytes := 0
	success, correct := 0, 0
	for i, run := range runs {
		durations[i] = float64(run.duration) / float64(time.Millisecond)
		bytes += run.outputBytes
		if run.success {
			success++
		}
		if run.correct {
			correct++
		}
	}
	return benchScenarioResult{
		Name:            name,
		Iterations:      len(runs),
		CallsPerRun:     callsPerRun,
		P50MS:           percentile(durations, 0.50),
		P95MS:           percentile(durations, 0.95),
		MeanMS:          mean(durations),
		AvgOutputBytes:  float64(bytes) / float64(len(runs)),
		SuccessRate:     float64(success) / float64(len(runs)),
		CorrectnessRate: float64(correct) / float64(len(runs)),
	}
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func envPositiveInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		t.Fatalf("%s must be a positive integer, got %q", name, raw)
	}
	return n
}

func envNonNegativeInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		t.Fatalf("%s must be a non-negative integer, got %q", name, raw)
	}
	return n
}

func benchChromeVersion() string {
	v, err := browserProduct(cdpAddr)
	if err != nil {
		return "unknown"
	}
	return v
}

func benchBinaryVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(tinybkBin, "--version").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func benchGitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func writeBenchReport(report benchReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile("bench-results.json", data, 0o644)
}
