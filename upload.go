package main

// File upload: set input[type=file] via DOM.setFileInputFiles, bypassing the native picker.
// Supports hidden inputs and same-origin iframe inputs; label= is not accepted.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
)

type uploadTarget struct {
	Tag      string `json:"tag"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Multiple bool   `json:"multiple,omitempty"`
}

type uploadResult struct {
	based
	Target    uploadTarget `json:"target"`
	Files     []string     `json:"files"`
	FileNames []string     `json:"file_names"`
	FileCount int          `json:"file_count"`
}

func cmdUpload(s *Session, target string, files []string, frame string) (any, error) {
	t, err := parseTarget(target)
	if err != nil {
		return nil, err
	}
	if t.label != "" {
		return nil, usagef("upload does not support label=; use CSS or ref=@<id> for input[type=file]")
	}
	absFiles, err := resolveUploadFiles(files)
	if err != nil {
		return nil, err
	}

	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	objID, err := resolveUploadTarget(ctx, t, frame)
	if err != nil {
		return nil, err
	}
	defer cdpCall(ctx, runtime.ReleaseObjectGroup,
		runtime.ReleaseObjectGroupParams{ObjectGroup: handleGroup})

	infoRaw, err := callOn(ctx, objID, `function () {
  return {
    tag: this.tagName.toLowerCase(),
    id: this.id || '',
    name: this.getAttribute('name') || '',
    type: (this.type || '').toLowerCase(),
    multiple: !!this.multiple
  };
}`)
	if err != nil {
		return nil, err
	}
	var info uploadTarget
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		return nil, err
	}
	if info.Tag != "input" || info.Type != "file" {
		return nil, fail(codeError,
			fmt.Sprintf("upload target is not input[type=file]: got <%s type=%q>", info.Tag, info.Type),
			map[string]any{"target": info})
	}

	if _, err := cdpCall(ctx, dom.SetFileInputFiles, dom.SetFileInputFilesParams{
		ObjectID: objID,
		Files:    absFiles,
	}); err != nil {
		return nil, fmt.Errorf("set input[type=file] files: %w", err)
	}

	stateRaw, err := callOn(ctx, objID, `function () {
  const fs = Array.from(this.files || []);
  return {count: fs.length, names: fs.map((f) => f.name)};
}`)
	if err != nil {
		return nil, err
	}
	var state struct {
		Count int      `json:"count"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		return nil, err
	}
	if state.Count != len(absFiles) {
		return nil, fmt.Errorf("file count mismatch: expected %d, input.files has %d", len(absFiles), state.Count)
	}
	return &uploadResult{
		Target:    info,
		Files:     absFiles,
		FileNames: state.Names,
		FileCount: state.Count,
	}, nil
}

func resolveUploadFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, usagef("upload requires at least one file path")
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("resolve file path %q: %w", p, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("file is unavailable %s: %w", abs, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("not a regular file: %s", abs)
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("file is not readable %s: %w", abs, err)
		}
		f.Close()
		out = append(out, abs)
	}
	return out, nil
}

func resolveUploadTarget(ctx context.Context, t elementTarget, frame string) (runtime.RemoteObjectID, error) {
	if t.bid != 0 {
		return resolveBid(ctx, t.bid)
	}
	if t.css == "" {
		return "", usagef("upload target must be CSS or ref=@<id>")
	}
	if frame == "" {
		no := false
		res, err := cdpCall(ctx, runtime.Evaluate, runtime.EvaluateParams{
			Expression:    `document.querySelector(` + strconv.Quote(t.css) + `)`,
			ObjectGroup:   handleGroup,
			ReturnByValue: &no,
		})
		if err != nil {
			return "", fmt.Errorf("find upload target: %w", err)
		}
		if res.ExceptionDetails != nil {
			return "", fmt.Errorf("find upload target failed: %s", res.ExceptionDetails.Text)
		}
		if res.Result == nil || res.Result.ObjectID == "" {
			return "", fmt.Errorf("selector %q matched no element", t.css)
		}
		return res.Result.ObjectID, nil
	}

	fi, err := resolveFrame(ctx, frame)
	if err != nil {
		return "", err
	}
	winObj, err := frameWindowObject(ctx, cdp.FrameID(fi.Frame))
	if err != nil {
		return "", err
	}
	objID, err := callOnRef(ctx, winObj, `function () {
  const el = this.document.querySelector(`+strconv.Quote(t.css)+`);
  if (!el) throw new Error('CSS selector matched no element: `+t.css+`');
  return el;
}`)
	if err != nil {
		return "", fmt.Errorf("find upload target inside iframe: %w", err)
	}
	return objID, nil
}
