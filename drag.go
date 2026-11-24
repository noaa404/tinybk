package main

// Mouse drag: press at the source center, move in steps, and release at the target center.
// The MVP supports pointer/mouse drag only; HTML5 DataTransfer and file drop are not supported.

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/chromedp/cdproto/input"
)

type dragResult struct {
	Observation
	From    elemInfo `json:"from"`
	To      elemInfo `json:"to"`
	Steps   int      `json:"steps"`
	MovedPx float64  `json:"moved_px"`
}

func cmdDrag(s *Session, from, to string, steps int, o obsOpts) (any, error) {
	if steps <= 0 {
		return nil, usagef("--steps must be a positive integer")
	}
	fromT, err := parseTarget(from)
	if err != nil {
		return nil, err
	}
	toT, err := parseTarget(to)
	if err != nil {
		return nil, err
	}
	if fromT.label != "" || toT.label != "" {
		return nil, usagef("drag does not support label=; use CSS or ref=@<id> for source and target")
	}
	if err := s.requireInputTarget(); err != nil {
		return nil, err
	}

	// drag requires source and target to be visible together; do not scroll one away while locating the other.
	src, err := locateWithScroll(s, fromT, false, "", false)
	if err != nil {
		return nil, err
	}
	dst, err := locateWithScroll(s, toT, false, "", false)
	if err != nil {
		return nil, err
	}
	if !src.Hit {
		return nil, fmt.Errorf("drag source is covered or not visible: %s", src.Blocker)
	}
	if !dst.Hit {
		return nil, fmt.Errorf("drag target is covered or not visible: %s", dst.Blocker)
	}
	dx, dy := dst.X-src.X, dst.Y-src.Y
	moved := math.Hypot(dx, dy)
	if moved < 1 {
		return nil, usagef("drag source and target centers overlap; no distance to drag")
	}

	before := pageIDs(s.cdpURL)
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	st := s.snapshot(ctx)
	if err := dispatchDragMouse(ctx, input.DispatchMouseEventTypeMouseMoved, src.X, src.Y, input.MouseButtonNone, 0, 0); err != nil {
		return nil, fmt.Errorf("drag move to source: %w", err)
	}
	if err := dispatchDragMouse(ctx, input.DispatchMouseEventTypeMousePressed, src.X, src.Y, input.MouseButtonLeft, 1, 1); err != nil {
		return nil, fmt.Errorf("drag press: %w", err)
	}
	for i := 1; i <= steps; i++ {
		x := src.X + dx*float64(i)/float64(steps)
		y := src.Y + dy*float64(i)/float64(steps)
		if err := dispatchDragMouse(ctx, input.DispatchMouseEventTypeMouseMoved, x, y, input.MouseButtonLeft, 1, 0); err != nil {
			return nil, fmt.Errorf("drag move step %d: %w", i, err)
		}
		time.Sleep(12 * time.Millisecond)
	}
	if err := dispatchDragMouse(ctx, input.DispatchMouseEventTypeMouseReleased, dst.X, dst.Y, input.MouseButtonLeft, 0, 1); err != nil {
		return nil, fmt.Errorf("drag release: %w", err)
	}

	nav := s.settleAfter(st)
	res := &dragResult{
		Observation: s.observeAfterAction(o),
		From:        src.elemInfo,
		To:          dst.elemInfo,
		Steps:       steps,
		MovedPx:     moved,
	}
	res.Nav = nav
	if before != nil {
		res.NewTab = newTabAfter(ctx, s.cdpURL, before, 0)
	}
	return res, nil
}

func dispatchDragMouse(ctx context.Context, typ input.DispatchMouseEventType, x, y float64, button input.MouseButton, buttons, clickCount int64) error {
	_, err := cdpCall(ctx, input.DispatchMouseEvent, input.DispatchMouseEventParams{
		Type:       typ,
		X:          x,
		Y:          y,
		Button:     button,
		Buttons:    buttons,
		ClickCount: clickCount,
	})
	return err
}
