package marian

import (
	"context"
	"math"
	"runtime"
	"slices"
	"sync"
)

// beam is a partial translation being extended by beam search.
type beam struct {
	tokens []int // starts with the decoder start token
	score  float64
	// Keys and values of the decoder's self-attention, one slice per layer
	// holding len(tokens) rows: each step only computes its own row. When a
	// beam is the only survivor of its parent it takes the parent's slices
	// over instead of copying them (the parent is dead from that step on).
	keys, values [][]float32
}

type hypothesis struct {
	tokens []int
	score  float64 // length-normalised
}

type candidate struct {
	parent, token int
	score         float64
}

// decoderScratch holds the work buffers of a decoding step, shared by the
// layers, the beams and the steps of a search: allocating them afresh for
// every layer of every step costs more than the dot products they serve.
type decoderScratch struct {
	q, k, v   []float32
	ctxv, tmp []float32
	scores    []float32
	hidden    []float32
}

// beamSearch generates the translation, as Hugging Face's generate() does
// with the model's defaults: num_beams beams, length penalty 1, the
// padding token never produced.
func (m *Model) beamSearch(ctx context.Context, enc []float32, srcLen int) ([]int, error) {
	d := m.cfg.DModel
	numBeams := max(m.cfg.NumBeams, 1)
	crossK := make([][]float32, len(m.decoder))
	crossV := make([][]float32, len(m.decoder))
	ffn := 0
	for i, l := range m.decoder {
		crossK[i] = make([]float32, srcLen*d)
		crossV[i] = make([]float32, srcLen*d)
		l.cross.k.rows(crossK[i], enc, srcLen)
		l.cross.v.rows(crossV[i], enc, srcLen)
		ffn = max(ffn, l.fc1.out)
	}
	maxLen := min(m.cfg.MaxPositions-1, 2*srcLen+10)
	scratch := &decoderScratch{
		q: make([]float32, d), k: make([]float32, d), v: make([]float32, d),
		ctxv: make([]float32, d), tmp: make([]float32, d),
		scores: make([]float32, max(maxLen+1, srcLen)),
		hidden: make([]float32, ffn),
	}
	// One vocabulary of logits per beam, reused at every step: the largest
	// allocation of the search.
	probs := make([][]float32, numBeams)
	for i := range probs {
		probs[i] = make([]float32, m.cfg.VocabSize)
	}
	beams := []beam{{
		tokens: []int{m.cfg.DecoderStart},
		keys:   make([][]float32, len(m.decoder)),
		values: make([][]float32, len(m.decoder)),
	}}
	var done []hypothesis
	for len(beams[0].tokens) <= maxLen {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hidden := make([][]float32, len(beams))
		for i := range beams {
			hidden[i] = m.decodeStep(&beams[i], crossK, crossV, srcLen, scratch)
		}
		logProbs := m.logProbs(hidden, probs[:len(beams)])
		var cands []candidate
		for i, lp := range logProbs {
			for _, t := range topK(lp, 2*numBeams) {
				cands = append(cands, candidate{parent: i, token: t, score: beams[i].score + float64(lp[t])})
			}
		}
		slices.SortStableFunc(cands, func(a, b candidate) int {
			switch {
			case a.score > b.score:
				return -1
			case a.score < b.score:
				return 1
			}
			return 0
		})
		// Choose the surviving beams first, then build them: a cache is
		// copied at most once per step, and only when several survivors
		// share its parent.
		children := make([]int, len(beams))
		chosen := make([]candidate, 0, numBeams)
		for rank, c := range cands {
			if c.token == m.cfg.EOSToken {
				if rank < numBeams {
					parent := beams[c.parent]
					done = append(done, hypothesis{tokens: slices.Clone(parent.tokens), score: c.score / float64(len(parent.tokens))})
				}
				continue
			}
			chosen = append(chosen, c)
			children[c.parent]++
			if len(chosen) == numBeams {
				break
			}
		}
		next := make([]beam, 0, len(chosen))
		for _, c := range chosen {
			parent := &beams[c.parent]
			child := beam{
				tokens: append(slices.Clone(parent.tokens), c.token),
				score:  c.score,
				keys:   make([][]float32, len(parent.keys)),
				values: make([][]float32, len(parent.values)),
			}
			takeOver := children[c.parent] == 1
			for l := range parent.keys {
				if takeOver {
					child.keys[l] = parent.keys[l]
					child.values[l] = parent.values[l]
				} else {
					child.keys[l] = slices.Clone(parent.keys[l])
					child.values[l] = slices.Clone(parent.values[l])
				}
			}
			next = append(next, child)
		}
		slices.SortStableFunc(done, func(a, b hypothesis) int {
			switch {
			case a.score > b.score:
				return -1
			case a.score < b.score:
				return 1
			}
			return 0
		})
		if len(done) > numBeams {
			done = done[:numBeams]
		}
		if len(next) == 0 {
			break
		}
		// Finished: no running beam can still beat the worst kept
		// hypothesis (scores only decrease as tokens are added).
		if len(done) == numBeams && next[0].score/float64(len(next[0].tokens)) <= done[numBeams-1].score {
			break
		}
		beams = next
	}
	if len(done) == 0 {
		return beams[0].tokens[1:], nil
	}
	return done[0].tokens[1:], nil
}

