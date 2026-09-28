// Package spell checks spelling with Hunspell dictionaries (the .aff and
// .dic files shipped by Linux distributions and LibreOffice). It implements
// the part of the Hunspell format that ordinary words need: prefixes and
// suffixes with their conditions, cross products, two-level suffixes,
// flag aliases and the usual word flags, plus compound words built with
// COMPOUNDFLAG or COMPOUNDBEGIN/MIDDLE/LAST (German, Dutch, Scandinavian
// dictionaries) or described by COMPOUNDRULE patterns.
package spell

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type flag uint16

// flagSet is a sorted list of flags; words and affixes carry only a few.
type flagSet []flag

func (s flagSet) has(f flag) bool {
	if f == 0 {
		return false
	}
	i := sort.Search(len(s), func(i int) bool { return s[i] >= f })
	return i < len(s) && s[i] == f
}

// condition is a Hunspell affix condition: a sequence of characters, "."
// or bracket classes ("[^aeiou]y"), matched against one end of the stem.
type condition []condUnit

type condUnit struct {
	any   bool
	neg   bool
	chars string
}

func (u condUnit) match(r rune) bool {
	if u.any {
		return true
	}
	return strings.ContainsRune(u.chars, r) != u.neg
}

func parseCondition(s string) condition {
	if s == "" || s == "." {
		return nil
	}
	var c condition
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '.':
			c = append(c, condUnit{any: true})
		case '[':
			u := condUnit{}
			i++
			if i < len(runes) && runes[i] == '^' {
				u.neg = true
				i++
			}
			start := i
			for i < len(runes) && runes[i] != ']' {
				i++
			}
			u.chars = string(runes[start:i])
			c = append(c, u)
		default:
			c = append(c, condUnit{chars: string(runes[i])})
		}
	}
	return c
}

func (c condition) matchStart(s string) bool {
	for _, u := range c {
		r, size := utf8.DecodeRuneInString(s)
		if size == 0 || !u.match(r) {
			return false
		}
		s = s[size:]
	}
	return true
}

func (c condition) matchEnd(s string) bool {
	for i := len(c) - 1; i >= 0; i-- {
		r, size := utf8.DecodeLastRuneInString(s)
		if size == 0 || !c[i].match(r) {
			return false
		}
		s = s[:len(s)-size]
	}
	return true
}

type affix struct {
	flag  flag
	cross bool
	strip string
	add   string
	cond  condition
	cont  flagSet // continuation classes: affixes allowed on top of this one
}

// compounding holds the rules for words made of several dictionary words
// ("Haustür" = "Haus" + "Tür").
type compounding struct {
	any, begin, middle, last flag // COMPOUNDFLAG, COMPOUNDBEGIN, COMPOUNDMIDDLE, COMPOUNDLAST
	permit, forbid           flag // affixes allowed or forbidden inside a compound
	min                      int  // shortest part, in characters (COMPOUNDMIN, 3 by default)
	maxWords                 int  // most parts (COMPOUNDWORDMAX, 0 = no limit)
	checkDup                 bool // CHECKCOMPOUNDDUP: no part repeated twice in a row
	checkCase                bool // CHECKCOMPOUNDCASE: no capital letter at a boundary
	rules                    []compoundRule
}

func (c compounding) enabled() bool { return c.any != 0 || c.begin != 0 || c.last != 0 }

// compoundRule is a COMPOUNDRULE pattern: a sequence of flags, each
// optionally followed by "*" (any number of parts) or "?" (at most one).
// A compound matches when its parts, in order, carry those flags.
type compoundRule []ruleStep

type ruleStep struct {
	flag flag
	rep  byte // 0, '*' or '?'
}

// parseRule reads a pattern such as "n*1t" or, with long or numeric flags,
// "(aa)(bb)*".
func (d *Dictionary) parseRule(s string) compoundRule {
	var r compoundRule
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		name := string(runes[i])
		if runes[i] == '(' {
			end := i + 1
			for end < len(runes) && runes[end] != ')' {
				end++
			}
			name = string(runes[i+1 : min(end, len(runes))])
			i = end
		}
		if name == "*" || name == "?" || name == "" {
			continue
		}
		step := ruleStep{flag: d.flagID(name)}
		if i+1 < len(runes) && (runes[i+1] == '*' || runes[i+1] == '?') {
			step.rep = byte(runes[i+1])
			i++
		}
		r = append(r, step)
	}
	return r
}

