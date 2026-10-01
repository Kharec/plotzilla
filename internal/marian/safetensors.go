package marian

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"unsafe"
)

// tensor is a weight matrix or vector read from a safetensors file, in
// row-major order.
type tensor struct {
	shape []int
	data  []float32
}

type tensorInfo struct {
	DType   string   `json:"dtype"`
	Shape   []int    `json:"shape"`
	Offsets [2]int64 `json:"data_offsets"`
}

// readSafetensors loads every tensor of a safetensors file (an 8-byte
// little-endian header length, a JSON header, then the raw data) as
// float32. F32 tensors are read straight into their slice: the file is
// large and a second copy would double the memory needed to load it.
func readSafetensors(path string) (map[string]tensor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var size uint64
	if err := binary.Read(f, binary.LittleEndian, &size); err != nil {
		return nil, fmt.Errorf("reading header size: %w", err)
	}
	if size > 100<<20 {
		return nil, fmt.Errorf("header too large (%d bytes)", size)
	}
	header := make([]byte, size)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	var infos map[string]json.RawMessage
	if err := json.Unmarshal(header, &infos); err != nil {
		return nil, fmt.Errorf("parsing header: %w", err)
	}
	base := int64(8 + size)
	out := make(map[string]tensor, len(infos))
	for name, raw := range infos {
		if name == "__metadata__" {
			continue
		}
		var info tensorInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			return nil, fmt.Errorf("tensor %s: %w", name, err)
		}
		n := 1
		for _, d := range info.Shape {
			n *= d
		}
		t, err := readTensor(f, base, info, n)
		if err != nil {
			return nil, fmt.Errorf("tensor %s: %w", name, err)
		}
		out[name] = tensor{shape: info.Shape, data: t}
	}
	return out, nil
}

func readTensor(f *os.File, base int64, info tensorInfo, n int) ([]float32, error) {
	start, end := info.Offsets[0], info.Offsets[1]
	width := map[string]int64{"F32": 4, "F16": 2, "BF16": 2}[info.DType]
	if width == 0 {
		return nil, fmt.Errorf("unsupported type %s", info.DType)
	}
	if end-start != int64(n)*width {
		return nil, fmt.Errorf("size %d does not match shape %v", end-start, info.Shape)
	}
	data := make([]float32, n)
	if n == 0 {
		return data, nil
	}
	if info.DType == "F32" {
		buf := unsafe.Slice((*byte)(unsafe.Pointer(&data[0])), n*4)
		if _, err := f.ReadAt(buf, base+start); err != nil {
			return nil, err
		}
		if !littleEndian {
			for i := range data {
				data[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
			}
		}
		return data, nil
	}
	buf := make([]byte, n*2)
	if _, err := f.ReadAt(buf, base+start); err != nil {
		return nil, err
	}
	for i := range data {
		h := binary.LittleEndian.Uint16(buf[i*2:])
		if info.DType == "BF16" {
			data[i] = math.Float32frombits(uint32(h) << 16)
		} else {
			data[i] = half(h)
		}
	}
	return data, nil
}

var littleEndian = func() bool {
	x := uint16(1)
	return *(*byte)(unsafe.Pointer(&x)) == 1
}()

// half converts an IEEE 754 half-precision float.
func half(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch {
	case exp == 0 && frac == 0:
		return math.Float32frombits(sign)
	case exp == 0: // subnormal
		v := float32(frac) / 1024 / 16384
		if sign != 0 {
			return -v
		}
		return v
	case exp == 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}