// decodeStep runs the decoder for the beam's last token and returns its
// output vector.
func (m *Model) decodeStep(b *beam, crossK, crossV [][]float32, srcLen int, s *decoderScratch) []float32 {
	d := m.cfg.DModel
	pos := len(b.tokens) - 1
	tok := b.tokens[pos]
	x := make([]float32, d) // the beam's output vector, which lives past the step
	emb := m.embed[tok*d : (tok+1)*d]
	p := m.decPos[pos*d : (pos+1)*d]
	for j := range x {
		x[j] = emb[j]*m.embedScale + p[j]
	}
	q, k, v, ctxv, tmp, scores := s.q, s.k, s.v, s.ctxv, s.tmp, s.scores
	for i, l := range m.decoder {
		l.self.q.apply(q, x)
		l.self.k.apply(k, x)
		l.self.v.apply(v, x)
		b.keys[i] = append(b.keys[i], k...)
		b.values[i] = append(b.values[i], v...)
		attend(ctxv, q, b.keys[i], b.values[i], pos+1, l.self.heads, scores)
		l.self.o.apply(tmp, ctxv)
		addNorm(x, tmp, l.selfNorm, 1)

		l.cross.q.apply(q, x)
		attend(ctxv, q, crossK[i], crossV[i], srcLen, l.cross.heads, scores)
		l.cross.o.apply(tmp, ctxv)
		addNorm(x, tmp, l.crossNorm, 1)

		hidden := s.hidden[:l.fc1.out]
		l.fc1.apply(hidden, x)
		for j := range hidden {
			hidden[j] = m.activation(hidden[j])
		}
		l.fc2.apply(tmp, hidden)
		addNorm(x, tmp, l.finalLayerNorm, 1)
	}
	return x
}

// logProbs projects each beam's vector on the vocabulary (the embedding
// matrix, shared with the output layer) and returns log-softmax scores in
// out, one vocabulary per beam, reused from step to step. This is the
// largest product of a step, so it is split across CPUs.
func (m *Model) logProbs(hidden, out [][]float32) [][]float32 {
	d, vocab := m.cfg.DModel, m.cfg.VocabSize
	workers := runtime.GOMAXPROCS(0)
	chunk := (vocab + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < vocab; lo += chunk {
		hi := min(lo+chunk, vocab)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := lo; t < hi; t++ {
				row := m.embed[t*d : (t+1)*d]
				for i, h := range hidden {
					out[i][t] = dot(h, row) + m.logitsBias[t]
				}
			}
		}()
	}
	wg.Wait()
	for _, logits := range out {
		if m.cfg.PadToken >= 0 && m.cfg.PadToken < vocab {
			logits[m.cfg.PadToken] = float32(math.Inf(-1))
		}
		maxLogit := float32(math.Inf(-1))
		for _, l := range logits {
			maxLogit = max(maxLogit, l)
		}
		var sum float64
		for _, l := range logits {
			sum += math.Exp(float64(l - maxLogit))
		}
		norm := maxLogit + float32(math.Log(sum))
		for t := range logits {
			logits[t] -= norm
		}
	}
	return out
}

// topK returns the indexes of the k largest values, largest first.
func topK(values []float32, k int) []int {
	idx := make([]int, 0, k+1)
	for i, v := range values {
		if len(idx) == k && v <= values[idx[k-1]] {
			continue
		}
		at := len(idx)
		for at > 0 && values[idx[at-1]] < v {
			at--
		}
		idx = slices.Insert(idx, at, i)
		if len(idx) > k {
			idx = idx[:k]
		}
	}
	return idx
}
