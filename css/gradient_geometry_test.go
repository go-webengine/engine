// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"testing"
)

// TestGradientGeometryAgreesWithAt: the shape Geometry reports is the one At
// samples — the colour At gives at a point is the stop colour at that point's
// position along the reported axis (linear) or ellipse (radial).
func TestGradientGeometryAgreesWithAt(t *testing.T) {
	black, white := Color{A: 255}, Color{R: 255, G: 255, B: 255, A: 255}
	stops := []ColorStop{{Color: black}, {Color: white}}

	lin := (&Gradient{AngleDeg: 90, Stops: stops}).Sampler(200, 50)
	g := lin.Geometry()
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if g.Radial || !near(g.X0, 0) || !near(g.X1, 200) || !near(g.Y0, 25) || !near(g.Y1, 25) {
		t.Errorf("linear 90deg on 200×50: axis (%v,%v)→(%v,%v), want (0,25)→(200,25)", g.X0, g.Y0, g.X1, g.Y1)
	}
	if len(g.Stops) != 2 || g.Stops[0].Pos != 0 || g.Stops[1].Pos != 1 || g.Stops[1].Color != white {
		t.Errorf("stops = %+v", g.Stops)
	}
	if c := lin.At(150, 10); math.Abs(float64(c.R)-0.75*255) > 1 {
		t.Errorf("At(150) = %v, want 75 %% along the axis", c)
	}

	rad := (&Gradient{Radial: true, Shape: RadialCircle, Extent: ExtentClosestSide,
		PosX: Length{Percent: .5, IsPercent: true}, PosY: Length{Percent: .5, IsPercent: true}, Stops: stops}).Sampler(100, 60)
	g = rad.Geometry()
	if !g.Radial || g.CX != 50 || g.CY != 30 || g.RX != 30 || g.RY != 30 {
		t.Errorf("closest-side circle on 100×60: centre (%v,%v) radii (%v,%v), want (50,30) and 30", g.CX, g.CY, g.RX, g.RY)
	}
	if c := rad.At(50+15, 30); math.Abs(float64(c.R)-0.5*255) > 1 {
		t.Errorf("At half the radius = %v, want the middle colour", c)
	}
}
