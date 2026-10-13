package engine

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}_']`)

// norm lowercases a word and drops everything but letters, digits,
// underscores and apostrophes.
func norm(word string) string {
	return nonWord.ReplaceAllString(strings.ToLower(word), "")
}

// stripCutoff is how similar a word must be to a wake phrase word to count.
const stripCutoff = 0.75

// StripWakePhrase removes leading occurrences of any of phrases from text,
// fuzzily, so a transcript doesn't start with the wake word:
//
//	"Hey Jarvis, turn off the lights" -> "Turn off the lights"
//	"Hey, Jervis. What time is it?"   -> "What time is it?"
func StripWakePhrase(text string, phrases []string) string {
	tokens := strings.Fields(text)
	norms := make([]string, len(tokens))
	for i, t := range tokens {
		norms[i] = norm(t)
	}
	var targets [][]string
	for _, p := range phrases {
		var words []string
		for _, w := range strings.Fields(p) {
			words = append(words, norm(w))
		}
		if len(words) > 0 {
			targets = append(targets, words)
		}
	}
	slices.SortStableFunc(targets, func(a, b []string) int { return len(b) - len(a) })
	i := 0
	for changed := true; changed; {
		changed = false
		for i < len(norms) && norms[i] == "" {
			i++
		}
		for _, target := range targets {
			if i+len(target) > len(norms) {
				continue
			}
			match := true
			for j, word := range target {
				match = match && similar(norms[i+j], word)
			}
			if match {
				i += len(target)
				changed = true
				break
			}
		}
	}
	if i == 0 {
		return strings.TrimSpace(text)
	}
	rest := strings.TrimSpace(strings.TrimLeft(strings.Join(tokens[i:], " "), ",.;:!?-– "))
	if rest == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(rest)
	return string(unicode.ToUpper(r)) + rest[size:]
}

func similar(a, b string) bool {
	return a == b || ratio(a, b) >= stripCutoff
}

// ratio is Python difflib.SequenceMatcher's ratio, 2*M/T, where M counts the
// characters in matching blocks found by repeatedly taking the longest
// common substring (Ratcliff/Obershelp), as wonky-talkie computed it.
func ratio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra)+len(rb) == 0 {
		return 1
	}
	return 2 * float64(matching(ra, rb)) / float64(len(ra)+len(rb))
}

func matching(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Longest common substring, earliest in a, then earliest in b, as difflib.
	bestI, bestJ, best := 0, 0, 0
	lengths := make([]int, len(b)+1)
	for i := range a {
		prev := 0
		for j := range b {
			current := lengths[j+1]
			if a[i] == b[j] {
				lengths[j+1] = prev + 1
				if lengths[j+1] > best {
					best, bestI, bestJ = lengths[j+1], i-prev, j-prev
				}
			} else {
				lengths[j+1] = 0
			}
			prev = current
		}
	}
	if best == 0 {
		return 0
	}
	return best + matching(a[:bestI], b[:bestJ]) + matching(a[bestI+best:], b[bestJ+best:])
}
