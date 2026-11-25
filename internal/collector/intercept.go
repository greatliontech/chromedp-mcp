package collector

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"

	"github.com/greatliontech/chromedp-mcp/internal/id"
)

// ErrInterceptRuleNotFound is returned when removing a rule ID that does not exist.
var ErrInterceptRuleNotFound = errors.New("intercept rule not found")

// InterceptAction identifies what an intercept rule does to a matching request.
type InterceptAction string

const (
	InterceptActionFulfill InterceptAction = "fulfill"
	InterceptActionModify  InterceptAction = "modify"
	InterceptActionFail    InterceptAction = "fail"
)

// InterceptRule defines a single request interception rule.
type InterceptRule struct {
	ID           string
	URLPattern   string
	Method       string // optional case-insensitive HTTP method filter
	ResourceType string // optional case-insensitive CDP ResourceType filter
	Action       InterceptAction

	// Fulfill fields (used when Action == InterceptActionFulfill).
	Status int
	Body   []byte

	// Modify fields (used when Action == InterceptActionModify).
	URL            string
	OverrideMethod string
	PostData       []byte

	// Headers are response headers for fulfill, or request header overrides for modify.
	Headers map[string]string

	// Fail field (used when Action == InterceptActionFail).
	ErrorReason network.ErrorReason

	// MatchCount records how many paused requests matched this rule.
	MatchCount int64
}

