package shaping

import (
	"github.com/go-text/typesetting/di"
	"golang.org/x/image/math/fixed"
)

// spacingAdvanceDelta returns the signed amount to add to a glyph advance to
// extend the text by spacing along the progression. Horizontal advances grow
// in the positive direction while vertical advances are negative (font space
// grows upward), so the vertical delta is negated: adding positive spacing to a
// vertical run makes its advance more negative and its column longer.
func spacingAdvanceDelta(direction di.Direction, spacing fixed.Int26_6) fixed.Int26_6 {
	if direction.IsVertical() {
		return -spacing
	}
	return spacing
}

// spacingOffsetDelta returns the signed amount to add to the main-axis glyph
// offset so that the glyph stays centered within its enlarged advance. Image
// space grows downward for vertical text (see drawVRun), hence the negation.
func spacingOffsetDelta(direction di.Direction, spacing fixed.Int26_6) fixed.Int26_6 {
	if direction.IsVertical() {
		return -spacing
	}
	return spacing
}

// AddWordSpacing alters the run, adding [additionalSpacing] on each
// word separator.
// [text] is the input slice used to create the run.
// Note that space is always added, even on boundaries.
//
// See also the convenience function [AddSpacing] to handle a slice of runs.
//
// See also https://www.w3.org/TR/css-text-3/#word-separator
func (run *Output) AddWordSpacing(text []rune, additionalSpacing fixed.Int26_6) {
	isVertical := run.Direction.IsVertical()
	advanceDelta := spacingAdvanceDelta(run.Direction, additionalSpacing)
	offsetDelta := spacingOffsetDelta(run.Direction, additionalSpacing/2)
	for i, g := range run.Glyphs {
		// find the corresponding runes :
		// to simplify, we assume a simple one to one rune/glyph mapping
		// which should be common in practice for word separators
		if !(g.RuneCount == 1 && g.GlyphCount == 1) {
			continue
		}
		r := text[g.ClusterIndex]
		switch r {
		case '\u0020', // space
			'\u00A0',                   // no-break space
			'\u1361',                   // Ethiopic word space
			'\U00010100', '\U00010101', // Aegean word separators
			'\U0001039F', // Ugaritic word divider
			'\U0001091F': // Phoenician word separator
		default:
			continue
		}
		// we have a word separator: add space
		// we do it by enlarging the separator glyph advance
		// and distributing space around the glyph content
		run.Glyphs[i].Advance += advanceDelta
		if isVertical {
			run.Glyphs[i].YAdvance += advanceDelta
			run.Glyphs[i].YOffset += offsetDelta
		} else {
			run.Glyphs[i].XAdvance += advanceDelta
			run.Glyphs[i].XOffset += offsetDelta
		}
	}
	run.RecomputeAdvance()
}

// AddLetterSpacing alters the run, adding [additionalSpacing] between
// each Harfbuzz clusters.
//
// Space is added at the boundaries if and only if there is an adjacent run, as specified by [isStartRun] and [isEndRun].
//
// See also the convenience function [AddSpacing] to handle a slice of runs.
//
// See also https://www.w3.org/TR/css-text-3/#letter-spacing-property
func (run *Output) AddLetterSpacing(additionalSpacing fixed.Int26_6, isStartRun, isEndRun bool) {
	isVertical := run.Direction.IsVertical()

	halfSpacing := additionalSpacing / 2
	advanceDelta := spacingAdvanceDelta(run.Direction, halfSpacing)
	offsetDelta := spacingOffsetDelta(run.Direction, halfSpacing)
	for startGIdx := 0; startGIdx < len(run.Glyphs); {
		startGlyph := run.Glyphs[startGIdx]
		endGIdx := startGIdx + startGlyph.GlyphCount - 1

		// start : apply spacing at boundary only if the run is not the first
		if startGIdx > 0 || !isStartRun {
			run.Glyphs[startGIdx].Advance += advanceDelta
			if isVertical {
				run.Glyphs[startGIdx].YAdvance += advanceDelta
				run.Glyphs[startGIdx].YOffset += offsetDelta
			} else {
				run.Glyphs[startGIdx].XAdvance += advanceDelta
				run.Glyphs[startGIdx].XOffset += offsetDelta
			}
			run.Glyphs[startGIdx].startLetterSpacing += halfSpacing
		}

		// end : apply spacing at boundary only if the run is not the last
		isLastCluster := startGIdx+startGlyph.GlyphCount >= len(run.Glyphs)
		if !isLastCluster || !isEndRun {
			run.Glyphs[endGIdx].Advance += advanceDelta
			if isVertical {
				run.Glyphs[endGIdx].YAdvance += advanceDelta
			} else {
				run.Glyphs[endGIdx].XAdvance += advanceDelta
			}
			run.Glyphs[endGIdx].endLetterSpacing += halfSpacing
		}

		// go to next cluster
		startGIdx += startGlyph.GlyphCount
	}

	run.RecomputeAdvance()
}

// does not run RecomputeAdvance
func (run *Output) trimStartLetterSpacing() {
	if len(run.Glyphs) == 0 {
		return
	}
	firstG := &run.Glyphs[0]
	halfSpacing := firstG.startLetterSpacing
	// Reverse the spacing applied in AddLetterSpacing, using the sign matching
	// the run's axis so that the trim removes exactly the added progression
	// length.
	advanceDelta := spacingAdvanceDelta(run.Direction, halfSpacing)
	offsetDelta := spacingOffsetDelta(run.Direction, halfSpacing)
	firstG.Advance -= advanceDelta
	if run.Direction.IsVertical() {
		firstG.YAdvance -= advanceDelta
		firstG.YOffset -= offsetDelta
	} else {
		firstG.XAdvance -= advanceDelta
		firstG.XOffset -= offsetDelta
	}
	firstG.startLetterSpacing = 0
}

// AddSpacing adds additionnal spacing between words and letters, mutating the given [runs].
// [text] is the input slice the [runs] refer to.
//
// See the method [Output.AddWordSpacing] and [Output.AddLetterSpacing] for details
// about what spacing actually is.
func AddSpacing(runs []Output, text []rune, wordSpacing, letterSpacing fixed.Int26_6) {
	for i := range runs {
		isStartRun, isEndRun := i == 0, i == len(runs)-1
		if wordSpacing != 0 {
			runs[i].AddWordSpacing(text, wordSpacing)
		}
		if letterSpacing != 0 {
			runs[i].AddLetterSpacing(letterSpacing, isStartRun, isEndRun)
		}
	}
}
