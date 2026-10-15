// Package marian runs MarianMT translation models (the OPUS-MT models of
// the University of Helsinki, in their Hugging Face form) on the CPU, in
// plain Go: a SentencePiece tokenizer, the transformer encoder-decoder and
// beam search. A model directory holds config.json, model.safetensors,
// source.spm, target.spm and vocab.json.
//
// The engine lives in-tree on purpose. Plotzilla ships as one binary built
// with only the Go toolchain: a native runtime (ctranslate2, argos) would
// add cgo and per-distribution packaging, and a separate binary or plugin
// an install step, all for a single menu item. Translation is offline by
// design — an unpublished script never leaves the machine — and the model
// is data downloaded at runtime (Apache-2.0), so neither the "no
// third-party dependency" rule nor the GPL is affected. The screenplay
// conventions stay out of this package (internal/translate owns them) and
// the pipeline is covered by a synthetic tiny model and, on demand, the
// real one. If more language pairs are ever added, each with its own
// 300 MB download and quirks, reconsider a separate optional component.
package marian

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Files are the files a model directory must contain.
var Files = []string{"config.json", "model.safetensors", "source.spm", "target.spm", "vocab.json"}

type config struct {
	DModel          int    `json:"d_model"`
	EncoderLayers   int    `json:"encoder_layers"`
	DecoderLayers   int    `json:"decoder_layers"`
	EncoderHeads    int    `json:"encoder_attention_heads"`
	DecoderHeads    int    `json:"decoder_attention_heads"`
	Activation      string `json:"activation_function"`
	ScaleEmbedding  bool   `json:"scale_embedding"`
	NormalizeBefore bool   `json:"normalize_before"`
	MaxPositions    int    `json:"max_position_embeddings"`
	VocabSize       int    `json:"vocab_size"`
	PadToken        int    `json:"pad_token_id"`
	EOSToken        int    `json:"eos_token_id"`
	DecoderStart    int    `json:"decoder_start_token_id"`
	NumBeams        int    `json:"num_beams"`
}

type linear struct {
	w, b    []float32
	in, out int
}

type layerNorm struct{ g, b []float32 }

type attention struct {
	q, k, v, o linear
	heads      int
}

type encoderLayer struct {
	self           attention
	selfNorm       layerNorm
	fc1, fc2       linear
	finalLayerNorm layerNorm
}

type decoderLayer struct {
	self, cross         attention
	selfNorm, crossNorm layerNorm
	fc1, fc2            linear
	finalLayerNorm      layerNorm
}

// Model is a loaded translation model. It is read-only once loaded (but
// for SetThreads, right after Load): Translate may be called from several
// goroutines at once.
type Model struct {
	cfg        config
	threads    int       // goroutines a decoding step may start (0: one per CPU)
	embed      []float32 // vocab × d, shared by encoder, decoder and output
	encPos     []float32 // positions × d
	decPos     []float32
	logitsBias []float32
	encoder    []encoderLayer
	decoder    []decoderLayer
	source     *sentencePiece
	target     *sentencePiece
	ids        map[string]int
	pieces     []string
	unk        int
	embedScale float32
	activation func(float32) float32
}

// Load reads the model in dir. The load can take seconds for a large
// model: it stops early when ctx is cancelled.
func Load(ctx context.Context, dir string) (*Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var cfg config
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("reading config.json: %w", err)
	}
	if cfg.DModel == 0 || cfg.EncoderHeads == 0 || cfg.DecoderHeads == 0 || cfg.DModel%cfg.EncoderHeads != 0 || cfg.DModel%cfg.DecoderHeads != 0 {
		return nil, fmt.Errorf("unsupported model dimensions in config.json")
	}
	if cfg.NormalizeBefore {
		return nil, fmt.Errorf("unsupported model: pre-norm layers")
	}
	m := &Model{cfg: cfg, embedScale: 1}
	if cfg.ScaleEmbedding {
		m.embedScale = float32(math.Sqrt(float64(cfg.DModel)))
	}
	switch cfg.Activation {
	case "swish", "silu":
		m.activation = func(x float32) float32 { return x / (1 + float32(math.Exp(float64(-x)))) }
	case "relu":
		m.activation = func(x float32) float32 { return max(x, 0) }
	case "gelu":
		m.activation = func(x float32) float32 { return 0.5 * x * (1 + float32(math.Erf(float64(x)/math.Sqrt2))) }
	default:
		return nil, fmt.Errorf("unsupported activation %q", cfg.Activation)
	}

	if m.source, err = loadSentencePiece(filepath.Join(dir, "source.spm")); err != nil {
		return nil, err
	}
	if m.target, err = loadSentencePiece(filepath.Join(dir, "target.spm")); err != nil {
		return nil, err
	}
	if data, err = os.ReadFile(filepath.Join(dir, "vocab.json")); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m.ids); err != nil {
		return nil, fmt.Errorf("reading vocab.json: %w", err)
	}
	m.pieces = make([]string, cfg.VocabSize)
	for p, id := range m.ids {
		if id >= 0 && id < len(m.pieces) {
			m.pieces[id] = p
		}
	}
	m.unk = m.ids["<unk>"]

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tensors, err := readSafetensors(ctx, filepath.Join(dir, "model.safetensors"))
	if err != nil {
		return nil, fmt.Errorf("reading model.safetensors: %w", err)
	}
	if err := m.bind(tensors); err != nil {
		return nil, fmt.Errorf("reading model.safetensors: %w", err)
	}
	return m, nil
}

