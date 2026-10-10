package live

import "math"

// Resampler converts a stream of 16-bit mono samples from one rate to another by linear interpolation,
// carrying its place from one chunk to the next so that the joins are not heard. The model's voice
// arrives in chunks of a tenth of a second; converting each alone would click ten times a second.
type Resampler struct {
	step  float64 // input samples per output sample
	carry []int16
	pos   float64 // where the next output sample falls, in input samples from carry[0]
}

// NewResampler converts from one rate to another.
func NewResampler(from, to int) *Resampler {
	return &Resampler{step: float64(from) / float64(to)}
}

// Push takes the next chunk and returns the output samples it completes.
func (r *Resampler) Push(in []int16) []int16 {
	buf := append(r.carry, in...)
	var out []int16
	for {
		j := int(r.pos)
		if j+1 >= len(buf) {
			break
		}
		frac := r.pos - float64(j)
		out = append(out, int16(float64(buf[j])*(1-frac)+float64(buf[j+1])*frac))
		r.pos += r.step
	}
	j := min(int(r.pos), len(buf))
	r.carry = append([]int16(nil), buf[j:]...)
	r.pos -= float64(j)
	return out
}

// Reset forgets the chunk before, for the start of a new stretch of sound.
func (r *Resampler) Reset() { r.carry, r.pos = nil, 0 }

// PCM16 reads little-endian 16-bit samples.
func PCM16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
	}
	return out
}

// Bytes writes samples as little-endian 16-bit.
func Bytes(s []int16) []byte {
	out := make([]byte, 2*len(s))
	for i, v := range s {
		out[2*i], out[2*i+1] = byte(v), byte(uint16(v)>>8)
	}
	return out
}

// RMS is the root mean square of the samples, on the scale of the samples themselves.
func RMS(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}
