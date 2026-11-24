package main

// Minimal iframe support: same-origin frames only. A frame is a page scope, not a tab;
// refs remain node identities, and click coordinates are translated through the iframe chain.

import (
	"context"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
)

type frameItem struct {
	Frame      string `json:"frame"`
	Short      string `json:"short"`
	Parent     string `json:"parent,omitempty"`
	Depth      int    `json:"depth"`
	URL        string `json:"url,omitempty"`
	Origin     string `json:"origin,omitempty"`
	Name       string `json:"name,omitempty"`
	SameOrigin bool   `json:"same_origin"`
	OOPIF      bool   `json:"oopif"`
	Accessible bool   `json:"accessible"`
}

type framesResult struct {
	based
	Frames []frameItem `json:"frames"`
}

func cmdFrames(s *Session) (any, error) {
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	frames, err := listFrames(ctx)
	if err != nil {
		return nil, fmt.Errorf("read frame tree: %w", err)
	}
	enrichFrameDiagnostics(s, ctx, frames)
	return &framesResult{Frames: frames}, nil
}

func listFrames(ctx context.Context) ([]frameItem, error) {
	res, err := cdpCall(ctx, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return nil, err
	}
	if res.FrameTree == nil || res.FrameTree.Frame == nil {
		return nil, fmt.Errorf("page has no frame tree")
	}
	var out []frameItem
	var walk func(*page.FrameTree, string, int)
	walk = func(ft *page.FrameTree, parent string, depth int) {
		f := ft.Frame
		out = append(out, frameItem{
			Frame:  string(f.ID),
			Short:  shortID(string(f.ID)),
			Parent: parent,
			Depth:  depth,
			URL:    f.URL,
			Origin: f.SecurityOrigin,
			Name:   f.Name,
		})
		for _, child := range ft.ChildFrames {
			walk(child, string(f.ID), depth+1)
		}
	}
	walk(res.FrameTree, "", 0)
	return out, nil
}

func resolveFrame(ctx context.Context, input string) (frameItem, error) {
	frames, err := listFrames(ctx)
	if err != nil {
		return frameItem{}, err
	}
	if input == "top" {
		if len(frames) == 0 {
			return frameItem{}, fmt.Errorf("page has no frame")
		}
		return frames[0], nil
	}
	var exact []frameItem
	var prefix []frameItem
	want := strings.ToUpper(input)
	for _, f := range frames {
		if f.Frame == input {
			exact = append(exact, f)
		}
		if strings.HasPrefix(strings.ToUpper(f.Frame), want) {
			prefix = append(prefix, f)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(prefix) == 1 && len(input) >= targetPrefixMin {
		return prefix[0], nil
	}
	if len(prefix) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "frame prefix %q matches %d frames; use a longer prefix: ", input, len(prefix))
		for i, f := range prefix {
			if i > 0 {
				b.WriteString("；")
			}
			fmt.Fprintf(&b, "%s %s", f.Frame, f.URL)
		}
		return frameItem{}, fail(codeFrameAmbiguous, b.String(), map[string]any{"frames": prefix})
	}
	return frameItem{}, fail(codeFrameNotFound,
		fmt.Sprintf("frame %q not found; run tinybk frames to get frame/short", input),
		map[string]any{"frames": frames})
}

func enrichFrameDiagnostics(s *Session, ctx context.Context, frames []frameItem) {
	if len(frames) == 0 {
		return
	}
	topOrigin := frames[0].Origin
	oopif := detectOOPIFTargets(ctx, s, frames)
	for i := range frames {
		f := &frames[i]
		f.SameOrigin = f.Depth == 0
		if f.Depth > 0 {
			f.SameOrigin = topOrigin != "" && topOrigin != "null" && f.Origin == topOrigin
		}
		// accessible reports whether this MVP (top page or same-origin iframe) can enumerate/click the frame.
		f.Accessible = f.Depth == 0 || f.SameOrigin
		f.OOPIF = oopif[cdp.FrameID(f.Frame)]
	}
}

// detectOOPIFTargets is a best-effort diagnostic: an OOPIF appears as a separate target of type iframe.
// Match by targetId==frameId first, otherwise by parent frame + URL.
func detectOOPIFTargets(ctx context.Context, s *Session, frames []frameItem) map[cdp.FrameID]bool {
	out := map[cdp.FrameID]bool{}
	res, err := cdp.Call(ctx, s.browser, target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		return out
	}
	for _, info := range res.TargetInfos {
		if info == nil || info.Type != "iframe" {
			continue
		}
		if string(info.TargetID) != "" {
			for _, f := range frames {
				if f.Frame == string(info.TargetID) {
					out[cdp.FrameID(f.Frame)] = true
				}
			}
		}
		parentID := string(info.ParentFrameID)
		var matches []frameItem
		for _, f := range frames[1:] {
			if parentID != "" && f.Parent != parentID {
				continue
			}
			if info.URL != "" && f.URL != info.URL {
				continue
			}
			matches = append(matches, f)
		}
		if len(matches) == 1 {
			out[cdp.FrameID(matches[0].Frame)] = true
		}
	}
	return out
}

// frameWindowObject gets the iframe element via DOM.getFrameOwner, then its contentWindow.
// Cross-origin frames are rejected here; OOPIF child targets are future work.
func frameWindowObject(ctx context.Context, frameID cdp.FrameID) (runtime.RemoteObjectID, error) {
	owner, err := cdpCall(ctx, dom.GetFrameOwner, dom.GetFrameOwnerParams{FrameID: frameID})
	if err != nil {
		return "", fmt.Errorf("get iframe owner node: %w", err)
	}
	if owner.BackendNodeID == 0 {
		return "", fmt.Errorf("frame %s has no owner node", frameID)
	}
	res, err := cdpCall(ctx, dom.ResolveNode, dom.ResolveNodeParams{
		BackendNodeID: owner.BackendNodeID,
		ObjectGroup:   handleGroup,
	})
	if err != nil {
		return "", fmt.Errorf("resolve iframe node: %w", err)
	}
	if res.Object == nil || res.Object.ObjectID == "" {
		return "", fmt.Errorf("iframe owner node is no longer valid")
	}
	fn := `function () {
  try {
    const w = this.contentWindow;
    if (!w) return null;
    // Trigger the same-origin check; cross-origin access throws SecurityError here.
    void w.document.documentElement;
    return w;
  } catch (e) {
    return null;
  }
}`
	win, err := callOnRef(ctx, res.Object.ObjectID, fn)
	if err != nil {
		return "", fmt.Errorf("frame is not accessible (this MVP supports same-origin iframes only): %w", err)
	}
	return win, nil
}
