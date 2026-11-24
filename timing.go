package main

// Action timeouts: every page state fails by timeout rather than hanging forever.
// Values are shared by the element, connection, and command layers.

import "time"

const (
	navTimeout   = 30 * time.Second // page loads such as goto/back
	evalTimeout  = 10 * time.Second // other evaluations and clicks
	obsTimeout   = 5 * time.Second  // observation after an action, with its own budget
	activateWait = 2 * time.Second  // wait for visibilityState=visible after activation
)
