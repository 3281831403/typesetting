package shaping

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"golang.org/x/image/math/fixed"
)

// mixedDoc returns a mixed-script paragraph of n runes, with a break
// opportunity at almost every rune (CJK), like reader documents.
func mixedDoc(n int) []rune {
	unit := []rune("混排稿件 reader 打开一份 table 数据 ")
	text := make([]rune, 0, n)
	for len(text) < n {
		text = append(text, unit...)
	}
	return text[:n]
}

func shapeForBench(text []rune) Output {
	var shaper HarfbuzzShaper
	return shaper.Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: di.DirectionLTR,
		Face:      benchEnFace,
		Size:      fixed.I(16),
		Script:    language.Latin,
		Language:  language.NewLanguage("EN"),
	})
}

// BenchmarkWrappingScalability wraps single paragraphs of doubling length,
// checking that wrapping stays near-linear in the input size. It covers the
// two cases that used to be quadratic in the number of break candidates per
// line: measuring at natural width (a huge maxWidth, so that the whole
// paragraph fits on one line) and long runs of characters like '#' or '%'
// (a break opportunity between every pair, like table separator lines).
func BenchmarkWrappingScalability(b *testing.B) {
	for _, n := range []int{2000, 4000, 8000, 16000} {
		b.Run(fmt.Sprintf("runes=%d/naturalWidth", n), func(b *testing.B) {
			text := mixedDoc(n)
			out := shapeForBench(text)
			outs := cutRunInto(out, 16) // mixed-script text produces many runs
			var l LineWrapper
			iter := NewSliceIterator(outs)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				lines, _ := l.WrapParagraph(WrapConfig{}, 1<<30, text, iter)
				iter.(*shapedRunSlice).Reset(outs)
				_ = lines
			}
		})
		b.Run(fmt.Sprintf("runes=%d/hashRun", n), func(b *testing.B) {
			text := []rune(strings.Repeat("#", n))
			out := shapeForBench(text)
			outs := []Output{out}
			var l LineWrapper
			iter := NewSliceIterator(outs)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				lines, _ := l.WrapParagraph(WrapConfig{}, 250, text, iter)
				iter.(*shapedRunSlice).Reset(outs)
				_ = lines
			}
		})
		b.Run(fmt.Sprintf("runes=%d/column", n), func(b *testing.B) {
			text := mixedDoc(n)
			out := shapeForBench(text)
			outs := cutRunInto(out, 16)
			var l LineWrapper
			iter := NewSliceIterator(outs)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				lines, _ := l.WrapParagraph(WrapConfig{}, 250, text, iter)
				iter.(*shapedRunSlice).Reset(outs)
				_ = lines
			}
		})
	}
}

// singleFaceFontmap is a Fontmap resolving every rune to the same face.
type singleFaceFontmap struct{ face *font.Face }

func (s singleFaceFontmap) ResolveFace(rune) *font.Face { return s.face }

// BenchmarkReaderPipeline measures the full reader pipeline (segment, split,
// shape, wrap) on a mixed-script document including table separator lines
// made of long '#' and '%' runs.
func BenchmarkReaderPipeline(b *testing.B) {
	para := "混排稿件的 reader 打开一份文档，包含 Latin 文本与 CJK 混排。The quick brown fox jumps over the lazy dog. "
	sep := strings.Repeat("#", 200) + "\n" + strings.Repeat("%", 200) + "\n"
	unit := []rune(para + sep)
	for _, n := range []int{50000, 100000, 200000} {
		text := make([]rune, 0, n)
		for len(text) < n {
			text = append(text, unit...)
		}
		text = text[:n]
		for _, width := range []int{1 << 30, 640} {
			b.Run(fmt.Sprintf("runes=%d/width=%d", n, width), func(b *testing.B) {
				faces := singleFaceFontmap{benchEnFace}
				var seg Segmenter
				var shaper HarfbuzzShaper
				var wrapper LineWrapper
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					input := Input{
						Text:      text,
						RunStart:  0,
						RunEnd:    len(text),
						Direction: di.DirectionLTR,
						Face:      benchEnFace,
						Size:      fixed.I(16),
						Script:    language.Han,
						Language:  language.NewLanguage("ZH"),
					}
					inputs := seg.Split(input, faces)
					outs := make([]Output, len(inputs))
					for k, in := range inputs {
						outs[k] = shaper.Shape(in)
					}
					wrapper.Prepare(WrapConfig{}, text, NewSliceIterator(outs))
					for done := false; !done; {
						_, done = wrapper.WrapNextLine(width)
					}
				}
			})
		}
	}
}
