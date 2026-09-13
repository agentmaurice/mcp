package retrieve

import "math"

// EmbeddingQuantizer converts float embeddings into int8 and binary representations.
type EmbeddingQuantizer interface {
	ToInt8(embedding []float64) ([]int8, error)
	ToBinary(embedding []float64) ([]byte, error) // bit-packed
}

// LinearQuantizer implements symmetric linear quantization.
type LinearQuantizer struct{}

// NewLinearQuantizer creates a new quantizer.
func NewLinearQuantizer() *LinearQuantizer {
	return &LinearQuantizer{}
}

func (q *LinearQuantizer) ToInt8(embedding []float64) ([]int8, error) {
	if len(embedding) == 0 {
		return []int8{}, nil
	}
	maxAbs := 0.0
	for _, v := range embedding {
		av := math.Abs(v)
		if av > maxAbs {
			maxAbs = av
		}
	}
	if maxAbs == 0 {
		out := make([]int8, len(embedding))
		return out, nil
	}
	scale := 127.0 / maxAbs
	out := make([]int8, len(embedding))
	for i, v := range embedding {
		qv := math.Round(v * scale)
		if qv > 127 {
			qv = 127
		}
		if qv < -127 {
			qv = -127
		}
		out[i] = int8(qv)
	}
	return out, nil
}

func (q *LinearQuantizer) ToBinary(embedding []float64) ([]byte, error) {
	if len(embedding) == 0 {
		return []byte{}, nil
	}
	byteLen := (len(embedding) + 7) / 8
	out := make([]byte, byteLen)
	for i, v := range embedding {
		if v > 0 {
			byteIndex := i / 8
			bit := uint(i % 8)
			out[byteIndex] |= 1 << bit
		}
	}
	return out, nil
}
