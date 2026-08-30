// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

func TestApplyBackgroundImageProperty(t *testing.T) {
	s := &Style{}
	s.apply(Declaration{Property: "background-image", Value: "linear-gradient(to right, red, blue)"}, 16)
	if len(s.BackgroundImages) != 1 || s.BackgroundImages[0].Kind != BgGradient {
		t.Fatalf("background-image = %+v", s.BackgroundImages)
	}
	// `none` resets the image list.
	s.apply(Declaration{Property: "background-image", Value: "none"}, 16)
	if s.BackgroundImages != nil {
		t.Errorf("background-image none did not reset: %+v", s.BackgroundImages)
	}
}

func TestApplyBackgroundShorthandGradient(t *testing.T) {
	s := &Style{}
	s.apply(Declaration{Property: "background", Value: "#222 linear-gradient(to bottom, rgba(0,0,0,1), rgba(0,0,0,0))"}, 16)
	if s.Background != (Color{0x22, 0x22, 0x22, 255}) {
		t.Errorf("shorthand colour = %+v", s.Background)
	}
	if len(s.BackgroundImages) != 1 || s.BackgroundImages[0].Kind != BgGradient {
		t.Errorf("shorthand image = %+v", s.BackgroundImages)
	}
}

func TestApplyBackgroundSizePositionRepeat(t *testing.T) {
	s := &Style{}
	s.apply(Declaration{Property: "background-size", Value: "cover"}, 16)
	if len(s.BackgroundSize) != 1 || s.BackgroundSize[0].Kind != SizeCover {
		t.Errorf("size = %+v", s.BackgroundSize)
	}
	s.apply(Declaration{Property: "background-position", Value: "center top"}, 16)
	if len(s.BackgroundPosition) != 1 || s.BackgroundPosition[0].Y.Percent != 0 {
		t.Errorf("position = %+v", s.BackgroundPosition)
	}
	s.apply(Declaration{Property: "background-repeat", Value: "no-repeat"}, 16)
	if len(s.BackgroundRepeat) != 1 || s.BackgroundRepeat[0] != NoRepeat {
		t.Errorf("repeat = %+v", s.BackgroundRepeat)
	}
	// Invalid values leave the property unset.
	s2 := &Style{}
	s2.apply(Declaration{Property: "background-size", Value: "bogus"}, 16)
	s2.apply(Declaration{Property: "background-position", Value: ""}, 16)
	s2.apply(Declaration{Property: "background-repeat", Value: "bogus"}, 16)
	if s2.BackgroundSize != nil || s2.BackgroundPosition != nil || s2.BackgroundRepeat != nil {
		t.Errorf("invalid values set something: %+v", s2)
	}
}

func TestApplyBoxShadowAndOpacity(t *testing.T) {
	s := &Style{}
	s.apply(Declaration{Property: "box-shadow", Value: "0 4px 8px rgba(0,0,0,0.3)"}, 16)
	if len(s.BoxShadows) != 1 || s.BoxShadows[0].Blur != 8 {
		t.Errorf("box-shadow = %+v", s.BoxShadows)
	}
	s.apply(Declaration{Property: "opacity", Value: "0.5"}, 16)
	if !s.HasOpacity || s.Opacity != 0.5 {
		t.Errorf("opacity = %v,%v", s.Opacity, s.HasOpacity)
	}
	// Clamping below 0 and above 1.
	lo := &Style{}
	lo.apply(Declaration{Property: "opacity", Value: "-2"}, 16)
	if lo.Opacity != 0 {
		t.Errorf("opacity -2 = %v want 0", lo.Opacity)
	}
	hi := &Style{}
	hi.apply(Declaration{Property: "opacity", Value: "3"}, 16)
	if hi.Opacity != 1 {
		t.Errorf("opacity 3 = %v want 1", hi.Opacity)
	}
	// A non-numeric opacity is ignored.
	bad := &Style{}
	bad.apply(Declaration{Property: "opacity", Value: "half"}, 16)
	if bad.HasOpacity {
		t.Error("non-numeric opacity should be ignored")
	}
}

func TestSplitDeclChunksNestedSemicolons(t *testing.T) {
	// Semicolons inside url()/parens and inside quotes must not split declarations.
	decls := ParseDeclarations(`background-image: url(data:image/png;base64,AAAA); color: red; content: 'a;b'; x: )`)
	got := map[string]string{}
	for _, d := range decls {
		got[d.Property] = d.Value
	}
	if got["background-image"] != "url(data:image/png;base64,AAAA)" {
		t.Errorf("bg-image = %q", got["background-image"])
	}
	if got["color"] != "red" {
		t.Errorf("color = %q", got["color"])
	}
	if got["content"] != "'a;b'" {
		t.Errorf("content = %q", got["content"])
	}
	if got["x"] != ")" {
		t.Errorf("x = %q (stray paren should not underflow)", got["x"])
	}
}

func TestParseBgImageLayerURLFail(t *testing.T) {
	// A url() with an empty target fails the layer (and the whole value).
	if _, ok := parseBackgroundImage("url()", 16); ok {
		t.Error("empty url() should not yield a layer")
	}
}

func TestParseRadialGradientBadStops(t *testing.T) {
	if _, ok := parseGradient("radial-gradient(circle at center, bogus, alsobad)", 16); ok {
		t.Error("radial with no valid stops should fail")
	}
}

func TestParseColorStopsEmptyEntry(t *testing.T) {
	// An empty stop entry (double comma) is skipped; the rest still parse.
	g, ok := parseGradient("linear-gradient(45deg, red, , blue)", 16)
	if !ok || len(g.Stops) != 2 {
		t.Errorf("empty stop entry handling = %+v,%v", g, ok)
	}
}
