package translate

import (
	"strings"
	"unicode"
)

// Lexicon tells which languages know a word, from spelling dictionaries.
// It refines the detection of words that are not function words.
type Lexicon interface {
	Knows(word string) (french, english bool)
}

// Function words of one language that are not words of the other: they
// carry most of the signal in a short line.
var frenchWords = set(`le la les un une des du de au aux et est sont ce cette ces cet il ils elle elles je tu nous vous
	ne pas que qui quoi dans sur avec pour par mais où donc ou ni sa ses leur leurs mon ma mes ton ta tes notre votre nos vos lui eux
	se ça cela ceci très bien aussi alors encore toujours jamais rien tout tous toute toutes quand puis sans sous chez vers entre
	être avoir fait faire va vais suis es était étaient ont avait avaient sera oui non merci voilà voici déjà ici là dont
	comme comment pourquoi peut peux veux veut faut quelque quelques chose rien personne moi toi soi y en
	après avant pendant depuis jusque contre derrière devant près loin enfin ensuite puis peu beaucoup trop assez`)

var englishWords = set(`the and is are was were be been being this that these those he she they we you it his her him their
	its my your our of to in at with for from by not no yes do does did have has had will would can could should shall
	what who whom where when why how there here then than just into out up down over about back all some any very
	only also again never always nothing everything something someone anyone everyone because before after while through
	i me us them which been get got go goes going went come came look looks looking says said tell told know think`)

// elisions are French contractions before a vowel: "l'homme", "qu'il".
var elisions = []string{"l'", "d'", "j'", "qu'", "n'", "s'", "c'", "m'", "t'", "jusqu'", "lorsqu'", "puisqu'"}

// contractions end English words: "don't", "we're", "Mary's".
var contractions = []string{"n't", "'re", "'ll", "'ve", "'m", "'d"}

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

const frenchLetters = "éèêëàâçîïôûùüœæ"

// words splits text into lower-case words, apostrophes included
// ("l'homme", "don't").
func words(text string) []string {
	var out []string
	var w []rune
	flush := func() {
		if len(w) > 0 {
			out = append(out, strings.Trim(string(w), "'"))
			w = w[:0]
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsMark(r):
			w = append(w, unicode.ToLower(r))
		case (r == '\'' || r == '’') && len(w) > 0:
			w = append(w, '\'')
		default:
			flush()
		}
	}
	flush()
	return out
}

// scores counts the evidence that text is French and that it is English.
// names are words to ignore (the script's characters).
func scores(text string, lex Lexicon, names map[string]bool) (fr, en int) {
	for _, w := range words(text) {
		if w == "" || names[w] {
			continue
		}
		switch {
		case frenchWords[w]:
			fr += 2
			continue
		case englishWords[w]:
			en += 2
			continue
		}
		if hasPrefix(w, elisions) {
			fr += 2
			continue
		}
		if hasSuffix(w, contractions) {
			en += 2
			continue
		}
		if strings.HasSuffix(w, "'s") {
			en++
			continue
		}
		if strings.ContainsAny(w, frenchLetters) {
			fr++
			continue
		}
		if lex != nil && len([]rune(w)) > 2 {
			switch f, e := lex.Knows(w); {
			case f && !e:
				fr++
			case e && !f:
				en++
			}
		}
	}
	return fr, en
}

func hasPrefix(w string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(w, p) && len(w) > len(p) {
			return true
		}
	}
	return false
}

func hasSuffix(w string, suffixes []string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(w, s) && len(w) > len(s) {
			return true
		}
	}
	return false
}

// notEnglish reports whether text is not clearly English: the parts of a
// line already known to be French (the location of a French slugline).
func (s *session) notEnglish(text string) bool {
	fr, en := scores(text, s.lex, s.nameWords)
	return en <= fr
}
