package marian

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------- test helpers

func protoKey(field, wire int) []byte { return binary.AppendUvarint(nil, uint64(field<<3|wire)) }

func protoBytes(field int, b []byte) []byte {
	out := append(protoKey(field, 2), binary.AppendUvarint(nil, uint64(len(b)))...)
	return append(out, b...)
}

func protoVarint(field int, v uint64) []byte {
	return append(protoKey(field, 0), binary.AppendUvarint(nil, v)...)
}

func protoFloat(field int, f float32) []byte {
	return binary.LittleEndian.AppendUint32(protoKey(field, 5), math.Float32bits(f))
}

type testPiece struct {
	piece string
	score float32
	typ   int
}

// spmModel serialises a SentencePiece ModelProto of the given type.
func spmModel(modelType int, pieces []testPiece) []byte {
	var out []byte
	for _, p := range pieces {
		var msg []byte
		msg = append(msg, protoBytes(1, []byte(p.piece))...)
		msg = append(msg, protoFloat(2, p.score)...)
		msg = append(msg, protoVarint(3, uint64(p.typ))...)
		out = append(out, protoBytes(1, msg)...)
	}
	out = append(out, protoBytes(2, protoVarint(3, uint64(modelType)))...)
	return out
}

var pieces = []testPiece{
	{"<unk>", 0, pieceUnknown}, {"</s>", 0, pieceControl},
	{"▁hello", -1, pieceNormal}, {"▁he", -2, pieceNormal}, {"llo", -2, pieceNormal}, {"▁", -3, pieceNormal},
	{"h", -5, pieceNormal}, {"e", -5, pieceNormal}, {"l", -5, pieceNormal}, {"o", -5, pieceNormal},
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type testTensor struct {
	name  string
	shape []int
	data  []float32
}

func writeSafetensors(t *testing.T, path string, tensors []testTensor) {
	t.Helper()
	header := map[string]any{"__metadata__": map[string]string{"format": "pt"}}
	var data []byte
	for _, x := range tensors {
		start := len(data)
		for _, f := range x.data {
			data = binary.LittleEndian.AppendUint32(data, math.Float32bits(f))
		}
		header[x.name] = map[string]any{"dtype": "F32", "shape": x.shape, "data_offsets": []int{start, len(data)}}
	}
	h, _ := json.Marshal(header)
	out := binary.LittleEndian.AppendUint64(nil, uint64(len(h)))
	writeFile(t, path, append(append(out, h...), data...))
}

// ----------------------------------------------------------------- tests

func TestSentencePiece(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.spm")
	writeFile(t, path, spmModel(1, pieces))
	sp, err := loadSentencePiece(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]string{
		"hello":        {"▁hello"},
		"  hello hel ": {"▁hello", "▁he", "l"},
		"helloz":       {"▁hello", "z"},
		"":             nil,
	}
	for in, want := range tests {
		if got := sp.encode(in); !reflect.DeepEqual(got, want) {
			t.Errorf("encode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := decodePieces([]string{"▁Hello", ",", "▁world"}); got != "Hello, world" {
		t.Errorf("decodePieces = %q", got)
	}

	writeFile(t, path, spmModel(2, pieces))
	if _, err := loadSentencePiece(path); err == nil || !strings.Contains(err.Error(), "unigram") {
		t.Errorf("BPE model: err = %v", err)
	}
	writeFile(t, path, []byte{0x0a, 0xff})
	if _, err := loadSentencePiece(path); err == nil {
		t.Error("malformed model accepted")
	}
}

func TestSafetensors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.safetensors")
	writeSafetensors(t, path, []testTensor{{"w", []int{2, 2}, []float32{1, -2, 3.5, 0}}})
	got, err := readSafetensors(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if w := got["w"]; !reflect.DeepEqual(w.shape, []int{2, 2}) || !reflect.DeepEqual(w.data, []float32{1, -2, 3.5, 0}) {
		t.Errorf("w = %+v", w)
	}

	// F16 and BF16 tensors are widened.
	h := []byte(`{"a":{"dtype":"F16","shape":[2],"data_offsets":[0,4]},"b":{"dtype":"BF16","shape":[1],"data_offsets":[4,6]}}`)
	data := binary.LittleEndian.AppendUint64(nil, uint64(len(h)))
	data = append(data, h...)
	data = append(data, 0x00, 0x3c, 0x00, 0xc0, 0x80, 0x3f) // 1, -2 (F16), 1 (BF16)
	writeFile(t, path, data)
	got, err = readSafetensors(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["a"].data, []float32{1, -2}) || !reflect.DeepEqual(got["b"].data, []float32{1}) {
		t.Errorf("a = %v, b = %v", got["a"].data, got["b"].data)
	}

	h = []byte(`{"a":{"dtype":"F32","shape":[3],"data_offsets":[0,4]}}`)
	writeFile(t, path, append(binary.LittleEndian.AppendUint64(nil, uint64(len(h))), append(h, 0, 0, 0, 0)...))
	if _, err := readSafetensors(context.Background(), path); err == nil {
		t.Error("shape mismatch accepted")
	}
}

func TestHalf(t *testing.T) {
	tests := map[uint16]float32{0x3c00: 1, 0xc000: -2, 0x7bff: 65504, 0x0001: 1.0 / (1 << 24), 0x8000: float32(math.Copysign(0, -1))}
	for h, want := range tests {
		if got := half(h); got != want {
			t.Errorf("half(%#04x) = %v, want %v", h, got, want)
		}
	}
	if !math.IsInf(float64(half(0x7c00)), 1) {
		t.Error("half(0x7c00) is not +Inf")
	}
}

func TestTopK(t *testing.T) {
	if got := topK([]float32{0.1, 0.9, 0.3, 0.7, 0.9}, 3); !reflect.DeepEqual(got, []int{1, 4, 3}) {
		t.Errorf("topK = %v", got)
	}
	if got := topK([]float32{1}, 3); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("topK of one = %v", got)
	}
}

// tinyModel writes a random model of the real architecture, small enough
// to run in a test.
func tinyModel(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const d, ffn, vocab, positions = 8, 16, 12, 32
	cfg := fmt.Sprintf(`{"d_model":%d,"encoder_layers":1,"decoder_layers":1,"encoder_attention_heads":2,
		"decoder_attention_heads":2,"activation_function":"swish","scale_embedding":true,"normalize_before":false,
		"max_position_embeddings":%d,"vocab_size":%d,"pad_token_id":%d,"eos_token_id":0,"decoder_start_token_id":%d,"num_beams":2}`,
		d, positions, vocab, vocab-1, vocab-1)
	writeFile(t, filepath.Join(dir, "config.json"), []byte(cfg))
	writeFile(t, filepath.Join(dir, "source.spm"), spmModel(1, pieces))
	writeFile(t, filepath.Join(dir, "target.spm"), spmModel(1, pieces))
	ids := map[string]int{"</s>": 0, "<unk>": 1, "<pad>": vocab - 1}
	for i, p := range pieces[2:] {
		ids[p.piece] = i + 2
	}
	v, _ := json.Marshal(ids)
	writeFile(t, filepath.Join(dir, "vocab.json"), v)

	rng := rand.New(rand.NewSource(1))
	random := func(n int) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = float32(rng.NormFloat64() * 0.5)
		}
		return out
	}
	ones := func(n int) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = 1
		}
		return out
	}
	var tensors []testTensor
	add := func(name string, data []float32, shape ...int) {
		tensors = append(tensors, testTensor{name, shape, data})
	}
	lin := func(p string, in, out int) {
		add(p+".weight", random(in*out), out, in)
		add(p+".bias", random(out), out)
	}
	norm := func(p string) {
		add(p+".weight", ones(d), d)
		add(p+".bias", make([]float32, d), d)
	}
	attn := func(p string) {
		for _, x := range []string{"q_proj", "k_proj", "v_proj", "out_proj"} {
			lin(p+"."+x, d, d)
		}
	}
	add("model.shared.weight", random(vocab*d), vocab, d)
	add("model.encoder.embed_positions.weight", random(positions*d), positions, d)
	add("model.decoder.embed_positions.weight", random(positions*d), positions, d)
	add("final_logits_bias", make([]float32, vocab), 1, vocab)
	attn("model.encoder.layers.0.self_attn")
	norm("model.encoder.layers.0.self_attn_layer_norm")
	lin("model.encoder.layers.0.fc1", d, ffn)
	lin("model.encoder.layers.0.fc2", ffn, d)
	norm("model.encoder.layers.0.final_layer_norm")
	attn("model.decoder.layers.0.self_attn")
	norm("model.decoder.layers.0.self_attn_layer_norm")
	attn("model.decoder.layers.0.encoder_attn")
	norm("model.decoder.layers.0.encoder_attn_layer_norm")
	lin("model.decoder.layers.0.fc1", d, ffn)
	lin("model.decoder.layers.0.fc2", ffn, d)
	norm("model.decoder.layers.0.final_layer_norm")
	writeSafetensors(t, filepath.Join(dir, "model.safetensors"), tensors)
	return dir
}

