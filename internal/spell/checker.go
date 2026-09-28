package spell

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"plotzilla/internal/screenplay"
)

// Word is a word found in a text. Offset and Length count runes, like
// every offset exchanged with the editor.
type Word struct {
	Text   string
	Offset int
	Length int
}

func isApostrophe(r rune) bool { return r == '\'' || r == '’' }

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r)
}

// Words splits text into words: runs of letters and digits, with the
// apostrophes inside them ("don't", "l'homme"). Hyphens, slashes and
// dots separate words, so each half of "well-known" is checked alone.
func Words(text string) []Word {
	var words []Word
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if !isWordRune(runes[i]) {
			i++
			continue
		}
		start := i
		for i < len(runes) && (isWordRune(runes[i]) ||
			(isApostrophe(runes[i]) && i+1 < len(runes) && isWordRune(runes[i+1]))) {
			i++
		}
		words = append(words, Word{Text: string(runes[start:i]), Offset: start, Length: i - start})
	}
	return words
}

// Misspelling locates a misspelled word: the index of its element in
// document order (as layout.Break) and its rune offset and length.
type Misspelling struct {
	Element int    `json:"element"`
	Offset  int    `json:"offset"`
	Length  int    `json:"length"`
	Word    string `json:"word"`
}

// jargon lists screenplay abbreviations that no dictionary knows.
var jargon = map[string]bool{
	"int": true, "ext": true, "est": true, "cont'd": true, "contd": true,
	"pov": true, "vo": true, "os": true, "oc": true, "ecu": true, "mos": true,
}

var sceneOpener = regexp.MustCompile(`(?i)^\s*(int\.?/ext|ext\.?/int|int|ext|est|i/e)\b`)

// Checker checks screenplays against one dictionary plus the user's own
// words, and remembers the answers: a script repeats the same words.
type Checker struct {
	dict     *Dictionary
	mu       sync.Mutex
	cache    map[string]bool
	personal map[string]bool // lower-case
}

func NewChecker(d *Dictionary, personal []string) *Checker {
	c := &Checker{dict: d, cache: map[string]bool{}, personal: map[string]bool{}}
	for _, w := range personal {
		c.personal[strings.ToLower(w)] = true
	}
	return c
}

// Add accepts word from now on, in any letter case.
func (c *Checker) Add(word string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.personal[strings.ToLower(normalize(word))] = true
}

func normalize(w string) string { return strings.ReplaceAll(w, "’", "'") }

// Check reports whether word is correct. Words with digits ("3rd", "R2D2")
// and single letters are always accepted.
func (c *Checker) Check(word string) bool {
	if len([]rune(word)) < 2 || strings.IndexFunc(word, unicode.IsDigit) >= 0 {
		return true
	}
	lower := strings.ToLower(normalize(word))
	c.mu.Lock()
	defer c.mu.Unlock()
	if jargon[lower] || c.personal[lower] {
		return true
	}
	ok, found := c.cache[word]
	if !found {
		ok = c.dict.Check(word)
		c.cache[word] = ok
	}
	return ok
}

func (c *Checker) Suggest(word string) []string { return c.dict.Suggest(word) }

// Misspellings returns the misspelled words of a script. Character cues
// hold names, so they are not checked, and the names they introduce are
// accepted everywhere else (an action line often spells them in capitals).
func (c *Checker) Misspellings(doc *screenplay.Document) []Misspelling {
	names := map[string]bool{}
	for _, el := range doc.Elements {
		if el.Type == screenplay.Character {
			for _, w := range Words(screenplay.CharacterName(el.PrintedText())) {
				names[strings.ToLower(normalize(w.Text))] = true
			}
		}
	}
	out := []Misspelling{}
	for i, el := range doc.Elements {
		if el.Type == screenplay.Character {
			continue
		}
		text := el.Text()
		skip := 0 // runes of a scene heading's "INT./EXT."
		if el.Type == screenplay.Scene {
			if m := sceneOpener.FindString(text); m != "" {
				skip = len([]rune(m))
			}
		}
		for _, w := range Words(text) {
			if w.Offset < skip || isName(names, w.Text) || c.Check(w.Text) {
				continue
			}
			out = append(out, Misspelling{Element: i, Offset: w.Offset, Length: w.Length, Word: w.Text})
		}
	}
	return out
}

// isName reports whether w is one of the names, possibly possessive
// ("ELODIE's").
func isName(names map[string]bool, w string) bool {
	lower := strings.ToLower(normalize(w))
	return names[lower] || names[strings.TrimSuffix(lower, "'s")]
}

// Language is an installed dictionary, named after its files ("en_US").
type Language struct {
	Code string
	Aff  string
	Dic  string
}

// Available lists the dictionaries found in dirs; when the same language
// is in several directories, the first one wins.
func Available(dirs []string) []Language {
	seen := map[string]bool{}
	var out []Language
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.dic"))
		sort.Strings(matches)
		for _, dic := range matches {
			code := strings.TrimSuffix(filepath.Base(dic), ".dic")
			aff := strings.TrimSuffix(dic, ".dic") + ".aff"
			if seen[code] || strings.HasPrefix(code, "hyph_") || strings.HasPrefix(code, "th_") {
				continue
			}
			if _, err := os.Stat(aff); err != nil {
				continue
			}
			seen[code] = true
			out = append(out, Language{Code: code, Aff: aff, Dic: dic})
		}
	}
	return out
}

// Preferred picks the dictionary that suits a locale such as
// "fr_FR.UTF-8": the same language and region, then the same language,
// then American English, then the first one.
func Preferred(codes []string, locale string) string {
	if len(codes) == 0 {
		return ""
	}
	locale, _, _ = strings.Cut(locale, ".")
	locale, _, _ = strings.Cut(locale, "@")
	lang, _, _ := strings.Cut(locale, "_")
	for _, want := range []func(string) bool{
		func(c string) bool { return locale != "" && c == locale },
		func(c string) bool { return lang != "" && (c == lang || strings.HasPrefix(c, lang+"_")) },
		func(c string) bool { return c == "en_US" },
		func(c string) bool { return strings.HasPrefix(c, "en_") || c == "en" },
	} {
		for _, c := range codes {
			if want(c) {
				return c
			}
		}
	}
	return codes[0]
}
