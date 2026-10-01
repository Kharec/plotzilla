package translate

import (
	"strings"
	"unicode"
)

// The standard terms of French scripts and their Hollywood equivalents.
// Keys are folded (see fold): upper case, no accents, single spaces.

var frenchTimes = table(map[string]string{
	"JOUR": "DAY", "NUIT": "NIGHT", "SOIR": "EVENING", "SOIREE": "EVENING", "MATIN": "MORNING",
	"MATINEE": "MORNING", "APRES-MIDI": "AFTERNOON", "APRES MIDI": "AFTERNOON", "AUBE": "DAWN",
	"AURORE": "DAWN", "CREPUSCULE": "DUSK", "COUCHER DE SOLEIL": "SUNSET", "COUCHER DU SOLEIL": "SUNSET",
	"LEVER DE SOLEIL": "SUNRISE", "LEVER DU SOLEIL": "SUNRISE", "MIDI": "NOON", "MINUIT": "MIDNIGHT",
	"PLUS TARD": "LATER", "UN PEU PLUS TARD": "A LITTLE LATER", "PEU APRES": "MOMENTS LATER",
	"INSTANTS PLUS TARD": "MOMENTS LATER", "QUELQUES INSTANTS PLUS TARD": "MOMENTS LATER",
	"QUELQUES MINUTES PLUS TARD": "MINUTES LATER", "CONTINU": "CONTINUOUS", "CONTINUE": "CONTINUOUS",
	"CONTINUITE": "CONTINUOUS", "EN CONTINU": "CONTINUOUS", "SUITE": "CONTINUOUS",
	"MEME MOMENT": "SAME TIME", "AU MEME MOMENT": "SAME TIME", "MEME TEMPS": "SAME TIME",
	"LE LENDEMAIN": "THE NEXT DAY", "LENDEMAIN": "NEXT DAY", "PETIT MATIN": "EARLY MORNING",
	"TOT LE MATIN": "EARLY MORNING", "FIN D'APRES-MIDI": "LATE AFTERNOON", "FIN DE JOURNEE": "LATE AFTERNOON",
	"DEBUT DE SOIREE": "EARLY EVENING", "TARD LE SOIR": "LATE NIGHT", "TARD DANS LA NUIT": "LATE NIGHT",
	"PLUS TOT": "EARLIER", "NUIT AMERICAINE": "DAY FOR NIGHT",
})

var englishTimes = table(map[string]string{
	"DAY": "", "NIGHT": "", "MORNING": "", "EVENING": "", "AFTERNOON": "", "DAWN": "", "DUSK": "",
	"SUNSET": "", "SUNRISE": "", "NOON": "", "MIDNIGHT": "", "LATER": "", "CONTINUOUS": "",
	"MOMENTS LATER": "", "SAME TIME": "", "EARLY MORNING": "", "LATE NIGHT": "", "EARLIER": "",
	"MAGIC HOUR": "", "LATE AFTERNOON": "", "EARLY EVENING": "", "SAME": "", "CONT'D": "",
})

// sceneNotes are the parenthetical notes that follow a slugline.
var sceneNotes = table(map[string]string{
	"FLASH-BACK": "FLASHBACK", "FLASHBACK": "FLASHBACK", "RETOUR EN ARRIERE": "FLASHBACK",
	"FIN DU FLASH-BACK": "END FLASHBACK", "FIN DU FLASHBACK": "END FLASHBACK", "REVE": "DREAM",
	"CAUCHEMAR": "NIGHTMARE", "SOUVENIR": "MEMORY", "PRESENT": "PRESENT DAY", "RETOUR AU PRESENT": "PRESENT DAY",
	"EN MOUVEMENT": "MOVING", "ROULANT": "MOVING", "A L'ARRET": "STATIONARY", "IMAGINAIRE": "IMAGINED",
	"FANTASME": "FANTASY", "STOCK SHOT": "STOCK FOOTAGE", "IMAGES D'ARCHIVE": "STOCK FOOTAGE",
})