func TestTinyModel(t *testing.T) {
	dir := tinyModel(t)
	if !Installed(dir) {
		t.Fatal("tiny model not installed")
	}
	m, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Translate(context.Background(), "hello hello")
	if err != nil {
		t.Fatal(err)
	}
	// Random weights: the output means nothing, but it is deterministic
	// and made of target pieces.
	again, _ := m.Translate(context.Background(), "hello hello")
	if first != again {
		t.Errorf("translations differ: %q, %q", first, again)
	}
	if strings.ContainsAny(first, "▁<>") {
		t.Errorf("translation %q contains special pieces", first)
	}
	if out, _ := m.Translate(context.Background(), "   "); out != "" {
		t.Errorf("blank text translated to %q", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Translate(ctx, "hello"); err == nil {
		t.Error("cancelled translation succeeded")
	}

	os.Remove(filepath.Join(dir, "vocab.json"))
	if Installed(dir) {
		t.Error("incomplete model reported installed")
	}
	if _, err := Load(context.Background(), dir); err == nil {
		t.Error("incomplete model loaded")
	}
}

// TestRealModel runs the OPUS-MT model when PLOTZILLA_MT_MODEL names its
// directory (~/.local/share/plotzilla/models/opus-mt-fr-en once
// downloaded by the app).
func TestRealModel(t *testing.T) {
	dir := os.Getenv("PLOTZILLA_MT_MODEL")
	if dir == "" {
		t.Skip("PLOTZILLA_MT_MODEL not set")
	}
	m, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"Bonjour, comment allez-vous ?":            "Hello, how are you?",
		"Je t'avais dit de ne jamais revenir ici.": "I told you never to come back here.",
	}
	for in, want := range tests {
		if got, err := m.Translate(context.Background(), in); err != nil || got != want {
			t.Errorf("Translate(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
}

func TestDownload(t *testing.T) {
	content := []byte("model weights")
	sum := sha256.Sum256(content)
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/full": // a server without resume support
			w.Write(content)
		default:
			var off int
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &off); err == nil && off > 0 && off < len(content) {
				w.WriteHeader(http.StatusPartialContent)
				w.Write(content[off:])
				return
			}
			w.Write(content)
		}
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "model")
	file := RemoteFile{Name: "f.bin", URL: srv.URL + "/f", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}

	var last int64
	if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{file}, func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f.bin")); string(got) != string(content) || last != file.Size {
		t.Errorf("downloaded %q, progress %d", got, last)
	}

	// A file already downloaded and verified is skipped.
	if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{file}, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("a complete file was downloaded again (%d requests)", requests)
	}

	// An interrupted download resumes its partial file.
	resumed := file
	resumed.Name, resumed.URL = "r.bin", srv.URL+"/r"
	writeFile(t, filepath.Join(dir, ".r.bin.partial"), content[:6])
	if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{resumed}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "r.bin")); string(got) != string(content) {
		t.Errorf("resumed download = %q", got)
	}

	// A server that cannot resume answers the whole file: the partial
	// download is restarted.
	full := file
	full.Name, full.URL = "full.bin", srv.URL+"/full"
	writeFile(t, filepath.Join(dir, ".full.bin.partial"), content[:6])
	if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{full}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "full.bin")); string(got) != string(content) {
		t.Errorf("restarted download = %q", got)
	}

	// A corrupt partial file fails the digest check and is removed: the
	// next download starts from scratch.
	corrupt := file
	corrupt.Name, corrupt.URL = "c.bin", srv.URL+"/c"
	writeFile(t, filepath.Join(dir, ".c.bin.partial"), []byte("garbag"))
	if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{corrupt}, nil); err == nil {
		t.Error("corrupt partial file accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ".c.bin.partial")); err == nil {
		t.Error("corrupt partial file kept")
	}
	if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{corrupt}, nil); err != nil {
		t.Fatal(err)
	}

	bad := file
	bad.Name, bad.SHA256 = "bad.bin", strings.Repeat("0", 64)
	missing := file
	missing.Name, missing.URL = "missing.bin", srv.URL+"/missing"
	short := file
	short.Name, short.Size = "short.bin", file.Size+5
	for _, f := range []RemoteFile{bad, missing, short} {
		if err := Download(context.Background(), srv.Client(), dir, []RemoteFile{f}, nil); err == nil {
			t.Errorf("%s: no error", f.Name)
		}
		if _, err := os.Stat(filepath.Join(dir, f.Name)); err == nil {
			t.Errorf("%s: a failed download left the file", f.Name)
		}
	}
	// Only the interrupted download keeps a partial file, to be resumed.
	for _, f := range []RemoteFile{bad, missing} {
		if _, err := os.Stat(filepath.Join(dir, "."+f.Name+".partial")); err == nil {
			t.Errorf("%s: a failed download left a partial file", f.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".short.bin.partial")); err != nil {
		t.Error("an interrupted download left no partial file to resume")
	}
}