type replacement struct {
	from, to string
	start    bool // "^" anchor
	end      bool // "$" anchor
}

// Dictionary is a loaded Hunspell dictionary. It is safe for concurrent
// use once loaded.
type Dictionary struct {
	words map[string][]flagSet // homonyms keep separate flag sets

	// Affixes are indexed by the text they add, so a word is only tested
	// against the affixes that can have produced it.
	prefixes    map[string][]*affix
	suffixes    map[string][]*affix
	maxPrefix   int // longest added text, in bytes
	maxSuffix   int
	contFlags   map[flag]bool // suffix flags used as a continuation class
	fullStrip   bool
	needAffix   flag
	forbidden   flag
	keepCase    flag
	noSuggest   flag
	onlyInComp  flag
	circumfix   flag
	compound    compounding
	try         []rune
	rep         []replacement
	iconv       *strings.Replacer
	oconv       *strings.Replacer
	flagMode    string
	flagIDs     map[string]flag
	flagAliases []flagSet
}

// Load reads a dictionary from its .aff and .dic files.
func Load(affPath, dicPath string) (*Dictionary, error) {
	aff, err := os.ReadFile(affPath)
	if err != nil {
		return nil, fmt.Errorf("reading affix file: %w", err)
	}
	dic, err := os.ReadFile(dicPath)
	if err != nil {
		return nil, fmt.Errorf("reading dictionary: %w", err)
	}
	return Parse(aff, dic)
}

// Parse builds a dictionary from the contents of .aff and .dic files, in
// the encoding named by the affix file's SET directive.
func Parse(aff, dic []byte) (*Dictionary, error) {
	enc := encodingOf(aff)
	affText, err := decode(aff, enc)
	if err != nil {
		return nil, err
	}
	dicText, err := decode(dic, enc)
	if err != nil {
		return nil, err
	}
	d := &Dictionary{
		compound:  compounding{min: 3},
		words:     map[string][]flagSet{},
		prefixes:  map[string][]*affix{},
		suffixes:  map[string][]*affix{},
		contFlags: map[flag]bool{},
		flagIDs:   map[string]flag{},
	}
	if err := d.parseAff(affText); err != nil {
		return nil, err
	}
	d.parseDic(dicText)
	return d, nil
}

func encodingOf(aff []byte) string {
	for _, line := range bytes.Split(aff, []byte("\n")) {
		if f := bytes.Fields(line); len(f) >= 2 && string(f[0]) == "SET" {
			return strings.ToUpper(string(f[1]))
		}
	}
	return "ISO8859-1" // Hunspell's default
}

// ISO 8859-15 differs from Latin-1 in eight positions (€, Š, Ž, Œ…).
var latin9 = map[byte]rune{
	0xA4: '€', 0xA6: 'Š', 0xA8: 'š', 0xB4: 'Ž', 0xB8: 'ž', 0xBC: 'Œ', 0xBD: 'œ', 0xBE: 'Ÿ',
}