// bind takes the weights the model needs from the tensors, checking their
// shapes.
func (m *Model) bind(t map[string]tensor) error {
	d := m.cfg.DModel
	var missing error
	get := func(name string, shape ...int) []float32 {
		x, ok := t[name]
		if !ok {
			if missing == nil {
				missing = fmt.Errorf("missing tensor %s", name)
			}
			return nil
		}
		n := 1
		for _, s := range shape {
			n *= s
		}
		if len(x.data) != n && missing == nil {
			missing = fmt.Errorf("tensor %s has shape %v", name, x.shape)
		}
		return x.data
	}
	first := func(names ...string) string {
		for _, n := range names {
			if _, ok := t[n]; ok {
				return n
			}
		}
		return names[0]
	}
	lin := func(prefix string, in, out int) linear {
		return linear{w: get(prefix+".weight", out, in), b: get(prefix+".bias", out), in: in, out: out}
	}
	norm := func(prefix string) layerNorm {
		return layerNorm{g: get(prefix+".weight", d), b: get(prefix+".bias", d)}
	}
	attn := func(prefix string, heads int) attention {
		return attention{
			q: lin(prefix+".q_proj", d, d), k: lin(prefix+".k_proj", d, d),
			v: lin(prefix+".v_proj", d, d), o: lin(prefix+".out_proj", d, d), heads: heads,
		}
	}
	ffn := func(prefix string) int {
		if x, ok := t[prefix+".fc1.weight"]; ok && len(x.shape) == 2 {
			return x.shape[0]
		}
		return 4 * d
	}

	v := m.cfg.VocabSize
	m.embed = get(first("model.shared.weight", "model.decoder.embed_tokens.weight", "model.encoder.embed_tokens.weight"), v, d)
	m.encPos = get("model.encoder.embed_positions.weight", m.cfg.MaxPositions, d)
	m.decPos = get("model.decoder.embed_positions.weight", m.cfg.MaxPositions, d)
	m.logitsBias = get("final_logits_bias", 1, v)
	for i := range m.cfg.EncoderLayers {
		p := fmt.Sprintf("model.encoder.layers.%d", i)
		f := ffn(p)
		m.encoder = append(m.encoder, encoderLayer{
			self: attn(p+".self_attn", m.cfg.EncoderHeads), selfNorm: norm(p + ".self_attn_layer_norm"),
			fc1: lin(p+".fc1", d, f), fc2: lin(p+".fc2", f, d), finalLayerNorm: norm(p + ".final_layer_norm"),
		})
	}
	for i := range m.cfg.DecoderLayers {
		p := fmt.Sprintf("model.decoder.layers.%d", i)
		f := ffn(p)
		m.decoder = append(m.decoder, decoderLayer{
			self: attn(p+".self_attn", m.cfg.DecoderHeads), selfNorm: norm(p + ".self_attn_layer_norm"),
			cross: attn(p+".encoder_attn", m.cfg.DecoderHeads), crossNorm: norm(p + ".encoder_attn_layer_norm"),
			fc1: lin(p+".fc1", d, f), fc2: lin(p+".fc2", f, d), finalLayerNorm: norm(p + ".final_layer_norm"),
		})
	}
	return missing
}

// SetThreads caps how many goroutines each decoding step starts (0: one
// per CPU, the default). When several sentences are translated at once,
// give each its share of the CPUs, or every step of every sentence starts
// a team of goroutines and they oversubscribe the machine. It must be
// called before the first Translate.
func (m *Model) SetThreads(n int) { m.threads = n }

// maxSourceTokens leaves room for the end-of-sentence token.
func (m *Model) maxSourceTokens() int { return m.cfg.MaxPositions - 1 }