// InterceptRuleInfo is the JSON-serializable summary returned by list_intercept_rules.
type InterceptRuleInfo struct {
	ID             string            `json:"id"`
	URLPattern     string            `json:"url_pattern"`
	Method         string            `json:"method,omitempty"`
	ResourceType   string            `json:"resource_type,omitempty"`
	Action         InterceptAction   `json:"action"`
	Status         int               `json:"status,omitempty"`
	URL            string            `json:"url,omitempty"`
	OverrideMethod string            `json:"override_method,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	ErrorReason    string            `json:"error_reason,omitempty"`
	Matches        int64             `json:"matches"`
}

// Intercept stores ordered per-tab network request interception rules.
// Matching runs synchronously in chromedp's event dispatcher, so all methods
// here are strictly in-memory and never perform CDP I/O while holding mu.
type Intercept struct {
	mu    sync.Mutex
	rules []*InterceptRule
}

// NewIntercept creates an empty per-tab request interception rule store.
func NewIntercept() *Intercept {
	return &Intercept{}
}

// Add appends a defensive copy of rule, assigns a unique ID, and returns that ID.
func (ic *Intercept) Add(rule InterceptRule) string {
	cp := cloneInterceptRule(rule)

	ic.mu.Lock()
	defer ic.mu.Unlock()
	for {
		cand := id.Generate()
		if !ic.hasIDLocked(cand) {
			cp.ID = cand
			break
		}
	}
	ic.rules = append(ic.rules, &cp)
	return cp.ID
}

// Remove deletes the rule with the given ID and reports how many rules remain.
// Returns ErrInterceptRuleNotFound if no rule matches id.
func (ic *Intercept) Remove(ruleID string) (remaining int, err error) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	for i, r := range ic.rules {
		if r.ID == ruleID {
			copy(ic.rules[i:], ic.rules[i+1:])
			ic.rules[len(ic.rules)-1] = nil
			ic.rules = ic.rules[:len(ic.rules)-1]
			return len(ic.rules), nil
		}
	}
	return len(ic.rules), fmt.Errorf("intercept rule %q: %w", ruleID, ErrInterceptRuleNotFound)
}

// Clear removes all rules and returns the number of rules removed.
func (ic *Intercept) Clear() int {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	n := len(ic.rules)
	clear(ic.rules)
	ic.rules = ic.rules[:0]
	return n
}

// Len returns the number of active rules.
func (ic *Intercept) Len() int {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return len(ic.rules)
}

// List returns summaries of all active rules in evaluation order.
func (ic *Intercept) List() []InterceptRuleInfo {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	out := make([]InterceptRuleInfo, 0, len(ic.rules))
	for _, r := range ic.rules {
		info := InterceptRuleInfo{
			ID:             r.ID,
			URLPattern:     r.URLPattern,
			Method:         r.Method,
			ResourceType:   r.ResourceType,
			Action:         r.Action,
			Status:         r.Status,
			URL:            r.URL,
			OverrideMethod: r.OverrideMethod,
			Headers:        maps.Clone(r.Headers),
			ErrorReason:    string(r.ErrorReason),
			Matches:        r.MatchCount,
		}
		out = append(out, info)
	}
	return out
}

// Match returns a defensive copy of the first rule matching ev (and increments
// its MatchCount), or (zero, false) if no rule matches.
func (ic *Intercept) Match(ev *fetch.EventRequestPaused) (InterceptRule, bool) {
	if ev == nil || ev.Request == nil {
		return InterceptRule{}, false
	}
	reqURL := ev.Request.URL
	method := ev.Request.Method
	resType := string(ev.ResourceType)

	ic.mu.Lock()
	defer ic.mu.Unlock()
	for _, r := range ic.rules {
		if r.Method != "" && !strings.EqualFold(r.Method, method) {
			continue
		}
		if r.ResourceType != "" && !strings.EqualFold(r.ResourceType, resType) {
			continue
		}
		if !MatchGlob(r.URLPattern, reqURL) {
			continue
		}
		r.MatchCount++
		return cloneInterceptRule(*r), true
	}
	return InterceptRule{}, false
}

func (ic *Intercept) hasIDLocked(ruleID string) bool {
	for _, r := range ic.rules {
		if r.ID == ruleID {
			return true
		}
	}
	return false
}

func cloneInterceptRule(r InterceptRule) InterceptRule {
	cp := r
	cp.Headers = maps.Clone(r.Headers)
	if r.Body != nil {
		cp.Body = slices.Clone(r.Body)
	}
	if r.PostData != nil {
		cp.PostData = slices.Clone(r.PostData)
	}
	return cp
}

// MatchGlob reports whether s matches pattern using CDP URLPattern-style glob
// wildcards: '*' matches zero or more of any rune (including '/'), '?' matches
// any single rune, and '\' escapes the next rune ('\*', '\?', '\\').
// Runs in O(len(pattern)*len(s)) worst-case time without regex backtracking.
func MatchGlob(pattern, s string) bool {
	type patToken struct {
		r        rune
		wildcard byte // 0 = literal, '*' or '?'
	}
	tokens := make([]patToken, 0, len(pattern))
	pr := []rune(pattern)
	for i := 0; i < len(pr); i++ {
		switch pr[i] {
		case '\\':
			if i+1 < len(pr) {
				i++
				tokens = append(tokens, patToken{r: pr[i]})
			} else {
				tokens = append(tokens, patToken{r: '\\'})
			}
		case '*':
			// Collapse consecutive '*' to keep matching linear on repetitive globs.
			if len(tokens) == 0 || tokens[len(tokens)-1].wildcard != '*' {
				tokens = append(tokens, patToken{wildcard: '*'})
			}
		case '?':
			tokens = append(tokens, patToken{wildcard: '?'})
		default:
			tokens = append(tokens, patToken{r: pr[i]})
		}
	}

	sr := []rune(s)
	pi, si := 0, 0
	starIdx, matchIdx := -1, 0
	for si < len(sr) {
		if pi < len(tokens) && (tokens[pi].wildcard == '?' || (tokens[pi].wildcard == 0 && tokens[pi].r == sr[si])) {
			pi++
			si++
		} else if pi < len(tokens) && tokens[pi].wildcard == '*' {
			starIdx = pi
			matchIdx = si
			pi++
		} else if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
		} else {
			return false
		}
	}
	for pi < len(tokens) && tokens[pi].wildcard == '*' {
		pi++
	}
	return pi == len(tokens)
}

// MergeRequestHeaders merges overrides (case-insensitively) on top of Chrome's
// original paused request headers and returns a deterministically sorted slice
// for Fetch.continueRequest. Preserving unmodified headers prevents CDP's
// full-replacement Headers parameter from stripping Accept, Cookie, or
// Content-Type when adding or overriding individual headers. Multi-value CDP
// request headers joined with '\n' are split into separate HeaderEntry items
// unless overridden so Chromium's header value validator never rejects them.
func MergeRequestHeaders(orig network.Headers, overrides map[string]string) []*fetch.HeaderEntry {
	type headerValues struct {
		name   string
		values []string
	}
	byLower := make(map[string]headerValues, len(orig)+len(overrides))

	origKeys := make([]string, 0, len(orig))
	for k := range orig {
		origKeys = append(origKeys, k)
	}
	slices.Sort(origKeys)
	for _, k := range origKeys {
		v := orig[k]
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprint(v)
		}
		parts := strings.Split(s, "\n")
		vals := make([]string, 0, len(parts))
		for _, p := range parts {
			vals = append(vals, strings.TrimSuffix(p, "\r"))
		}
		byLower[strings.ToLower(k)] = headerValues{name: k, values: vals}
	}

	overrideKeys := make([]string, 0, len(overrides))
	for k := range overrides {
		overrideKeys = append(overrideKeys, k)
	}
	slices.Sort(overrideKeys)
	for _, k := range overrideKeys {
		byLower[strings.ToLower(k)] = headerValues{name: k, values: []string{overrides[k]}}
	}

	out := make([]*fetch.HeaderEntry, 0, len(byLower))
	for _, hv := range byLower {
		for _, val := range hv.values {
			out = append(out, &fetch.HeaderEntry{Name: hv.name, Value: val})
		}
	}
	sortHeaderEntries(out)
	return out
}

// HeaderEntriesFromMap converts a header map to a deterministically sorted
// slice for Fetch.fulfillRequest.
func HeaderEntriesFromMap(headers map[string]string) []*fetch.HeaderEntry {
	if len(headers) == 0 {
		return nil
	}
	out := make([]*fetch.HeaderEntry, 0, len(headers))
	for k, v := range headers {
		out = append(out, &fetch.HeaderEntry{Name: k, Value: v})
	}
	sortHeaderEntries(out)
	return out
}

func sortHeaderEntries(entries []*fetch.HeaderEntry) {
	// Sort stably by header name only so multi-value field lines keep their
	// original relative wire order (RFC 9110 §5.3).
	slices.SortStableFunc(entries, func(a, b *fetch.HeaderEntry) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
}