func decode(b []byte, enc string) (string, error) {
	switch strings.ReplaceAll(enc, "-", "") {
	case "UTF8":
		return strings.TrimPrefix(string(b), "\uFEFF"), nil
	case "ISO88591", "ISO885915":
		isLatin9 := strings.HasSuffix(enc, "15")
		var sb strings.Builder
		sb.Grow(len(b))
		for _, c := range b {
			r, ok := latin9[c]
			if !ok || !isLatin9 {
				r = rune(c) // Latin-1 bytes are the first 256 code points
			}
			sb.WriteRune(r)
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("unsupported dictionary encoding %s", enc)
}

// flagID interns a flag name ("A", "L'", "123") as a small integer.
func (d *Dictionary) flagID(name string) flag {
	if id, ok := d.flagIDs[name]; ok {
		return id
	}
	id := flag(len(d.flagIDs) + 1)
	d.flagIDs[name] = id
	return id
}

// parseFlags reads a flag field, which is either an alias number (AF) or
// flags written in the dictionary's FLAG format.
func (d *Dictionary) parseFlags(s string) flagSet {
	if len(d.flagAliases) > 0 {
		if n, err := strconv.Atoi(s); err == nil {
			if n >= 1 && n <= len(d.flagAliases) {
				return d.flagAliases[n-1]
			}
			return nil
		}
	}
	return d.plainFlags(s)
}

// plainFlags reads flags written in the dictionary's FLAG format.
func (d *Dictionary) plainFlags(s string) flagSet {
	var names []string
	switch d.flagMode {
	case "long":
		r := []rune(s)
		for i := 0; i+1 < len(r); i += 2 {
			names = append(names, string(r[i:i+2]))
		}
	case "num":
		names = strings.Split(s, ",")
	default: // single characters, "UTF-8" included
		for _, r := range s {
			names = append(names, string(r))
		}
	}
	set := make(flagSet, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			set = append(set, d.flagID(n))
		}
	}
	sort.Slice(set, func(i, j int) bool { return set[i] < set[j] })
	return set
}

func (d *Dictionary) oneFlag(s string) flag {
	if f := d.parseFlags(s); len(f) > 0 {
		return f[0]
	}
	return 0
}

// pairs collects the pairs of a table directive such as ICONV or REP.
func pairs(lines [][]string, key string) [][2]string {
	var out [][2]string
	for _, f := range lines {
		if len(f) >= 3 && f[0] == key {
			out = append(out, [2]string{f[1], f[2]})
		}
	}
	return out
}

func (d *Dictionary) parseAff(text string) error {
	var lines [][]string
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		lines = append(lines, f)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading affix file: %w", err)
	}

	// FLAG and AF change how every other flag is read: take them first.
	for _, f := range lines {
		if f[0] == "FLAG" && len(f) >= 2 {
			d.flagMode = f[1]
		}
	}
	var aliases []flagSet
	counted := false
	for _, f := range lines {
		if f[0] == "AF" && len(f) >= 2 {
			if !counted {
				counted = true // the first line holds the number of aliases
				continue
			}
			aliases = append(aliases, d.plainFlags(f[1]))
		}
	}
	d.flagAliases = aliases

	// COMPOUNDRULE: the first line holds the number of rules.
	counted = false
	for _, f := range lines {
		if f[0] == "COMPOUNDRULE" && len(f) >= 2 {
			if !counted {
				counted = true
				continue
			}
			if r := d.parseRule(f[1]); len(r) > 0 {
				d.compound.rules = append(d.compound.rules, r)
			}
		}
	}

	pending := map[string]int{} // affix entries still expected per "PFX A"
	cross := map[string]bool{}
	for _, f := range lines {
		switch f[0] {
		case "TRY":
			if len(f) >= 2 {
				d.try = []rune(f[1])
			}
		case "FULLSTRIP":
			d.fullStrip = true
		case "NEEDAFFIX", "PSEUDOROOT":
			d.needAffix = d.flagArg(f)
		case "FORBIDDENWORD":
			d.forbidden = d.flagArg(f)
		case "KEEPCASE":
			d.keepCase = d.flagArg(f)
		case "NOSUGGEST":
			d.noSuggest = d.flagArg(f)
		case "ONLYINCOMPOUND":
			d.onlyInComp = d.flagArg(f)
		case "CIRCUMFIX":
			d.circumfix = d.flagArg(f)
		case "COMPOUNDFLAG":
			d.compound.any = d.flagArg(f)
		case "COMPOUNDBEGIN":
			d.compound.begin = d.flagArg(f)
		case "COMPOUNDMIDDLE":
			d.compound.middle = d.flagArg(f)
		case "COMPOUNDLAST", "COMPOUNDEND":
			d.compound.last = d.flagArg(f)
		case "COMPOUNDPERMITFLAG":
			d.compound.permit = d.flagArg(f)
		case "COMPOUNDFORBIDFLAG":
			d.compound.forbid = d.flagArg(f)
		case "COMPOUNDMIN":
			if len(f) >= 2 {
				if n, err := strconv.Atoi(f[1]); err == nil {
					d.compound.min = max(n, 1)
				}
			}
		case "COMPOUNDWORDMAX":
			if len(f) >= 2 {
				d.compound.maxWords, _ = strconv.Atoi(f[1])
			}
		case "CHECKCOMPOUNDDUP":
			d.compound.checkDup = true
		case "CHECKCOMPOUNDCASE":
			d.compound.checkCase = true
		case "PFX", "SFX":
			if len(f) < 4 {
				continue
			}
			key := f[0] + " " + f[1]
			if pending[key] == 0 {
				n, err := strconv.Atoi(f[3])
				if err != nil {
					continue
				}
				pending[key] = n
				cross[key] = f[2] == "Y"
				continue
			}
			pending[key]--
			d.addAffix(f, cross[key])
		}
	}
	for _, p := range pairs(lines, "REP") {
		r := replacement{from: p[0], to: strings.ReplaceAll(p[1], "_", " ")}
		r.start = strings.HasPrefix(r.from, "^")
		r.end = strings.HasSuffix(r.from, "$")
		r.from = strings.TrimSuffix(strings.TrimPrefix(r.from, "^"), "$")
		r.from = strings.ReplaceAll(r.from, "_", " ")
		if r.from != "" {
			d.rep = append(d.rep, r)
		}
	}
	d.iconv = replacer(pairs(lines, "ICONV"))
	d.oconv = replacer(pairs(lines, "OCONV"))
	return nil
}

