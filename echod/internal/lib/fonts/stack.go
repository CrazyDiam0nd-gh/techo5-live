package fonts

import (
	"image"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Layer is one face in a Stack, with the font it was made from, which says which characters it has
// a glyph for. A layer with no Font is taken to have every character: the last resort.
type Layer struct {
	Face font.Face
	Font *sfnt.Font
}

// Stack is a face drawn from several: each character from the first layer that has a glyph for it.
// The chosen font comes first and Go after it, so a character the chosen font lacks ("♪", say) is
// still drawn rather than left blank or boxed; another script's font can go in between the same way.
// A character no layer has is the first layer's to draw, as it would be on its own.
//
// Line metrics (ascent, descent, height) are the metrics face's when there is one, else the first
// layer's: a page laid out for one font's line spacing keeps it whichever font draws the letters.
//
// Kerning applies only between two characters drawn from the same layer. A Stack is safe to use from
// more than one goroutine; the faces in it are only used under its lock.
type Stack struct {
	mu      sync.Mutex
	buf     sfnt.Buffer
	layers  []Layer
	metrics font.Face
}

// NewStack makes a Stack of layers, in the order they are tried, with metrics' line metrics (nil for
// the first layer's).
func NewStack(metrics font.Face, layers ...Layer) *Stack {
	return &Stack{layers: layers, metrics: metrics}
}

// pick is the layer that draws r. Called under s.mu.
func (s *Stack) pick(r rune) font.Face {
	for _, l := range s.layers {
		if s.has(l, r) {
			return l.Face
		}
	}
	if len(s.layers) == 0 {
		return nil
	}
	return s.layers[0].Face
}

// has is whether l has a glyph for r. Called under s.mu.
func (s *Stack) has(l Layer, r rune) bool {
	if l.Font == nil {
		return true
	}
	i, err := l.Font.GlyphIndex(&s.buf, r)
	return err == nil && i != 0
}

// Has is whether any layer has a glyph for r: whether the stack draws it as itself.
func (s *Stack) Has(r rune) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.layers {
		if s.has(l, r) {
			return true
		}
	}
	return false
}

func (s *Stack) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.pick(r)
	if f == nil {
		return image.Rectangle{}, nil, image.Point{}, 0, false
	}
	return f.Glyph(dot, r)
}

func (s *Stack) GlyphBounds(r rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.pick(r)
	if f == nil {
		return fixed.Rectangle26_6{}, 0, false
	}
	return f.GlyphBounds(r)
}

func (s *Stack) GlyphAdvance(r rune) (fixed.Int26_6, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.pick(r)
	if f == nil {
		return 0, false
	}
	return f.GlyphAdvance(r)
}

func (s *Stack) Kern(r0, r1 rune) fixed.Int26_6 {
	s.mu.Lock()
	defer s.mu.Unlock()
	f0, f1 := s.pick(r0), s.pick(r1)
	if f0 == nil || f0 != f1 {
		return 0
	}
	return f0.Kern(r0, r1)
}

func (s *Stack) Metrics() font.Metrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics != nil {
		return s.metrics.Metrics()
	}
	if len(s.layers) == 0 {
		return font.Metrics{}
	}
	return s.layers[0].Face.Metrics()
}

// Close closes every layer's face and the metrics face, each once.
func (s *Stack) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	shared := false
	for _, l := range s.layers {
		shared = shared || l.Face == s.metrics
		if err := l.Face.Close(); err != nil && first == nil {
			first = err
		}
	}
	if s.metrics != nil && !shared {
		if err := s.metrics.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
