package marian

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"unicode"
)

// space is SentencePiece's visible word boundary ("▁").
const space = "▁"

// sentencePiece is a SentencePiece unigram model: it splits text into the
// pieces of the vocabulary that maximise the summed piece scores.
type sentencePiece struct {
	scores   map[string]float32
	maxRunes int
	unkScore float32
	// addDummyPrefix starts the text with "▁", as if preceded by a space.
	addDummyPrefix bool
}

// SentencePiece piece types (sentencepiece_model.proto).
const (
	pieceNormal      = 1
	pieceUnknown     = 2
	pieceControl     = 3
	pieceUserDefined = 4
	pieceUnused      = 5
	pieceByte        = 6
)

// loadSentencePiece reads a serialized ModelProto. Only the fields needed
// to encode are decoded: the pieces (field 1), the model type (trainer
// spec, field 2) and the dummy-prefix flag (normalizer spec, field 3).
func loadSentencePiece(path string) (*sentencePiece, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sp := &sentencePiece{scores: map[string]float32{}, addDummyPrefix: true}
	minScore := float32(0)
	err = protoFields(data, func(field int, wire int, v uint64, b []byte) error {
		switch field {
		case 1:
			var piece string
			var score float32
			typ := pieceNormal
			err := protoFields(b, func(field, wire int, v uint64, b []byte) error {
				switch field {
				case 1:
					piece = string(b)
				case 2:
					score = math.Float32frombits(uint32(v))
				case 3:
					typ = int(v)
				}
				return nil
			})
			if err != nil {
				return err
			}
			if typ == pieceNormal || typ == pieceUserDefined {
				sp.scores[piece] = score
				sp.maxRunes = max(sp.maxRunes, len([]rune(piece)))
				minScore = min(minScore, score)
			}
		case 2:
			return protoFields(b, func(field, wire int, v uint64, b []byte) error {
				if field == 3 && v != 1 {
					return fmt.Errorf("unsupported SentencePiece model type %d (only unigram)", v)
				}
				return nil
			})
		case 3:
			return protoFields(b, func(field, wire int, v uint64, b []byte) error {
				if field == 3 {
					sp.addDummyPrefix = v != 0
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(sp.scores) == 0 {
		return nil, fmt.Errorf("reading %s: no pieces", path)
	}
	// SentencePiece gives unknown characters a score below every piece.
	sp.unkScore = minScore - 10
	return sp, nil
}

// protoFields calls fn for each field of a protobuf message: v holds
// varints and fixed-width values, b length-delimited ones.
func protoFields(data []byte, fn func(field, wire int, v uint64, b []byte) error) error {
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return errors.New("malformed protobuf key")
		}
		data = data[n:]
		field, wire := int(key>>3), int(key&7)
		var v uint64
		var b []byte
		switch wire {
		case 0:
			v, n = binary.Uvarint(data)
			if n <= 0 {
				return errors.New("malformed protobuf varint")
			}
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return errors.New("truncated protobuf")
			}
			v, data = binary.LittleEndian.Uint64(data), data[8:]
		case 2:
			l, n := binary.Uvarint(data)
			if n <= 0 || uint64(len(data)-n) < l {
				return errors.New("truncated protobuf")
			}
			b, data = data[n:n+int(l)], data[n+int(l):]
		case 5:
			if len(data) < 4 {
				return errors.New("truncated protobuf")
			}
			v, data = uint64(binary.LittleEndian.Uint32(data)), data[4:]
		default:
			return fmt.Errorf("unsupported protobuf wire type %d", wire)
		}
		if err := fn(field, wire, v, b); err != nil {
			return err
		}
	}
	return nil
}

// normalize approximates SentencePiece's nmt_nfkc normalisation for the
// characters scripts actually contain: every kind of space becomes a
// plain one, runs of spaces collapse, control characters go away and a
// few compatibility characters are decomposed.
func normalize(text string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = b.Len() > 0
			continue
		case unicode.IsControl(r) || r == '\u200b' || r == '\ufeff':
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		switch r {
		case '…':
			b.WriteString("...")
		case 'ﬁ':
			b.WriteString("fi")
		case 'ﬂ':
			b.WriteString("fl")
		case 'ﬀ':
			b.WriteString("ff")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// encode splits text into pieces (Viterbi search over the unigram scores).
func (sp *sentencePiece) encode(text string) []string {
	text = normalize(text)
	if text == "" {
		return nil
	}
	if sp.addDummyPrefix {
		text = " " + text
	}
	runes := []rune(strings.ReplaceAll(text, " ", space))
	n := len(runes)
	best := make([]float64, n+1)
	from := make([]int, n+1)
	for i := 1; i <= n; i++ {
		best[i] = math.Inf(-1)
	}
	for end := 1; end <= n; end++ {
		for start := max(0, end-sp.maxRunes); start < end; start++ {
			if math.IsInf(best[start], -1) {
				continue
			}
			score, ok := sp.scores[string(runes[start:end])]
			if !ok {
				if end-start != 1 {
					continue
				}
				score = sp.unkScore
			}
			if s := best[start] + float64(score); s > best[end] {
				best[end], from[end] = s, start
			}
		}
	}
	var pieces []string
	for end := n; end > 0; end = from[end] {
		pieces = append(pieces, string(runes[from[end]:end]))
	}
	for i, j := 0, len(pieces)-1; i < j; i, j = i+1, j-1 {
		pieces[i], pieces[j] = pieces[j], pieces[i]
	}
	return pieces
}

// decodePieces joins target pieces back into text.
func decodePieces(pieces []string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.Join(pieces, ""), space, " "))
}
