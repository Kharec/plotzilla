package spell

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type letterCase int

const (
	lowerCase letterCase = iota
	titleCase            // "Paris"
	upperCase            // "PARIS"
	mixedCase            // "iPhone", "McDonald"
)

func caseOf(w string) letterCase {
	upper, lower, firstUpper := 0, 0, false
	for i, r := range w {
		switch {
		case unicode.IsUpper(r):
			upper++
			if i == 0 {
				firstUpper = true
			}
		case unicode.IsLower(r):
			lower++
		}
	}
	switch {
	case upper == 0:
		return lowerCase
	case lower == 0:
		return upperCase
	case upper == 1 && firstUpper:
		return titleCase
	}
	return mixedCase
}

func capitalize(w string) string {
	r, size := utf8.DecodeRuneInString(w)
	return string(unicode.ToTitle(r)) + w[size:]
}

// Check reports whether word is spelled correctly. As in Hunspell, a
// capitalised or all-caps word is also accepted in its lower-case form
// ("Door", "DOOR"), but a lower-case proper noun is not ("paris").
func (d *Dictionary) Check(word string) bool {
	w := d.iconv.Replace(word)
	if d.accepts(w, false) {
		return true
	}
	switch caseOf(w) {
	case titleCase:
		return d.accepts(strings.ToLower(w), true)
	case upperCase:
		lower := strings.ToLower(w)
		return d.accepts(capitalize(lower), true) || d.accepts(lower, true)
	}
	return false
}

// accepts checks one exact spelling, as a word or as a compound word.
func (d *Dictionary) accepts(w string, caseChanged bool) bool {
	if d.valid(w, caseChanged) || (d.compound.enabled() && d.compoundOf(w, 0, "", caseChanged)) {
		return true
	}
	for _, r := range d.compound.rules {
		if d.matchesRule(w, r, 0, 0, caseChanged) {
			return true
		}
	}
	return false
}

// matchesRule reports whether w splits into dictionary words (without
// affixes, as in Hunspell) whose flags follow rule from step i; parts
// counts the words already matched.
func (d *Dictionary) matchesRule(w string, rule compoundRule, i, parts int, cc bool) bool {
	if i < len(rule) && rule[i].rep != 0 && d.matchesRule(w, rule, i+1, parts, cc) {
		return true // an optional step left out
	}
	if w == "" {
		return i == len(rule) && parts >= 2
	}
	if i == len(rule) {
		return false
	}
	step := rule[i]
	next := i + 1
	if step.rep == '*' {
		next = i
	}
	runes := []rune(w)
	for n := d.compound.min; n <= len(runes); n++ {
		part := string(runes[:n])
		if d.isForbidden(part) || !d.stemHas(part, cc, func(f flagSet) bool { return f.has(step.flag) }) {
			continue
		}
		if d.matchesRule(string(runes[n:]), rule, next, parts+1, cc) {
			return true
		}
	}
	return false
}

// Where a part sits in a compound word.
const (
	partBegin = iota
	partMiddle
	partLast
)

// compoundOf reports whether w splits into compound parts, the first of
// which follows parts earlier ones (prev is the last of them).
func (d *Dictionary) compoundOf(w string, parts int, prev string, cc bool) bool {
	c := d.compound
	runes := []rune(w)
	fits := func(n int) bool { return c.maxWords == 0 || n <= c.maxWords }
	for i := c.min; i <= len(runes)-c.min; i++ {
		head, rest := string(runes[:i]), string(runes[i:])
		if c.checkDup && head == prev {
			continue
		}
		if c.checkCase && (unicode.IsUpper(runes[i]) || unicode.IsUpper(runes[i-1])) {
			continue
		}
		pos := partBegin
		if parts > 0 {
			pos = partMiddle
		}
		if !d.compoundPart(head, pos, cc) {
			continue
		}
		if fits(parts+2) && !(c.checkDup && rest == head) && d.compoundPart(rest, partLast, cc) {
			return true
		}
		if fits(parts+3) && d.compoundOf(rest, parts+1, head, cc) {
			return true
		}
	}
	return false
}

