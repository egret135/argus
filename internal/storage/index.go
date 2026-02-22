package storage

import (
	"regexp"
	"strings"

	"github.com/admin/argus/internal/model"
)

var hexPattern = regexp.MustCompile(`^[0-9a-f]{16,}$`)

var stopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {},
	"do": {}, "does": {}, "did": {}, "will": {}, "would": {}, "could": {},
	"should": {}, "may": {}, "might": {}, "shall": {}, "can": {}, "to": {},
	"of": {}, "in": {}, "for": {}, "on": {}, "with": {}, "at": {}, "by": {},
	"from": {}, "as": {}, "into": {}, "through": {}, "during": {}, "before": {},
	"after": {}, "above": {}, "below": {}, "between": {}, "out": {}, "off": {},
	"over": {}, "under": {}, "again": {}, "further": {}, "then": {}, "once": {},
	"it": {}, "its": {}, "this": {}, "that": {}, "these": {}, "those": {},
	"and": {}, "but": {}, "or": {}, "nor": {}, "not": {}, "so": {}, "if": {},
}

// IndexSnapshot is an immutable inverted index safe for concurrent reads.
type IndexSnapshot struct {
	Inverted  map[string][]uint64
	LevelIdx  map[string][]uint64
	SourceIdx map[string][]uint64
	TailSeq   uint64
}

// BuildIndex constructs an IndexSnapshot from a slice of entries.
// Entries are assumed to be in ascending seqID order.
func BuildIndex(entries []model.EntryLite, tailSeq uint64) *IndexSnapshot {
	idx := &IndexSnapshot{
		Inverted:  make(map[string][]uint64),
		LevelIdx:  make(map[string][]uint64),
		SourceIdx: make(map[string][]uint64),
		TailSeq:   tailSeq,
	}

	for i := range entries {
		e := &entries[i]
		seq := e.SeqID

		for _, tok := range tokenize(e.Message) {
			idx.Inverted[tok] = append(idx.Inverted[tok], seq)
		}

		if e.Level != "" {
			idx.LevelIdx[e.Level] = append(idx.LevelIdx[e.Level], seq)
		}
		if e.Source != "" {
			idx.SourceIdx[e.Source] = append(idx.SourceIdx[e.Source], seq)
		}
	}

	return idx
}

// Search performs a two-phase search: intersect keyword posting lists,
// then intersect with level and source filters. Returns seqIDs in ascending order.
func (idx *IndexSnapshot) Search(query string, level string, source string) []uint64 {
	var result []uint64

	if query != "" {
		tokens := tokenize(query)
		if len(tokens) == 0 {
			return nil
		}
		result = idx.Inverted[tokens[0]]
		for _, tok := range tokens[1:] {
			result = intersect(result, idx.Inverted[tok])
			if len(result) == 0 {
				return nil
			}
		}
	}

	if level != "" {
		list := idx.LevelIdx[level]
		if result == nil {
			result = list
		} else {
			result = intersect(result, list)
		}
		if len(result) == 0 {
			return nil
		}
	}

	if source != "" {
		list := idx.SourceIdx[source]
		if result == nil {
			result = list
		} else {
			result = intersect(result, list)
		}
		if len(result) == 0 {
			return nil
		}
	}

	return result
}

func tokenize(s string) []string {
	fields := strings.Fields(s)
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		tok := strings.ToLower(f)
		if len(tok) > 32 {
			continue
		}
		if _, ok := stopWords[tok]; ok {
			continue
		}
		if isNumeric(tok) {
			continue
		}
		if hexPattern.MatchString(tok) {
			continue
		}
		tokens = append(tokens, tok)
	}
	return tokens
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

func intersect(a, b []uint64) []uint64 {
	result := make([]uint64, 0, min(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			result = append(result, a[i])
			i++
			j++
		}
	}
	return result
}
