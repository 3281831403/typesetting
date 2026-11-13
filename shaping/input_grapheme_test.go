package shaping

import (
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// excludeCombiningCmap wraps a cmap and reports no glyph for the combining
// marks it is given, so that a face built on top of it behaves like a primary
// font missing decomposed (NFD) diacritics while keeping every other glyph.
type excludeCombiningCmap struct {
	font.Cmap
	excluded map[rune]bool
}

func (c excludeCombiningCmap) Lookup(r rune) (font.GID, bool) {
	if c.excluded[r] {
		return 0, false
	}
	return c.Cmap.Lookup(r)
}

func newExcludedFace(t *testing.T, filename string, excluded ...rune) *font.Face {
	face := loadOpentypeFont(t, filename)
	ex := make(map[rune]bool, len(excluded))
	for _, r := range excluded {
		ex[r] = true
	}
	return font.NewFace(&font.Font{Cmap: excludeCombiningCmap{Cmap: face.Font.Cmap, excluded: ex}})
}

// nfdNguyen is "Nguyễn" in Unicode NFD: the ễ syllable is decomposed into
// 'e' + circumflex combining accent (U+0302) + tilde combining accent (U+0303).
var nfdNguyen = []rune{'N', 'g', 'u', 'y', 'e', '̂', '̃', 'n'}

// shapeEach shapes every run with the given shaper and returns the total
// number of glyphs and the total advance width.
func shapeEach(t *testing.T, shaper *HarfbuzzShaper, runs []Input) (int, fixed.Int26_6) {
	t.Helper()
	glyphCount := 0
	var advance fixed.Int26_6
	for _, run := range runs {
		out := shaper.Shape(run)
		glyphCount += len(out.Glyphs)
		advance += out.Advance
	}
	return glyphCount, advance
}

// TestSplitByFaceKeepsGraphemeClusters is the regression test for NFD names
// synced from the iOS address book: the combining marks of a decomposed
// syllable must stay in the same run, and be shaped by the same font, as their
// base letter. A face change is only legal on a grapheme cluster boundary.
func TestSplitByFaceKeepsGraphemeClusters(t *testing.T) {
	// Roboto covers the base letters and U+0303 but lacks U+0302; Amiri, the
	// fallback, covers the whole cluster.
	primary := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	fallback := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	faces := []*font.Face{primary, fallback}

	// sanity check of the bundled font coverage the test relies on
	_, hasCircumflex := primary.NominalGlyph('̂')
	tu.AssertC(t, !hasCircumflex, "Roboto is expected to lack U+0302")
	for _, r := range nfdNguyen {
		_, ok := fallback.NominalGlyph(r)
		tu.AssertC(t, ok, "Amiri should cover every rune of the NFD string")
	}

	t.Run("SplitByFontGlyphs", func(t *testing.T) {
		runs := SplitByFontGlyphs(Input{
			Text: nfdNguyen, RunStart: 0, RunEnd: len(nfdNguyen),
			Direction: di.DirectionLTR,
		}, faces)

		// the marked syllable [e U+0302 U+0303] is a single grapheme cluster
		// spanning runes 4..7 and must end up in one run assigned to the fallback
		var clusterRun *Input
		for i := range runs {
			r := &runs[i]
			// no run boundary may fall inside the cluster [4, 7)
			tu.AssertC(t, !(r.RunStart > 4 && r.RunStart < 7), "a run starts inside the grapheme cluster")
			tu.AssertC(t, !(r.RunEnd > 4 && r.RunEnd < 7), "a run ends inside the grapheme cluster")
			if r.RunStart == 4 && r.RunEnd == 7 {
				clusterRun = r
			}
		}
		tu.AssertC(t, clusterRun != nil, "the marked syllable is not a run on its own")
		tu.AssertC(t, clusterRun.Face == fallback, "the decomposed syllable must be shaped by the fallback font")
	})

	t.Run("Segmenter.Split", func(t *testing.T) {
		var seg Segmenter
		runs := seg.Split(Input{
			Text: nfdNguyen, RunStart: 0, RunEnd: len(nfdNguyen),
			Direction: di.DirectionLTR, Language: "vi",
		}, fixedFontmap(faces))

		var clusterRun *Input
		for i := range runs {
			r := &runs[i]
			tu.AssertC(t, !(r.RunStart > 4 && r.RunStart < 7), "a run starts inside the grapheme cluster")
			tu.AssertC(t, !(r.RunEnd > 4 && r.RunEnd < 7), "a run ends inside the grapheme cluster")
			if r.RunStart == 4 && r.RunEnd == 7 {
				clusterRun = r
			}
		}
		tu.AssertC(t, clusterRun != nil, "the marked syllable is not a run on its own")
		tu.AssertC(t, clusterRun.Face == fallback, "the decomposed syllable must be shaped by the fallback font")
	})

	// the same guarantee must hold when only a subslice of the paragraph is split
	t.Run("sub-range", func(t *testing.T) {
		padded := append([]rune("XX"), nfdNguyen...)
		padded = append(padded, 'X', 'X')
		runs := SplitByFontGlyphs(Input{
			Text: padded, RunStart: 2, RunEnd: 2 + len(nfdNguyen),
			Direction: di.DirectionLTR,
		}, faces)
		for _, r := range runs {
			tu.AssertC(t, !(r.RunStart > 6 && r.RunStart < 9), "a run starts inside the grapheme cluster")
			tu.AssertC(t, !(r.RunEnd > 6 && r.RunEnd < 9), "a run ends inside the grapheme cluster")
		}
	})
}

// TestSplitByFaceMatchesFallbackOnlyShaping verifies the user visible outcome:
// when the primary font cannot render any part of the text, every grapheme
// cluster resolves to the fallback font, and shaping the split runs produces
// the same glyph count and the same width as shaping the whole string with the
// fallback font only.
func TestSplitByFaceMatchesFallbackOnlyShaping(t *testing.T) {
	fallback := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	// a primary font supporting none of the Vietnamese syllable runes
	primary := newExcludedFace(t, "../font/testdata/Roboto-Regular.ttf", nfdNguyen...)
	faces := []*font.Face{primary, fallback}

	shaper := &HarfbuzzShaper{}
	base := Input{
		Text: nfdNguyen, RunStart: 0, RunEnd: len(nfdNguyen),
		Direction: di.DirectionLTR, Size: fixed.I(16),
		Script: language.Latin, Language: "vi",
	}

	reference := shaper.Shape(func() Input {
		in := base
		in.Face = fallback
		return in
	}())

	for _, tc := range []struct {
		name string
		runs []Input
	}{
		{"SplitByFontGlyphs", SplitByFontGlyphs(base, faces)},
		{"SplitByFace", SplitByFace(base, fixedFontmap(faces))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// every run must use the fallback font
			for _, r := range tc.runs {
				tu.AssertC(t, r.Face == fallback, "the run is not assigned to the fallback font")
			}
			glyphCount, advance := shapeEach(t, shaper, tc.runs)
			tu.AssertC(t, glyphCount == len(reference.Glyphs), "glyph count differs from the fallback-only reference")
			tu.AssertC(t, advance == reference.Advance, "line width differs from the fallback-only reference")
		})
	}
}