// compoundPart checks one part of a compound word. The first part may take
// a prefix and the last one a suffix; other affixes need COMPOUNDPERMITFLAG.
func (d *Dictionary) compoundPart(w string, pos int, cc bool) bool {
	c := d.compound
	allowed := func(f flagSet) bool {
		return f.has(c.any) || (pos == partBegin && f.has(c.begin)) ||
			(pos == partMiddle && f.has(c.middle)) || (pos == partLast && f.has(c.last))
	}
	if d.isForbidden(w) {
		return false
	}
	if d.stemHas(w, cc, func(f flagSet) bool { return !f.has(d.needAffix) && allowed(f) }) {
		return true
	}
	affixOK := func(a *affix, at int) bool {
		return !a.cont.has(c.forbid) && (pos == at || a.cont.has(c.permit))
	}
	if d.matchingSuffixes(w, func(s *affix, stem string) bool {
		return affixOK(s, partLast) && d.stemHas(stem, cc, func(f flagSet) bool { return f.has(s.flag) && allowed(f) })
	}) {
		return true
	}
	return d.matchingPrefixes(w, func(p *affix, rest string) bool {
		if !affixOK(p, partBegin) {
			return false
		}
		if d.stemHas(rest, cc, func(f flagSet) bool { return f.has(p.flag) && allowed(f) }) {
			return true
		}
		// Both: a prefix and a suffix that combine (cross product).
		return p.cross && d.matchingSuffixes(rest, func(s *affix, stem string) bool {
			return s.cross && affixOK(s, partLast) &&
				d.stemHas(stem, cc, func(f flagSet) bool { return f.has(p.flag) && f.has(s.flag) && allowed(f) })
		})
	})
}

// valid checks one exact spelling. caseChanged is set when the word was
// lower-cased to be looked up, which KEEPCASE words forbid.
func (d *Dictionary) valid(w string, caseChanged bool) bool {
	if w == "" || d.isForbidden(w) {
		return false
	}
	if d.stemHas(w, caseChanged, func(f flagSet) bool { return !f.has(d.needAffix) && !f.has(d.onlyInComp) }) {
		return true
	}
	return d.suffixed(w, caseChanged) || d.prefixed(w, caseChanged)
}

func (d *Dictionary) isForbidden(w string) bool {
	if d.forbidden == 0 {
		return false
	}
	for _, f := range d.words[w] {
		if f.has(d.forbidden) {
			return true
		}
	}
	return false
}

// stemHas reports whether one of the homonyms of stem satisfies ok.
func (d *Dictionary) stemHas(stem string, caseChanged bool, ok func(flagSet) bool) bool {
	for _, f := range d.words[stem] {
		if f.has(d.forbidden) || (caseChanged && f.has(d.keepCase)) {
			continue
		}
		if ok(f) {
			return true
		}
	}
	return false
}

// matchingSuffixes calls fn for every suffix that w can end with, with the
// stem it leaves once the suffix is removed and the stripped text restored.
func (d *Dictionary) matchingSuffixes(w string, fn func(a *affix, stem string) bool) bool {
	for n := 0; n <= min(d.maxSuffix, len(w)); n++ {
		for _, a := range d.suffixes[w[len(w)-n:]] {
			base := w[:len(w)-n]
			if base == "" && !d.fullStrip {
				continue
			}
			stem := base + a.strip
			if stem == "" || !a.cond.matchEnd(stem) {
				continue
			}
			if fn(a, stem) {
				return true
			}
		}
	}
	return false
}

func (d *Dictionary) matchingPrefixes(w string, fn func(a *affix, stem string) bool) bool {
	for n := 0; n <= min(d.maxPrefix, len(w)); n++ {
		for _, a := range d.prefixes[w[:n]] {
			rest := w[n:]
			if rest == "" && !d.fullStrip {
				continue
			}
			stem := a.strip + rest
			if stem == "" || !a.cond.matchStart(stem) {
				continue
			}
			if fn(a, stem) {
				return true
			}
		}
	}
	return false
}

// suffixed accepts stem+suffix, and stem+suffix+suffix when the inner
// suffix lists the outer one as a continuation class.
func (d *Dictionary) suffixed(w string, cc bool) bool {
	return d.matchingSuffixes(w, func(s *affix, stem string) bool {
		if !s.cont.has(d.needAffix) && !s.cont.has(d.circumfix) &&
			d.stemHas(stem, cc, func(f flagSet) bool { return f.has(s.flag) }) {
			return true
		}
		if !d.contFlags[s.flag] {
			return false
		}
		return d.matchingSuffixes(stem, func(inner *affix, root string) bool {
			return inner.cont.has(s.flag) &&
				d.stemHas(root, cc, func(f flagSet) bool { return f.has(inner.flag) })
		})
	})
}

