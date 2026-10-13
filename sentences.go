package talkie

import (
	"strings"
	"unicode"
)

// abbreviations end in a period without ending a sentence.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true, "jr": true,
	"st": true, "vs": true, "e.g": true, "i.e": true, "a.m": true, "p.m": true, "no": true,
}

// sentences cuts the complete sentences off the front of text, written so far
// by a streaming agent, and returns them with the remainder. A sentence ends
// at ".", "!" or "?" (with any closing quotes or brackets) followed by
// whitespace, or at a line break; with final, the remainder is a sentence
// too. Decimals ("3.5") and common abbreviations ("Dr.") don't end one.
// Sentences are trimmed; the remainder keeps its leading whitespace, so the
// next delta joins it correctly.
func sentences(text string, final bool) (complete []string, rest string) {
	runes := []rune(text)
	start := 0
	emit := func(end int) {
		if s := strings.TrimSpace(string(runes[start:end])); s != "" {
			complete = append(complete, s)
		}
		start = end
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' {
			emit(i + 1)
			continue
		}
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		end := i + 1
		for end < len(runes) && strings.ContainsRune(`"')]”’`, runes[end]) {
			end++
		}
		if end == len(runes) || !unicode.IsSpace(runes[end]) {
			continue // the next delta decides, or "3.5" / "e.g."
		}
		if r == '.' && abbreviations[strings.ToLower(lastWord(runes[start:i]))] {
			continue
		}
		emit(end)
		i = end - 1
	}
	if final {
		emit(len(runes))
	}
	return complete, string(runes[start:])
}

// lastWord returns the letters and periods before the end of runes.
func lastWord(runes []rune) string {
	i := len(runes)
	for i > 0 && (unicode.IsLetter(runes[i-1]) || runes[i-1] == '.') {
		i--
	}
	return string(runes[i:])
}