// TestSplitByFaceMixedLayoutUnchanged makes sure ordinary mixed-script
// segmentation, where no grapheme cluster straddles a font change, keeps the
// exact run boundaries it had before the fix.
func TestSplitByFaceMixedLayoutUnchanged(t *testing.T) {
	latinFont := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	arabicFont := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	text := []rune("Hello " + "تثذرزسشص" + "world")

	runs := SplitByFontGlyphs(Input{
		Text: text, RunStart: 0, RunEnd: len(text),
		Direction: di.DirectionLTR,
	}, []*font.Face{latinFont, arabicFont})

	expected := []struct {
		start, end int
		face       *font.Face
	}{
		{0, 6, latinFont},
		{6, 14, arabicFont},
		{14, 19, latinFont},
	}
	tu.AssertC(t, len(runs) == len(expected), "unexpected number of runs")
	for i, want := range expected {
		got := runs[i]
		tu.AssertC(t, got.RunStart == want.start && got.RunEnd == want.end, "run boundaries changed")
		tu.AssertC(t, got.Face == want.face, "run face changed")
	}
}

// TestPlainFontmapStillSplitsOnClusters checks that a [Fontmap] which does not
// implement [FontmapGraphemeCluster] still gets its face changes aligned to
// grapheme cluster boundaries: the marks are resolved together with their base
// letter, never as standalone runs.
func TestPlainFontmapStillSplitsOnClusters(t *testing.T) {
	// a primary font with a hole exactly on U+0302
	primary := newExcludedFace(t, "../font/testdata/Roboto-Regular.ttf", '̂')
	fallback := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")

	runs := SplitByFace(Input{
		Text: nfdNguyen, RunStart: 0, RunEnd: len(nfdNguyen),
		Direction: di.DirectionLTR,
	}, plainOnlyFontmap{primary, fallback})

	for _, r := range runs {
		tu.AssertC(t, !(r.RunStart > 4 && r.RunStart < 7), "a run starts inside the grapheme cluster")
		tu.AssertC(t, !(r.RunEnd > 4 && r.RunEnd < 7), "a run ends inside the grapheme cluster")
	}
}