// prefixed accepts prefix+stem and prefix+stem+suffix.
func (d *Dictionary) prefixed(w string, cc bool) bool {
	return d.matchingPrefixes(w, func(p *affix, rest string) bool {
		if !p.cont.has(d.needAffix) && !p.cont.has(d.circumfix) &&
			d.stemHas(rest, cc, func(f flagSet) bool { return f.has(p.flag) }) {
			return true
		}
		return d.matchingSuffixes(rest, func(s *affix, stem string) bool {
			if s.cont.has(d.circumfix) != p.cont.has(d.circumfix) {
				return false
			}
			return d.stemHas(stem, cc, func(f flagSet) bool {
				switch {
				case f.has(s.flag) && f.has(p.flag):
					return p.cross && s.cross
				case f.has(s.flag):
					return s.cont.has(p.flag) // the suffix allows the prefix
				case f.has(p.flag):
					return p.cont.has(s.flag)
				}
				return false
			})
		})
	})
}

// maxSuggestions is how many corrections Suggest returns at most.
const maxSuggestions = 8

// Suggest returns likely corrections for a misspelled word, best first,
// in the word's case ("TEH" gives "THE"). It tries the word with a capital
// (proper nouns), the dictionary's table of common misspellings (REP), then
// every spelling one edit away, the most frequent kinds of typo first:
// swapped letters, an extra letter, a wrong letter, a missing letter, and
// a missing space between two words of three letters or more.
func (d *Dictionary) Suggest(word string) []string {
	w := d.iconv.Replace(word)
	wc := caseOf(w)
	base := w
	if wc == titleCase || wc == upperCase {
		base = strings.ToLower(w)
	}

	var out, proper []string
	seen := map[string]bool{w: true}
	try := func(cand string) {
		if len(out) >= maxSuggestions || seen[cand] {
			return
		}
		seen[cand] = true
		if d.suggestable(cand) {
			out = append(out, cand)
			return
		}
		// A lower-case candidate may be a proper noun ("pariss" → "Paris"),
		// offered after the common words.
		if caseOf(cand) == lowerCase {
			if cand = capitalize(cand); !seen[cand] && d.suggestable(cand) {
				seen[cand] = true
				proper = append(proper, cand)
			}
		}
	}

	if wc == lowerCase && d.suggestable(capitalize(base)) {
		out = append(out, capitalize(base))
	}
	for _, r := range d.rep {
		for i := 0; ; {
			j := strings.Index(base[i:], r.from)
			if j < 0 {
				break
			}
			at := i + j
			if (!r.start || at == 0) && (!r.end || at+len(r.from) == len(base)) {
				try(base[:at] + r.to + base[at+len(r.from):])
			}
			i = at + 1
		}
	}

	runes := []rune(base)
	edit := func(i, del int, ins ...rune) string {
		return string(runes[:i]) + string(ins) + string(runes[i+del:])
	}
	for i := 0; i+1 < len(runes); i++ {
		if runes[i] != runes[i+1] {
			try(edit(i, 2, runes[i+1], runes[i]))
		}
	}
	for i := range runes {
		try(edit(i, 1))
	}
	for i := range runes {
		for _, c := range d.try {
			if c != runes[i] && !unicode.IsUpper(c) {
				try(edit(i, 1, c))
			}
		}
	}
	for i := 0; i <= len(runes); i++ {
		for _, c := range d.try {
			if !unicode.IsUpper(c) {
				try(edit(i, 0, c))
			}
		}
	}
	// Short words ("a", "re", "er") make most splits look valid but are
	// rarely what was meant; REP tables cover the common cases ("a lot").
	for i := 3; i+3 <= len(runes); i++ {
		try(edit(i, 0, ' '))
	}

	out = append(out, proper...)
	out = out[:min(len(out), maxSuggestions)]
	for i, s := range out {
		switch wc {
		case upperCase:
			s = strings.ToUpper(s)
		case titleCase:
			s = capitalize(s)
		}
		out[i] = d.oconv.Replace(s)
	}
	return out
}

// suggestable checks a candidate, which may be two words separated by a
// space, and leaves out words marked NOSUGGEST (offensive words, mostly).
func (d *Dictionary) suggestable(cand string) bool {
	for _, w := range strings.Split(cand, " ") {
		if w == "" || strings.HasPrefix(w, "-") || strings.HasSuffix(w, "-") || !d.valid(w, false) {
			return false
		}
		for _, f := range d.words[w] {
			if f.has(d.noSuggest) {
				return false
			}
		}
	}
	return true
}
