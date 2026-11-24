package main

// Wire and view shapes: /json/list entries, tab views, short ids, and common result fields.
// This layer has no dependencies; connection, command, and CLI layers depend on it.

type pageEntry struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// targetPrefixMin is the shortest accepted prefix for --target/activate/close.
// Five hex chars give 20 bits; collisions are rare with few tabs and are reported with candidates.
const targetPrefixMin = 5

// shortIDLen is the short id length in tab output and the minimum prefix accepted by --target.
const shortIDLen = targetPrefixMin

func shortID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

type tabItem struct {
	// Short is the first 5 chars of the id; usable as --target / tab activate / tab close.
	Short string `json:"short"`
	ID    string `json:"id"`
	// Index is only the current /json/list position, not a stable identity; use ID/Short across calls.
	Index int    `json:"index"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// tabItemFromPage is the single construction point for tabItem, keeping id/short/index consistent.
func tabItemFromPage(p pageEntry, index int) tabItem {
	return tabItem{
		ID:    p.ID,
		Short: shortID(p.ID),
		Index: index,
		URL:   p.URL,
		Title: p.Title,
	}
}

func toTabs(pages []pageEntry) []tabItem {
	tabs := make([]tabItem, len(pages))
	for i, p := range pages {
		tabs[i] = tabItemFromPage(p, i)
	}
	return tabs
}

// based holds the two common result fields; commands fill business fields and the runner sets action/short.
type based struct {
	Action string `json:"action"`
	Short  string `json:"short,omitempty"`
}

func (b *based) setAction(a string) { b.Action = a }
func (b *based) setShort(s string)  { b.Short = s }