// plainOnlyFontmap deliberately implements only [Fontmap], to exercise the
// fallback path for font maps without cluster aware resolution.
type plainOnlyFontmap []*font.Face

func (ff plainOnlyFontmap) ResolveFace(r rune) *font.Face {
	for _, f := range ff {
		if _, has := f.NominalGlyph(r); has {
			return f
		}
	}
	return ff[0]
}

// TestSplitKeepsClusterAcrossUpstreamBoundary covers the case where an earlier
// segmentation stage (bidi, script or vertical orientation) places a boundary
// inside a grapheme cluster. The face stage must absorb that boundary rather
// than shape a base character and its combining mark in two different runs.
func TestSplitKeepsClusterAcrossUpstreamBoundary(t *testing.T) {
	primary := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	fallback := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	faces := fixedFontmap{primary, fallback}

	// a digit followed by the Hebrew point U+05BB (a combining mark): the two
	// runes form one grapheme cluster but have different resolved scripts
	text := []rune{'0', 'ֻ'}

	var gs segmenter.Segmenter
	gs.Init(text)
	tu.AssertC(t, !gs.IsGraphemeBoundary(1), "the two runes are expected to form one grapheme cluster")

	var seg Segmenter
	runs := seg.Split(Input{
		Text: text, RunStart: 0, RunEnd: len(text),
		Direction: di.DirectionLTR,
	}, faces)

	// the whole cluster must be a single run
	tu.AssertC(t, len(runs) == 1, "the cluster was split across an upstream script boundary")
	tu.AssertC(t, runs[0].RunStart == 0 && runs[0].RunEnd == 2, "the run does not cover the whole cluster")
}

// TestSplitByFaceClusterSequences covers grapheme clusters joined by ZWJ and
// regional indicators: none of their internal boundaries may become face run
// boundaries, and plain whitespace only inputs must still get a face.
func TestSplitByFaceClusterSequences(t *testing.T) {
	primary := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	fallback := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	faces := []*font.Face{primary, fallback}

	for _, tc := range []struct {
		name       string
		text       []rune
		wantNRuns  int
		wantSingle bool // the whole text must be a single run
	}{
		// the ZWJ emoji family is one extended grapheme cluster
		{"zwj emoji family", []rune{'\U0001F468', '‍', '\U0001F469', '‍', '\U0001F467'}, 1, true},
		// a flag is two regional indicators forming one cluster
		{"flag", []rune{'🇫', '🇷'}, 1, true},
		// one base letter with three stacked combining marks is one cluster
		{"base with three marks", []rune{'y', '̂', '̃', '̀'}, 1, true},
		// whitespace only input must still be assigned a face, exactly one run
		{"all whitespace", []rune("   "), 1, true},
		// two independent decomposed clusters may be split between fonts,
		// but never inside a cluster
		{"two decomposed clusters", []rune{'a', '̀', 'e', '́'}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := SplitByFontGlyphs(Input{
				Text: tc.text, RunStart: 0, RunEnd: len(tc.text),
				Direction: di.DirectionLTR,
			}, faces)

			if tc.wantSingle {
				tu.AssertC(t, len(runs) == 1, "expected a single run")
				got := runs[0]
				tu.AssertC(t, got.RunStart == 0 && got.RunEnd == len(tc.text), "the run does not cover the whole text")
			}
			if tc.wantNRuns != 0 {
				tu.AssertC(t, len(runs) == tc.wantNRuns, "unexpected number of runs")
			}

			// general invariant: every run boundary is a grapheme boundary
			var gs segmenter.Segmenter
			gs.Init(tc.text)
			for _, r := range runs {
				tu.AssertC(t, gs.IsGraphemeBoundary(r.RunStart), "run start is not a grapheme boundary")
				tu.AssertC(t, gs.IsGraphemeBoundary(r.RunEnd), "run end is not a grapheme boundary")
			}
		})
	}
}
