package main

// Global CLI configuration and observation options. Kept independent of the connection and command layers.

import "time"

const defaultCDP = "http://127.0.0.1:9222"

const (
	defaultTextLimit   = 1200
	defaultLockTimeout = 30000
)

type globals struct {
	cdpURL      string
	target      string // Stable tab id or a prefix of at least 5 characters.
	tab         int    // Tab index in the current /json/list order (not stable).
	tabSet      bool
	textLimit   int
	lockTimeout int // Lock wait timeout in milliseconds; 0 means no wait.
	shot        bool
	shotDir     string
	help        bool
	showVersion bool
}

func (g globals) lockWait() time.Duration {
	return time.Duration(g.lockTimeout) * time.Millisecond
}

func (g globals) obs() obsOpts {
	return obsOpts{TextLimit: g.textLimit, Shot: g.shot, ShotDir: g.shotDir}
}

// Observation trimming options from --text, --shot, and --shot-dir.
type obsOpts struct {
	TextLimit int
	Shot      bool
	ShotDir   string
}