// Translate translates one text. Long texts are better split into
// sentences first: the models are trained on sentences.
func (m *Model) Translate(ctx context.Context, text string) (string, error) {
	pieces := m.source.encode(text)
	if len(pieces) == 0 {
		return "", nil
	}
	ids := make([]int, 0, len(pieces)+1)
	for _, p := range pieces {
		id, ok := m.ids[p]
		if !ok {
			id = m.unk
		}
		ids = append(ids, id)
	}
	if len(ids) > m.maxSourceTokens() {
		ids = ids[:m.maxSourceTokens()]
	}
	ids = append(ids, m.cfg.EOSToken)
	out, err := m.beamSearch(ctx, m.encode(ids), len(ids))
	if err != nil {
		return "", err
	}
	words := make([]string, 0, len(out))
	for _, id := range out {
		if id == m.cfg.EOSToken || id == m.cfg.PadToken || id == m.unk || id < 0 || id >= len(m.pieces) {
			continue
		}
		words = append(words, m.pieces[id])
	}
	return decodePieces(words), nil
}

// encode runs the encoder: one d-vector per source token.
func (m *Model) encode(ids []int) []float32 {
	d := m.cfg.DModel
	n := len(ids)
	x := make([]float32, n*d)
	for i, id := range ids {
		row := x[i*d : (i+1)*d]
		emb := m.embed[id*d : (id+1)*d]
		pos := m.encPos[i*d : (i+1)*d]
		for j := range row {
			row[j] = emb[j]*m.embedScale + pos[j]
		}
	}
	q := make([]float32, n*d)
	k := make([]float32, n*d)
	v := make([]float32, n*d)
	ctxv := make([]float32, n*d)
	tmp := make([]float32, n*d)
	scores := make([]float32, n)
	ffn := 0 // the hidden layer of every encoder layer, allocated once
	for _, l := range m.encoder {
		ffn = max(ffn, l.fc1.out)
	}
	hidden := make([]float32, n*ffn)
	for _, l := range m.encoder {
		l.self.q.rows(q, x, n)
		l.self.k.rows(k, x, n)
		l.self.v.rows(v, x, n)
		for i := range n {
			attend(ctxv[i*d:(i+1)*d], q[i*d:(i+1)*d], k, v, n, l.self.heads, scores)
		}
		l.self.o.rows(tmp, ctxv, n)
		addNorm(x, tmp, l.selfNorm, n)
		h := hidden[:n*l.fc1.out]
		l.fc1.rows(h, x, n)
		for i := range h {
			h[i] = m.activation(h[i])
		}
		l.fc2.rows(tmp, h, n)
		addNorm(x, tmp, l.finalLayerNorm, n)
	}
	return x
}

// rows applies the layer to n row vectors: dst = x·Wᵀ + b.
func (l linear) rows(dst, x []float32, n int) {
	for i := range n {
		l.apply(dst[i*l.out:(i+1)*l.out], x[i*l.in:(i+1)*l.in])
	}
}

func (l linear) apply(dst, x []float32) {
	for o := range l.out {
		dst[o] = dot(x, l.w[o*l.in:(o+1)*l.in]) + l.b[o]
	}
}

// attend computes multi-head scaled dot-product attention of one query
// over n keys and values (n × d each).
func attend(dst, q, k, v []float32, n, heads int, scores []float32) {
	d := len(q)
	hd := d / heads
	scale := float32(1 / math.Sqrt(float64(hd)))
	clear(dst)
	for h := range heads {
		lo, hi := h*hd, (h+1)*hd
		maxScore := float32(math.Inf(-1))
		for j := range n {
			s := dot(q[lo:hi], k[j*d+lo:j*d+hi]) * scale
			scores[j] = s
			maxScore = max(maxScore, s)
		}
		var sum float32
		for j := range n {
			scores[j] = float32(math.Exp(float64(scores[j] - maxScore)))
			sum += scores[j]
		}
		out := dst[lo:hi]
		for j := range n {
			p := scores[j] / sum
			row := v[j*d+lo : j*d+hi]
			for c := range out {
				out[c] += p * row[c]
			}
		}
	}
}

// addNorm sets x = LayerNorm(x + y) for n rows.
func addNorm(x, y []float32, ln layerNorm, n int) {
	d := len(ln.g)
	for i := range n {
		row := x[i*d : (i+1)*d]
		add := y[i*d : (i+1)*d]
		var mean float64
		for j := range row {
			row[j] += add[j]
			mean += float64(row[j])
		}
		mean /= float64(d)
		var variance float64
		for _, val := range row {
			diff := float64(val) - mean
			variance += diff * diff
		}
		inv := 1 / math.Sqrt(variance/float64(d)+1e-5)
		for j := range row {
			row[j] = float32((float64(row[j])-mean)*inv)*ln.g[j] + ln.b[j]
		}
	}
}

// dot is the hot loop of the whole model: unrolled, with independent
// accumulators so the CPU can overlap the additions.
func dot(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		s0 += a[i]*b[i] + a[i+4]*b[i+4]
		s1 += a[i+1]*b[i+1] + a[i+5]*b[i+5]
		s2 += a[i+2]*b[i+2] + a[i+6]*b[i+6]
		s3 += a[i+3]*b[i+3] + a[i+7]*b[i+7]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}
