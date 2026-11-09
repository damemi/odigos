// Package model scores URL path segments as "parameter" vs "static" with a
// logistic regression over hashed features. Offline training is train/train.py
// in github.com/odigos-io/URL_templatization_model; features() must stay
// byte-identical with that script.
package model

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"net/url"
	"strings"
)

//go:embed model.bin
var modelBin []byte

// model.bin layout (little endian): uint32 buckets, float32 bias, float32 threshold, float32 weights[buckets].
var bias, threshold, weights = load(modelBin)

func load(b []byte) (float32, float32, []float32) {
	n := binary.LittleEndian.Uint32(b)
	f := func(off int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(b[off:])) }
	w := make([]float32, n)
	for i := range w {
		w[i] = f(12 + 4*i)
	}
	return f(4), f(8), w
}

// Segments returns the path segments of a raw URL or path, ignoring query and empty segments.
func Segments(raw string) []string {
	if u, err := url.Parse(raw); err == nil {
		raw = u.Path
	}
	var segs []string
	for _, s := range strings.Split(raw, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// Probability returns P(segs[i] is a parameter), using its neighbours as context.
func Probability(segs []string, i int) float64 {
	z := float64(bias)
	// ponytail: allocates a string per feature; hash incrementally if this goes on the per-span hot path.
	for _, f := range features(segs, i) {
		z += float64(weights[bucket(f, len(weights))])
	}
	return 1 / (1 + math.Exp(-z))
}

// Template replaces every segment scoring >= the trained threshold with {id}.
func Template(raw string) string {
	segs := Segments(raw)
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s
		if Probability(segs, i) >= float64(threshold) {
			out[i] = "{id}"
		}
	}
	return "/" + strings.Join(out, "/")
}

// features must stay byte-identical to features() in URL_templatization_model/train/train.py.
func features(segs []string, i int) []string {
	s := segs[i]
	low := []rune(asciiLower(s))
	var f []string
	for n := 1; n <= 4; n++ {
		for j := 0; j+n <= len(low); j++ {
			f = append(f, fmt.Sprintf("c%d:%s", n, string(low[j:j+n])))
		}
	}
	f = append(f, "sh:"+prefix(shape(s), 12))
	for _, t := range splitKeepEmpty(string(low)) {
		f = append(f, "w:"+t)
	}
	prev, prev2, next := "<s>", "<s>", "</s>"
	if i > 0 {
		prev = asciiLower(segs[i-1])
	}
	if i > 1 {
		prev2 = asciiLower(segs[i-2])
	}
	if i+1 < len(segs) {
		next = asciiLower(segs[i+1])
	}
	return append(f,
		"p1:"+prev, "p2:"+prev2, "n1:"+next,
		fmt.Sprintf("pos:%d", min(i, 6)),
		fmt.Sprintf("last:%d", b2i(i == len(segs)-1)),
		fmt.Sprintf("p1s:%d", b2i(strings.HasSuffix(prev, "s"))),
		"p1sh:"+prefix(shape(prev), 8), "n1sh:"+prefix(shape(next), 8),
	)
}

// Python's str.lower() also folds non-ASCII; train.py restricts it to ASCII so both sides agree.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// shape collapses runs of digits/lower/upper to 0/a/A and keeps other runes, e.g. "cus_Nff12" -> "a_Aa0".
func shape(s string) string {
	var b strings.Builder
	var last rune
	for _, r := range s {
		var k rune
		switch {
		case '0' <= r && r <= '9':
			k = '0'
		case 'a' <= r && r <= 'z':
			k = 'a'
		case 'A' <= r && r <= 'Z':
			k = 'A'
		}
		if k == 0 {
			b.WriteRune(r)
		} else if k != last {
			b.WriteRune(k)
		}
		last = k
	}
	return b.String()
}

// splitKeepEmpty matches Python's re.split(r'[-_.]', s), which keeps empty fields.
func splitKeepEmpty(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r == '-' || r == '_' || r == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func prefix(s string, n int) string {
	r := []rune(s)
	return string(r[:min(n, len(r))])
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// bucket matches sklearn FeatureHasher: abs(murmur3_32(utf8, seed 0)) % n, with its INT_MIN special case.
// https://github.com/scikit-learn/scikit-learn/blob/1.9.1/sklearn/feature_extraction/_hashing_fast.pyx#L58-L65
func bucket(f string, n int) int {
	h := int32(murmur3([]byte(f)))
	if h == math.MinInt32 {
		return (math.MaxInt32 - (n - 1)) % n
	}
	if h < 0 {
		h = -h
	}
	return int(h) % n
}

// murmur3 is MurmurHash3_x86_32 with seed 0.
func murmur3(data []byte) uint32 {
	const c1, c2 = 0xcc9e2d51, 0x1b873593
	var h uint32
	n := len(data)
	for i := 0; i+4 <= n; i += 4 {
		k := binary.LittleEndian.Uint32(data[i:])
		k = bits.RotateLeft32(k*c1, 15) * c2
		h = bits.RotateLeft32(h^k, 13)*5 + 0xe6546b64
	}
	var k uint32
	tail := data[n&^3:]
	switch len(tail) {
	case 3:
		k ^= uint32(tail[2]) << 16
		fallthrough
	case 2:
		k ^= uint32(tail[1]) << 8
		fallthrough
	case 1:
		k ^= uint32(tail[0])
		h ^= bits.RotateLeft32(k*c1, 15) * c2
	}
	h ^= uint32(n)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}