// extensions are the character extensions, folded without dots, spaces,
// hyphens or apostrophes: "V.O.", "VOIX OFF" and "voix-off" all give "VOIXOFF".
var extensions = map[string]string{
	"VO": "V.O.", "VOIXOFF": "V.O.", "OFF": "V.O.", "ENOFF": "V.O.", "ENPENSEE": "V.O.", "PENSEE": "V.O.",
	"PENSEES": "V.O.", "HC": "O.S.", "HORSCHAMP": "O.S.", "HORSCHAMPS": "O.S.", "SUITE": "CONT'D",
	"CONT": "CONT'D", "ENSUITE": "CONT'D", "AUTELEPHONE": "ON PHONE", "TELEPHONE": "ON PHONE",
	"AUTEL": "ON PHONE", "TEL": "ON PHONE", "ALARADIO": "ON RADIO", "RADIO": "ON RADIO",
	"ALATELE": "ON TV", "ALATELEVISION": "ON TV", "TELE": "ON TV", "ALATV": "ON TV",
	"SOUSTITRE": "SUBTITLED", "SOUSTITRES": "SUBTITLED", "SOUSTITREE": "SUBTITLED", "PRELAP": "PRE-LAP",
	"CHUCHOTE": "WHISPERING", "CHUCHOTANT": "WHISPERING", "ENCHUCHOTANT": "WHISPERING",
	"ENREGISTREMENT": "RECORDED", "ENREGISTRE": "RECORDED", "SURECRAN": "ON SCREEN", "ALECRAN": "ON SCREEN",
}

var transitions = table(map[string]string{
	"COUPE": "CUT TO:", "COUPE SUR": "CUT TO:", "COUPURE": "CUT TO:", "CUT SUR": "CUT TO:",
	"FONDU ENCHAINE": "DISSOLVE TO:", "ENCHAINE": "DISSOLVE TO:", "ENCHAINE SUR": "DISSOLVE TO:",
	"FONDU ENCHAINE SUR": "DISSOLVE TO:", "FONDU ENCHAINE A": "DISSOLVE TO:",
	"FONDU AU NOIR": "FADE TO BLACK.", "FONDU EN NOIR": "FADE TO BLACK.", "FONDU NOIR": "FADE TO BLACK.",
	"FONDU AU BLANC": "FADE TO WHITE.", "FONDU EN FERMETURE": "FADE OUT.", "FERMETURE AU NOIR": "FADE OUT.",
	"FERMETURE EN FONDU": "FADE OUT.", "FONDU DE FERMETURE": "FADE OUT.", "FONDU DE SORTIE": "FADE OUT.",
	"FONDU EN OUVERTURE": "FADE IN:", "OUVERTURE AU NOIR": "FADE IN:", "OUVERTURE EN FONDU": "FADE IN:",
	"FONDU D'OUVERTURE": "FADE IN:", "OUVERTURE EN FONDU AU NOIR": "FADE IN:", "OUVERTURE": "FADE IN:",
	"COUPE FRANCHE": "SMASH CUT TO:", "CUT FRANC": "SMASH CUT TO:", "COUPE SECHE": "SMASH CUT TO:",
	"CUT SEC": "SMASH CUT TO:", "RACCORD": "MATCH CUT TO:", "RACCORD SUR": "MATCH CUT TO:",
	"RACCORD MOUVEMENT": "MATCH CUT TO:", "RACCORD REGARD": "MATCH CUT TO:", "RACCORD DANS L'AXE": "MATCH CUT TO:",
	"COUPE SAUTEE": "JUMP CUT TO:", "VOLET": "WIPE TO:", "VOLET SUR": "WIPE TO:", "FONDU": "FADE TO:",
	"FONDU SUR": "FADE TO:", "FONDU A": "FADE TO:", "FIN": "THE END",
})