func (d *Dictionary) flagArg(f []string) flag {
	if len(f) < 2 {
		return 0
	}
	return d.oneFlag(f[1])
}

func replacer(table [][2]string) *strings.Replacer {
	var args []string
	for _, p := range table {
		args = append(args, p[0], p[1])
	}
	return strings.NewReplacer(args...)
}

// addAffix reads one entry line: "SFX flag strip add[/cont] [condition]".
func (d *Dictionary) addAffix(f []string, cross bool) {
	a := &affix{flag: d.oneFlag(f[1]), cross: cross}
	if f[2] != "0" {
		a.strip = f[2]
	}
	add := f[3]
	if i := strings.IndexByte(add, '/'); i >= 0 {
		a.cont = d.parseFlags(add[i+1:])
		add = add[:i]
	}
	if add != "0" {
		a.add = add
	}
	if len(f) >= 5 {
		a.cond = parseCondition(f[4])
	}
	if f[0] == "PFX" {
		d.prefixes[a.add] = append(d.prefixes[a.add], a)
		d.maxPrefix = max(d.maxPrefix, len(a.add))
		return
	}
	d.suffixes[a.add] = append(d.suffixes[a.add], a)
	d.maxSuffix = max(d.maxSuffix, len(a.add))
	for _, c := range a.cont {
		d.contFlags[c] = true
	}
}

func (d *Dictionary) parseDic(text string) {
	first := true
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if first {
			first = false
			if _, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
				continue // approximate word count
			}
		}
		if line == "" || line[0] == '\t' || strings.HasPrefix(line, "#") {
			continue
		}
		// Morphological fields follow a tab or a space.
		if i := strings.IndexAny(line, "\t "); i >= 0 {
			line = line[:i]
		}
		word, flags := splitEntry(line)
		if word == "" {
			continue
		}
		var set flagSet
		if flags != "" {
			set = d.parseFlags(flags)
		}
		d.words[word] = append(d.words[word], set)
	}
}

// splitEntry separates "word/flags", where "\/" is a literal slash.
func splitEntry(s string) (word, flags string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '/' {
			i++
			continue
		}
		if s[i] == '/' {
			return strings.ReplaceAll(s[:i], `\/`, "/"), s[i+1:]
		}
	}
	return strings.ReplaceAll(s, `\/`, "/"), ""
}
