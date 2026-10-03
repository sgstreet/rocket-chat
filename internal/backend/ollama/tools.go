package ollama

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

const (
	toolWebSearch = "web_search"
	toolWebFetch  = "web_fetch"
)

var webTools = api.Tools{
	tool(toolWebSearch, "Search the web for current information. Returns numbered results with title, URL and a content excerpt.",
		"query", "The search query"),
	tool(toolWebFetch, "Fetch the text of a web page by URL. Use it to read a search result in full.",
		"url", "The URL to fetch"),
}

func tool(name, description, param, paramDescription string) api.Tool {
	props := api.NewToolPropertiesMap()
	props.Set(param, api.ToolProperty{Type: api.PropertyType{"string"}, Description: paramDescription})
	return api.Tool{
		Type: "function",
		Function: api.ToolFunction{
			Name:        name,
			Description: description,
			Parameters:  api.ToolFunctionParameters{Type: "object", Required: []string{param}, Properties: props},
		},
	}
}

// searchPrompt tells the model when to use the tools. Citation instructions
// are not here but in the tool results (citeHint): in the system prompt they
// lead small models to answer with made-up [1] markers instead of searching.
func searchPrompt(now time.Time) string {
	return "You have two tools: web_search searches the web and web_fetch reads a web page. " +
		"For current events, recent facts or anything you are not sure about, call web_search first " +
		"and answer only after you have the results. Today's date is " + now.Format("2006-01-02") + "."
}

// citeHint ends every tool result that contains numbered sources.
const citeHint = "When you use these sources, cite them inline by number in square brackets, for example [1]. " +
	"Only cite numbers that appear in tool results."

// searchTurn tracks the sources gathered while answering one request, so
// the same URL keeps the same [n] number throughout.
type searchTurn struct {
	settings SearchSettings
	searcher webSearcher
	sources  []chat.Source
	byURL    map[string]int
	queries  []string
}

func newSearchTurn(s SearchSettings, searcher webSearcher) *searchTurn {
	return &searchTurn{settings: s, searcher: searcher, byURL: map[string]int{}}
}

// source registers a source and returns its 1-based number.
func (t *searchTurn) source(s chat.Source) int {
	if n, ok := t.byURL[s.URL]; ok {
		if t.sources[n-1].Title == "" {
			t.sources[n-1].Title = s.Title
		}
		return n
	}
	t.sources = append(t.sources, s)
	n := len(t.sources)
	t.byURL[s.URL] = n
	return n
}

// call runs one tool call and returns the text for the tool message. Errors
// the model can work around are returned as text for the model; errors it
// cannot (cancellation, missing credentials) end the reply.
func (t *searchTurn) call(ctx context.Context, name string, args api.ToolCallFunctionArguments) (string, error) {
	var (
		text string
		err  error
	)
	switch name {
	case toolWebSearch:
		text, err = t.search(ctx, stringArg(args, "query"))
	case toolWebFetch:
		text, err = t.fetch(ctx, stringArg(args, "url"))
	default:
		return fmt.Sprintf("Unknown tool %q. Available tools: %s, %s.", name, toolWebSearch, toolWebFetch), nil
	}
	if err == nil {
		return text, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(err, errSearchAuth) {
		return "", err
	}
	return fmt.Sprintf("The %s tool failed: %v", name, err), nil
}

func (t *searchTurn) search(ctx context.Context, query string) (string, error) {
	if query == "" {
		return "The query argument is required.", nil
	}
	t.queries = append(t.queries, query)
	results, err := t.searcher.search(ctx, query, t.settings.MaxResults)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No results for " + strconv.Quote(query) + ".", nil
	}
	var b strings.Builder
	for _, r := range results {
		content := strings.TrimSpace(r.Content)
		if len(content) > t.settings.MaxResultChars {
			content = truncateUTF8(content, t.settings.MaxResultChars) + " [truncated; use web_fetch for the full page]"
		}
		n := t.source(chat.Source{Title: r.Title, URL: r.URL, Snippet: content})
		fmt.Fprintf(&b, "[%d] %s\nURL: %s\n%s\n\n", n, r.Title, r.URL, content)
	}
	return b.String() + citeHint, nil
}

func (t *searchTurn) fetch(ctx context.Context, url string) (string, error) {
	if url == "" {
		return "The url argument is required.", nil
	}
	page, err := t.searcher.fetch(ctx, url)
	if err != nil {
		return "", err
	}
	n := t.source(chat.Source{Title: page.Title, URL: url})
	content := page.Content
	if len(content) > t.settings.MaxFetchChars {
		content = truncateUTF8(content, t.settings.MaxFetchChars) + "\n[truncated]"
	}
	return fmt.Sprintf("[%d] %s\nURL: %s\n\n%s\n\n%s", n, page.Title, url, content, citeHint), nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

var citation = regexp.MustCompile(`\[(\d+(?:\s*,\s*\d+)*)\]`)

// grounding returns the sources, marking those the answer cites.
func (t *searchTurn) grounding(answer string) *chat.Grounding {
	if len(t.sources) == 0 && len(t.queries) == 0 {
		return nil
	}
	sources := append([]chat.Source(nil), t.sources...)
	for _, m := range citation.FindAllStringSubmatch(answer, -1) {
		for _, num := range strings.Split(m[1], ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(num)); err == nil && n >= 1 && n <= len(sources) {
				sources[n-1].Cited = true
			}
		}
	}
	return &chat.Grounding{Sources: sources, Queries: t.queries}
}