// shots are matched as the first words of an insert / shot element; the
// English forms start like the shots the Fountain format recognises.
var shots = []struct{ french, english string }{
	{"TRES GROS PLAN SUR", "EXTREME CLOSE ON"},
	{"TRES GROS PLAN", "EXTREME CLOSE-UP"},
	{"GROS PLAN SUR", "CLOSE ON"},
	{"GROS PLAN", "CLOSE UP"},
	{"PLAN RAPPROCHE SUR", "CLOSE ON"},
	{"PLAN SERRE SUR", "CLOSE ON"},
	{"PLAN LARGE SUR", "WIDE ON"},
	{"PLAN LARGE", "WIDE SHOT"},
	{"PLAN D'ENSEMBLE", "WIDE SHOT"},
	{"PLAN GENERAL", "WIDE SHOT"},
	{"PLAN DE SITUATION", "WIDE SHOT"},
	{"PLAN AERIEN", "AERIAL SHOT"},
	{"VUE AERIENNE", "AERIAL SHOT"},
	{"PLAN EN PLONGEE", "OVERHEAD SHOT"},
	{"PLONGEE", "OVERHEAD SHOT"},
	{"VUE DU DESSUS", "OVERHEAD SHOT"},
	{"POINT DE VUE DE", "POV"},
	{"POINT DE VUE", "POV"},
	{"P.D.V.", "POV"},
	{"PDV", "POV"},
	{"PLAN SUBJECTIF", "POV"},
	{"VUE SUBJECTIVE", "POV"},
	{"CONTRECHAMP", "REVERSE ANGLE"},
	{"CONTRE-CHAMP", "REVERSE ANGLE"},
	{"NOUVEL ANGLE", "NEW ANGLE"},
	{"ANGLE SUR", "ANGLE ON"},
	{"RETOUR A LA SCENE", "BACK TO SCENE"},
	{"RETOUR SUR LA SCENE", "BACK TO SCENE"},
	{"INSERT SUR", "INSERT"},
	{"TRAVELLING SUR", "TRACKING ON"},
	{"TRAVELLING", "TRACKING SHOT"},
	{"PLAN MOYEN", "MEDIUM SHOT"},
	{"PLAN AMERICAIN", "MEDIUM SHOT"},
	{"PLAN TAILLE", "MEDIUM SHOT"},
	{"SERIE DE PLANS", "SERIES OF SHOTS"},
	{"FIN DU MONTAGE", "END MONTAGE"},
}

// parentheticals are the stock wrylies, folded to lower case.
var parentheticals = map[string]string{
	"un temps": "beat", "temps": "beat", "un temps d'arret": "beat", "un silence": "beat",
	"suite": "continuing", "continuant": "continuing", "a voix basse": "quietly", "tout bas": "quietly",
	"bas": "quietly", "a part": "aside", "ironique": "wryly", "pour lui-meme": "to himself",
	"pour elle-meme": "to herself", "pour soi": "to themself", "off": "O.S.", "en off": "V.O.",
	"hors champ": "O.S.", "hors-champ": "O.S.", "voix off": "V.O.",
}

// credits are the usual title page credits, folded to lower case.
var credits = map[string]string{
	"ecrit par": "Written by", "scenario de": "Screenplay by", "scenario": "Screenplay by",
	"scenario et dialogues de": "Screenplay by", "scenario et dialogues": "Screenplay by",
	"un film de": "A film by", "realise par": "Directed by", "d'apres une idee de": "Story by",
	"histoire originale de": "Story by",
}

func table(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[fold(k)] = v
	}
	return out
}

var accents = strings.NewReplacer(
	"À", "A", "Â", "A", "Ä", "A", "Ç", "C", "É", "E", "È", "E", "Ê", "E", "Ë", "E", "Î", "I", "Ï", "I",
	"Ô", "O", "Ö", "O", "Ù", "U", "Û", "U", "Ü", "U", "Œ", "OE", "Æ", "AE", "Ÿ", "Y", "’", "'",
)

// fold upper-cases, removes accents and collapses spaces, to look a term
// up whatever its spelling.
func fold(s string) string {
	return strings.Join(strings.Fields(accents.Replace(strings.ToUpper(s))), " ")
}

// foldTight also drops dots, spaces, hyphens and apostrophes ("V.O." → "VO").
func foldTight(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '\'' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, fold(s))
}

// foldLower is fold in lower case, for parentheticals and credits.
func foldLower(s string) string { return strings.ToLower(fold(s)) }
