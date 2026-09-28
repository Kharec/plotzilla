package spell

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"plotzilla/internal/screenplay"
)

const enAff = `SET UTF-8
TRY esianrtolcdugmphbyfvkwz'
ICONV 1
ICONV ’ '
NOSUGGEST !
KEEPCASE K
FORBIDDENWORD F
NEEDAFFIX N
REP 1
REP alot a_lot

PFX U Y 1
PFX U   0     un         .

SFX S Y 4
SFX S   y     ies        [^aeiou]y
SFX S   0     s          [aeiou]y
SFX S   0     s          [^sxzy]
SFX S   0     es         [sxz]

SFX D Y 2
SFX D   0     ed         [^e]
SFX D   0     d          e

SFX M Y 1
SFX M   0     's         .

# "ness" allows a plural on top of it: kindnesses.
SFX Q Y 1
SFX Q   0     ness/S     .
`

const enDic = `16
kind/QU
open/SD
for
door/S
city/SM
play/SD
love/DS
the
a
lot
Paris/M
iPhone
OpenGL/K
walkd/F
realis/N
rude/!
`

func testDict(t *testing.T) *Dictionary {
	t.Helper()
	d, err := Parse([]byte(enAff), []byte(enDic))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCheck(t *testing.T) {
	d := testDict(t)
	for word, want := range map[string]bool{
		"door":          true,
		"doors":         true,
		"cities":        true,
		"citys":         false, // condition [^y] rejects it
		"plays":         true,
		"played":        true,
		"loved":         true,
		"loveed":        false,
		"unkind":        true,
		"kindness":      true,
		"kindnesses":    true, // two suffixes
		"unkindness":    true, // cross product
		"undoor":        false,
		"city's":        true,
		"city’s":        true, // typographic apostrophe
		"Door":          true,
		"DOORS":         true,
		"Paris":         true,
		"PARIS":         true,
		"Paris's":       true,
		"paris":         false, // proper nouns keep their capital
		"iPhone":        true,
		"OpenGL":        true,
		"OPENGL":        false, // KEEPCASE
		"walkd":         false, // FORBIDDENWORD
		"realis":        false, // NEEDAFFIX
		"cty":           false,
		"":              false,
		"rude":          true,
		"UnKind":        false,
		"the":           true,
		"supercalifrag": false,
	} {
		if got := d.Check(word); got != want {
			t.Errorf("Check(%q) = %v, want %v", word, got, want)
		}
	}
}

func TestSuggest(t *testing.T) {
	d := testDict(t)
	for word, want := range map[string][]string{
		"teh":     {"the"},
		"dor":     {"for", "door"},
		"doorz":   {"door", "doors"},
		"DOOR'S":  {"DOORS"},
		"Citys":   {"City", "City's"},
		"paris":   {"Paris"},
		"alot":    {"a lot", "lot"},
		"thedoor": {"the door"},
		"rudee":   nil, // NOSUGGEST
		"luve":    {"love"},
	} {
		if got := d.Suggest(word); !reflect.DeepEqual(got, want) {
			t.Errorf("Suggest(%q) = %q, want %q", word, got, want)
		}
	}
}

// Long flags, flag aliases and prefixes allowed by a suffix's
// continuation class, as in the French dictionary ("l'homme", "d'hommes").
func TestAliasesAndLongFlags(t *testing.T) {
	aff := `SET UTF-8
FLAG long
AF 2
AF L'S.
AF S.

PFX L' Y 1
PFX L' 0 l' [aeiouh]

SFX S. Y 1
SFX S. 0 s/L' [^sxz]
`
	dic := "2\nhomme/1\nami/2\n"
	d, err := Parse([]byte(aff), []byte(dic))
	if err != nil {
		t.Fatal(err)
	}
	for word, want := range map[string]bool{
		"homme": true, "hommes": true, "l'homme": true, "l'hommes": true,
		"L'homme": true, "amis": true, "l'ami": false, "l'amis": true,
	} {
		if got := d.Check(word); got != want {
			t.Errorf("Check(%q) = %v, want %v", word, got, want)
		}
	}
}

func TestLatin1(t *testing.T) {
	aff := []byte("SET ISO8859-1\nTRY ae\xe4\n")
	dic := []byte("1\nH\xe4user\n")
	d, err := Parse(aff, dic)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Check("Häuser") || d.Check("Hauser") {
		t.Error("Latin-1 dictionary not decoded")
	}
	if got := d.Suggest("Hauser"); !slices.Contains(got, "Häuser") {
		t.Errorf("Suggest(Hauser) = %q", got)
	}
	if _, err := Parse([]byte("SET KOI8-R\n"), nil); err == nil {
		t.Error("unsupported encoding accepted")
	}
}

func TestWords(t *testing.T) {
	got := Words("Élise's well-known R2D2, l’homme… 'quoted' x")
	want := []Word{
		{"Élise's", 0, 7}, {"well", 8, 4}, {"known", 13, 5}, {"R2D2", 19, 4},
		{"l’homme", 25, 7}, {"quoted", 35, 6}, {"x", 43, 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Words = %+v\nwant    %+v", got, want)
	}
}

func TestMisspellings(t *testing.T) {
	c := NewChecker(testDict(t), []string{"Zorblax"})
	doc := &screenplay.Document{Elements: []screenplay.Element{
		screenplay.NewElement(screenplay.Scene, "INT. THE CTY - DAY"),
		screenplay.NewElement(screenplay.Action, "GRUNDLE opens the dor for Zorblax."),
		screenplay.NewElement(screenplay.Character, "Grundle (CONT'D)"),
		screenplay.NewElement(screenplay.Dialogue, "Élan loves 3rd doors"),
		screenplay.NewElement(screenplay.Action, "GRUNDLE's door."),
	}}
	got := c.Misspellings(doc)
	want := []Misspelling{
		{Element: 0, Offset: 9, Length: 3, Word: "CTY"},
		{Element: 0, Offset: 15, Length: 3, Word: "DAY"},
		{Element: 1, Offset: 18, Length: 3, Word: "dor"},
		{Element: 3, Offset: 0, Length: 4, Word: "Élan"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Misspellings = %+v\nwant           %+v", got, want)
	}

	c.Add("élan")
	c.Add("day")
	if got := c.Misspellings(doc); len(got) != 2 {
		t.Errorf("after Add, Misspellings = %+v", got)
	}
}

func TestAvailable(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, f := range []string{
		filepath.Join(a, "fr_FR.aff"), filepath.Join(a, "fr_FR.dic"),
		filepath.Join(a, "de.dic"), // no .aff: skipped
		filepath.Join(a, "hyph_fr.dic"), filepath.Join(a, "hyph_fr.aff"),
		filepath.Join(b, "fr_FR.aff"), filepath.Join(b, "fr_FR.dic"),
		filepath.Join(b, "en_US.aff"), filepath.Join(b, "en_US.dic"),
	} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Available([]string{a, b, filepath.Join(a, "missing")})
	want := []Language{
		{"fr_FR", filepath.Join(a, "fr_FR.aff"), filepath.Join(a, "fr_FR.dic")},
		{"en_US", filepath.Join(b, "en_US.aff"), filepath.Join(b, "en_US.dic")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Available = %+v", got)
	}
}

func TestPreferred(t *testing.T) {
	codes := []string{"de_DE_frami", "en_GB", "en_US", "fr"}
	for locale, want := range map[string]string{
		"fr_FR.UTF-8": "fr",
		"en_GB.UTF-8": "en_GB",
		"de_AT.UTF-8": "de_DE_frami",
		"C":           "en_US",
		"":            "en_US",
	} {
		if got := Preferred(codes, locale); got != want {
			t.Errorf("Preferred(%q) = %q, want %q", locale, got, want)
		}
	}
	if got := Preferred(nil, "fr_FR"); got != "" {
		t.Errorf("Preferred(nil) = %q", got)
	}
}

func TestCompounds(t *testing.T) {
	// German-style compounds: parts marked for the start, middle or end,
	// a linking "s" that only exists inside compounds, and an affix that
	// lower-cases a noun inside a compound (COMPOUNDPERMITFLAG).
	aff := `SET UTF-8
COMPOUNDBEGIN B
COMPOUNDMIDDLE M
COMPOUNDLAST E
ONLYINCOMPOUND O
COMPOUNDPERMITFLAG P
COMPOUNDMIN 1
COMPOUNDWORDMAX 3
CHECKCOMPOUNDDUP

PFX L Y 3
PFX L   T     t/P     T
PFX L   S     s/P     S
PFX L   Z     z/P     Z

SFX N Y 1
SFX N   0     en      .
`
	dic := `6
Haus/BM
Tür/LMEN
Schlüssel/BMLE
s/MO
Arbeit/BM
Zeit/LE
`
	d, err := Parse([]byte(aff), []byte(dic))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"Haus", "Haustür", "Haustüren", "Haustürschlüssel", "Arbeitszeit", "HAUSTÜR"} {
		if !d.Check(w) {
			t.Errorf("%q should be correct", w)
		}
	}
	for _, w := range []string{"s", "Türhaus", "Hausshaus", "Haushaus", "Arbeitsarbeitszeit", "Haustürx"} {
		if d.Check(w) {
			t.Errorf("%q should be wrong", w)
		}
	}

	// COMPOUNDFLAG: any marked word can go anywhere.
	d, err = Parse([]byte("SET UTF-8\nCOMPOUNDFLAG X\n"), []byte("2\nfoot/X\nball/X\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Check("football") || !d.Check("ballfoot") || d.Check("footbal") {
		t.Errorf("COMPOUNDFLAG compounds")
	}
}

func TestCompoundRule(t *testing.T) {
	// en_US style: numbers built from digits, ordinal endings that only
	// exist in compounds, and a word pattern with long flags.
	aff := `SET UTF-8
ONLYINCOMPOUND c
COMPOUNDMIN 1
COMPOUNDRULE 2
COMPOUNDRULE n*1t
COMPOUNDRULE n*mp
`
	dic := `8
0/nm
1/n1
2/nm
3/nm
st/tc
nd/pc
th/pc
rd/pc
`
	d, err := Parse([]byte(aff), []byte(dic))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"1st", "21st", "2nd", "33rd", "20th"} {
		if !d.Check(w) {
			t.Errorf("%q should be correct", w)
		}
	}
	for _, w := range []string{"st", "1nd", "2st", "st1"} {
		if d.Check(w) {
			t.Errorf("%q should be wrong", w)
		}
	}

	d, err = Parse([]byte("SET UTF-8\nFLAG long\nCOMPOUNDRULE 1\nCOMPOUNDRULE (aa)(bb)?(cc)\n"),
		[]byte("3\nsun/aa\nflower/cc\nlit/bb\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Check("sunflower") || !d.Check("sunlitflower") || d.Check("flowersun") || d.Check("sunlit") {
		t.Errorf("long-flag COMPOUNDRULE")
	}
}
