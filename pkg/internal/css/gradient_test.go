package css

import (
	"image/color"
	"math"
	"testing"
)

func TestParseGradientLinearDirection(t *testing.T) {
	tests := []struct {
		value     string
		wantAngle float64
		wantCornX float64
		wantCornY float64
		corner    bool
	}{
		// The direction is optional; omitting it means `to bottom` (180deg).
		{"linear-gradient(red, blue)", 180, 0, 0, false},

		// Every angle unit.
		{"linear-gradient(45deg, red, blue)", 45, 0, 0, false},
		{"linear-gradient(-90deg, red, blue)", -90, 0, 0, false},
		{"linear-gradient(0.25turn, red, blue)", 90, 0, 0, false},
		{"linear-gradient(200grad, red, blue)", 180, 0, 0, false},
		{"linear-gradient(1rad, red, blue)", 180 / math.Pi, 0, 0, false},

		// Sides normalize to a fixed angle (they do not depend on the box shape).
		{"linear-gradient(to top, red, blue)", 0, 0, 0, false},
		{"linear-gradient(to right, red, blue)", 90, 0, 0, false},
		{"linear-gradient(to bottom, red, blue)", 180, 0, 0, false},
		{"linear-gradient(to left, red, blue)", 270, 0, 0, false},

		// Corners stay as components: their angle depends on the box's aspect
		// ratio, so it cannot be resolved here.
		{"linear-gradient(to top right, red, blue)", 0, 1, -1, true},
		{"linear-gradient(to right top, red, blue)", 0, 1, -1, true},
		{"linear-gradient(to bottom left, red, blue)", 0, -1, 1, true},
		{"linear-gradient(to bottom right, red, blue)", 0, 1, 1, true},
		{"linear-gradient(to top left, red, blue)", 0, -1, -1, true},

		// The function name is case-insensitive.
		{"LINEAR-GRADIENT(TO RIGHT, red, blue)", 90, 0, 0, false},
	}
	for _, tt := range tests {
		g, ok := parseGradient(tt.value)
		if !ok {
			t.Errorf("%s: parse failed", tt.value)
			continue
		}
		if g.HasCorner != tt.corner {
			t.Errorf("%s: HasCorner = %v, want %v", tt.value, g.HasCorner, tt.corner)
			continue
		}
		if tt.corner {
			if g.CornerX != tt.wantCornX || g.CornerY != tt.wantCornY {
				t.Errorf("%s: corner = (%v,%v), want (%v,%v)",
					tt.value, g.CornerX, g.CornerY, tt.wantCornX, tt.wantCornY)
			}
			continue
		}
		if math.Abs(g.AngleDeg-tt.wantAngle) > 1e-9 {
			t.Errorf("%s: angle = %v, want %v", tt.value, g.AngleDeg, tt.wantAngle)
		}
	}
}

func TestParseGradientRejects(t *testing.T) {
	// Each of these must be rejected outright so the declaration is DROPPED
	// rather than painting an invented gradient.
	for _, v := range []string{
		"",
		"none",
		"url(x.png)",
		"linear-gradient()",
		"linear-gradient(red)",              // fewer than two stops
		"linear-gradient(to right, red)",    // direction plus one stop
		"linear-gradient(45, red, blue)",    // unitless number is not an <angle>
		"linear-gradient(45px, red, blue)",  // a length is not an <angle>
		"linear-gradient(to, red, blue)",    // dangling `to`
		"linear-gradient(to up, red, blue)", // not a side keyword
		// Two keywords on one axis.
		"linear-gradient(to left right, red, blue)",
		"linear-gradient(to top bottom, red, blue)",
		"linear-gradient(to top left bottom, red, blue)",
		"linear-gradient(red, notacolour)",
		"linear-gradient(red, blue) extra", // trailing text
		"conic-gradient(red, blue)",        // a gradient this engine does not paint
		"radial-gradient(red)",             // fewer than two stops
		"radial-gradient(circle ellipse, red, blue)",
		"radial-gradient(50%, red, blue)",              // a percentage circle radius is invalid
		"radial-gradient(circle 10px 20px, red, blue)", // two radii for a circle
		"radial-gradient(ellipse 10px, red, blue)",     // one radius for an ellipse
		"radial-gradient(closest-side farthest-side, red, blue)",
		"radial-gradient(-10px, red, blue)",     // negative radius
		"radial-gradient(circle at, red, blue)", // dangling `at`
	} {
		if g, ok := parseGradient(v); ok {
			t.Errorf("parseGradient(%q) = %+v, want rejected", v, g)
		}
	}
}

func TestParseGradientColorHintRejected(t *testing.T) {
	// A bare <length-percentage> between two stops is a COLOUR HINT (a non-linear
	// interpolation midpoint). It is deliberately not supported, and must be
	// REJECTED rather than mistaken for a stop — treating it as one would silently
	// shift every colour around it.
	for _, v := range []string{
		"linear-gradient(red, 30%, blue)",
		"linear-gradient(to right, red, 20px, blue)",
	} {
		if _, ok := parseGradient(v); ok {
			t.Errorf("parseGradient(%q): colour hint must be rejected, not painted", v)
		}
	}
}

func TestParseGradientStops(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}

	g, ok := parseGradient("linear-gradient(red 10%, blue 90%)")
	if !ok {
		t.Fatal("parse failed")
	}
	if len(g.Stops) != 2 {
		t.Fatalf("stops = %d, want 2", len(g.Stops))
	}
	if g.Stops[0].Color != red || !g.Stops[0].HasPos ||
		g.Stops[0].Pos != (Length{10, UnitPercent}) {
		t.Errorf("stop 0 = %+v", g.Stops[0])
	}
	if g.Stops[1].Color != blue || g.Stops[1].Pos != (Length{90, UnitPercent}) {
		t.Errorf("stop 1 = %+v", g.Stops[1])
	}

	// The two-position shorthand expands to two stops of the same colour, which
	// is how a hard-edged band is written.
	g, ok = parseGradient("linear-gradient(red 0 40%, blue 40% 100%)")
	if !ok {
		t.Fatal("parse failed")
	}
	if len(g.Stops) != 4 {
		t.Fatalf("stops = %d, want 4 (each two-position stop expands to two)", len(g.Stops))
	}
	if g.Stops[0].Color != red || g.Stops[1].Color != red ||
		g.Stops[2].Color != blue || g.Stops[3].Color != blue {
		t.Errorf("expanded colours wrong: %+v", g.Stops)
	}

	// An omitted position must stay DISTINGUISHABLE from an explicit 0%.
	g, ok = parseGradient("linear-gradient(red, blue)")
	if !ok {
		t.Fatal("parse failed")
	}
	if g.Stops[0].HasPos || g.Stops[1].HasPos {
		t.Error("omitted positions must have HasPos=false")
	}
}

func TestParseGradientRadial(t *testing.T) {
	tests := []struct {
		value      string
		shape      RadialShape
		extent     RadialExtent
		centerX    Length
		centerY    Length
		r1, r2     Length
		checkRadii bool
	}{
		{value: "radial-gradient(red, blue)",
			shape: RadialEllipse, extent: ExtentFarthestCorner,
			centerX: pctLen(50), centerY: pctLen(50)},
		{value: "radial-gradient(circle, red, blue)",
			shape: RadialCircle, extent: ExtentFarthestCorner,
			centerX: pctLen(50), centerY: pctLen(50)},
		{value: "radial-gradient(closest-side, red, blue)",
			shape: RadialEllipse, extent: ExtentClosestSide,
			centerX: pctLen(50), centerY: pctLen(50)},
		{value: "radial-gradient(circle closest-corner at 25% 75%, red, blue)",
			shape: RadialCircle, extent: ExtentClosestCorner,
			centerX: pctLen(25), centerY: pctLen(75)},
		// The shape keyword and the extent may appear in either order.
		{value: "radial-gradient(farthest-side circle, red, blue)",
			shape: RadialCircle, extent: ExtentFarthestSide,
			centerX: pctLen(50), centerY: pctLen(50)},
		// A single length IMPLIES circle; two imply ellipse.
		{value: "radial-gradient(30px, red, blue)",
			shape: RadialCircle, extent: ExtentExplicit,
			centerX: pctLen(50), centerY: pctLen(50),
			r1: Length{30, UnitPx}, r2: Length{30, UnitPx}, checkRadii: true},
		{value: "radial-gradient(30px 60%, red, blue)",
			shape: RadialEllipse, extent: ExtentExplicit,
			centerX: pctLen(50), centerY: pctLen(50),
			r1: Length{30, UnitPx}, r2: Length{60, UnitPercent}, checkRadii: true},
		// `at <position>` reuses the background-position grammar, keywords included.
		{value: "radial-gradient(at top left, red, blue)",
			shape: RadialEllipse, extent: ExtentFarthestCorner,
			centerX: pctLen(0), centerY: pctLen(0)},
	}
	for _, tt := range tests {
		g, ok := parseGradient(tt.value)
		if !ok {
			t.Errorf("%s: parse failed", tt.value)
			continue
		}
		if g.Kind != GradientRadial {
			t.Errorf("%s: kind = %v, want radial", tt.value, g.Kind)
		}
		if g.Shape != tt.shape {
			t.Errorf("%s: shape = %v, want %v", tt.value, g.Shape, tt.shape)
		}
		if g.Extent != tt.extent {
			t.Errorf("%s: extent = %v, want %v", tt.value, g.Extent, tt.extent)
		}
		if g.Center.X != tt.centerX || g.Center.Y != tt.centerY {
			t.Errorf("%s: center = %v/%v, want %v/%v",
				tt.value, g.Center.X, g.Center.Y, tt.centerX, tt.centerY)
		}
		if tt.checkRadii && (g.ExplicitR1 != tt.r1 || g.ExplicitR2 != tt.r2) {
			t.Errorf("%s: radii = %v/%v, want %v/%v",
				tt.value, g.ExplicitR1, g.ExplicitR2, tt.r1, tt.r2)
		}
	}
}

func TestParseGradientRepeating(t *testing.T) {
	for _, tt := range []struct {
		value string
		kind  GradientKind
	}{
		{"repeating-linear-gradient(red, blue)", GradientLinear},
		{"repeating-radial-gradient(red, blue)", GradientRadial},
	} {
		g, ok := parseGradient(tt.value)
		if !ok {
			t.Errorf("%s: parse failed", tt.value)
			continue
		}
		if !g.Repeating {
			t.Errorf("%s: Repeating = false", tt.value)
		}
		if g.Kind != tt.kind {
			t.Errorf("%s: kind = %v, want %v", tt.value, g.Kind, tt.kind)
		}
	}
	// The non-repeating forms must NOT set the flag — the prefix match must not
	// be a substring match in the wrong direction.
	g, _ := parseGradient("linear-gradient(red, blue)")
	if g.Repeating {
		t.Error("linear-gradient must not be Repeating")
	}
}

func TestGradientLineSides(t *testing.T) {
	// A side gradient's line runs edge to edge through the centre, and its
	// length is the box's extent along that axis.
	const w, h = 100.0, 40.0
	tests := []struct {
		value                  string
		x0, y0, x1, y1, length float64
	}{
		{"linear-gradient(to right, red, blue)", 0, 20, 100, 20, 100},
		{"linear-gradient(to left, red, blue)", 100, 20, 0, 20, 100},
		{"linear-gradient(to bottom, red, blue)", 50, 0, 50, 40, 40},
		{"linear-gradient(to top, red, blue)", 50, 40, 50, 0, 40},
		// The default direction is `to bottom`.
		{"linear-gradient(red, blue)", 50, 0, 50, 40, 40},
	}
	for _, tt := range tests {
		g, ok := parseGradient(tt.value)
		if !ok {
			t.Fatalf("%s: parse failed", tt.value)
		}
		x0, y0, x1, y1, l := g.GradientLine(w, h)
		if !nearly(x0, tt.x0) || !nearly(y0, tt.y0) || !nearly(x1, tt.x1) || !nearly(y1, tt.y1) || !nearly(l, tt.length) {
			t.Errorf("%s: line = (%.4f,%.4f)-(%.4f,%.4f) len %.4f, want (%.4f,%.4f)-(%.4f,%.4f) len %.4f",
				tt.value, x0, y0, x1, y1, l, tt.x0, tt.y0, tt.x1, tt.y1, tt.length)
		}
	}
}

// TestGradientLineCorner is the case most likely to be got wrong: a `to
// <corner>` gradient is NOT a 45-degree gradient except on a square.
//
// The defining property, from CSS Images 3, is that the line PERPENDICULAR to
// the gradient line passing through the END point also passes through the
// target corner (and the perpendicular through the START point passes through
// the opposite corner). This test asserts that property directly rather than a
// precomputed angle, so it stays meaningful if the implementation changes.
func TestGradientLineCorner(t *testing.T) {
	for _, box := range [][2]float64{{100, 40}, {40, 100}, {50, 50}, {200, 30}} {
		w, h := box[0], box[1]
		for _, tt := range []struct {
			value            string
			cornerX, cornerY float64 // the corner the END of the line must serve
		}{
			{"linear-gradient(to bottom right, red, blue)", w, h},
			{"linear-gradient(to bottom left, red, blue)", 0, h},
			{"linear-gradient(to top right, red, blue)", w, 0},
			{"linear-gradient(to top left, red, blue)", 0, 0},
		} {
			g, ok := parseGradient(tt.value)
			if !ok {
				t.Fatalf("%s: parse failed", tt.value)
			}
			x0, y0, x1, y1, _ := g.GradientLine(w, h)
			dx, dy := x1-x0, y1-y0

			// The corner must project onto the gradient line exactly at the END
			// point — i.e. (corner − end) is PERPENDICULAR to the line direction.
			vx, vy := tt.cornerX-x1, tt.cornerY-y1
			if dot := vx*dx + vy*dy; math.Abs(dot) > 1e-6 {
				t.Errorf("box %gx%g %s: corner (%g,%g) does not project onto the line end (%.4f,%.4f); dot=%g",
					w, h, tt.value, tt.cornerX, tt.cornerY, x1, y1, dot)
			}

			// And the OPPOSITE corner must likewise project onto the start point.
			ox, oy := w-tt.cornerX, h-tt.cornerY
			ux, uy := ox-x0, oy-y0
			if dot := ux*dx + uy*dy; math.Abs(dot) > 1e-6 {
				t.Errorf("box %gx%g %s: opposite corner (%g,%g) does not project onto the line start (%.4f,%.4f); dot=%g",
					w, h, tt.value, ox, oy, x0, y0, dot)
			}
		}
	}
}

// TestGradientLineCornerIsNot45 pins the specific bug this geometry exists to
// avoid: on a NON-square box the corner direction must differ from 45 degrees.
// A regression to "corner == 45deg" would still pass a square-only test.
func TestGradientLineCornerIsNot45(t *testing.T) {
	g, ok := parseGradient("linear-gradient(to bottom right, red, blue)")
	if !ok {
		t.Fatal("parse failed")
	}
	// On a 100x40 box the direction is perpendicular to the other diagonal
	// (100,-40), i.e. proportional to (40,100) — decidedly not (1,1)/√2.
	x0, y0, x1, y1, _ := g.GradientLine(100, 40)
	dx, dy := x1-x0, y1-y0
	if math.Abs(dx-dy) < 1e-6 {
		t.Fatalf("direction (%g,%g) is 45 degrees on a 100x40 box; the corner rule is aspect-dependent", dx, dy)
	}
	if !nearly(dx/dy, 40.0/100.0) {
		t.Errorf("direction ratio dx/dy = %g, want %g (perpendicular to the other diagonal)", dx/dy, 0.4)
	}

	// On a SQUARE the two coincide, which is why a square-only test cannot
	// catch the bug above.
	x0, y0, x1, y1, _ = g.GradientLine(50, 50)
	if !nearly(x1-x0, y1-y0) {
		t.Error("on a square the corner direction must be 45 degrees")
	}
}

func TestGradientLineAngles(t *testing.T) {
	// 0deg points UP (toward the top of the box) and angles increase CLOCKWISE.
	// With Y down, that makes 90deg point right and 180deg point down.
	const w, h = 100.0, 100.0
	for _, tt := range []struct{ deg, dx, dy float64 }{
		{0, 0, -1},
		{90, 1, 0},
		{180, 0, 1},
		{270, -1, 0},
	} {
		g := &Gradient{Kind: GradientLinear, AngleDeg: tt.deg}
		x0, y0, x1, y1, l := g.GradientLine(w, h)
		dx, dy := (x1-x0)/l, (y1-y0)/l
		if !nearly(dx, tt.dx) || !nearly(dy, tt.dy) {
			t.Errorf("%gdeg: direction = (%.4f,%.4f), want (%g,%g)", tt.deg, dx, dy, tt.dx, tt.dy)
		}
	}
}

func TestNormalizeStopsOmittedPositions(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	green := color.RGBA{0, 128, 0, 255}

	tests := []struct {
		name  string
		stops []GradientStop
		want  []float64
	}{
		{
			// Rule 1: the endpoints get 0% and 100% when unpositioned.
			name:  "two unpositioned",
			stops: []GradientStop{{Color: red}, {Color: blue}},
			want:  []float64{0, 1},
		},
		{
			// Rule 3: a middle run is spread evenly between the endpoints.
			name:  "three unpositioned",
			stops: []GradientStop{{Color: red}, {Color: green}, {Color: blue}},
			want:  []float64{0, 0.5, 1},
		},
		{
			name:  "five unpositioned",
			stops: []GradientStop{{Color: red}, {Color: green}, {Color: blue}, {Color: green}, {Color: red}},
			want:  []float64{0, 0.25, 0.5, 0.75, 1},
		},
		{
			// A run is spread between the POSITIONED stops that bracket it, not
			// between 0 and 1.
			name: "run between positioned brackets",
			stops: []GradientStop{
				{Color: red, Pos: pctLen(20), HasPos: true},
				{Color: green},
				{Color: green},
				{Color: blue, Pos: pctLen(80), HasPos: true},
			},
			want: []float64{0.2, 0.4, 0.6, 0.8},
		},
		{
			// A leading unpositioned stop takes 0 even when the NEXT stop has a
			// position of its own.
			name: "leading unpositioned",
			stops: []GradientStop{
				{Color: red},
				{Color: blue, Pos: pctLen(30), HasPos: true},
			},
			want: []float64{0, 0.3},
		},
	}
	for _, tt := range tests {
		got := NormalizeStops(tt.stops, 100, 12)
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %d stops, want %d", tt.name, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if !nearly(got[i].Pos, tt.want[i]) {
				t.Errorf("%s: stop %d pos = %v, want %v (all: %v)", tt.name, i, got[i].Pos, tt.want[i], positions(got))
			}
		}
	}
}

func TestNormalizeStopsCorrectsDecreasing(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	green := color.RGBA{0, 128, 0, 255}

	// A decreasing position is CORRECTED UP to the running maximum, producing a
	// hard colour break. It must NOT be sorted: sorting would reorder the
	// author's colours, which is a different rendering entirely.
	got := NormalizeStops([]GradientStop{
		{Color: red, Pos: pctLen(40), HasPos: true},
		{Color: blue, Pos: pctLen(20), HasPos: true},
	}, 100, 12)
	if !nearly(got[0].Pos, 0.4) || !nearly(got[1].Pos, 0.4) {
		t.Errorf("positions = %v, want [0.4 0.4]", positions(got))
	}
	if got[0].Color != red || got[1].Color != blue {
		t.Error("correcting a decreasing position must not reorder the colours")
	}

	// The clamp is to the RUNNING maximum, so a later stop is pinned by the
	// largest position before it, not merely by its immediate predecessor.
	got = NormalizeStops([]GradientStop{
		{Color: red, Pos: pctLen(80), HasPos: true},
		{Color: green, Pos: pctLen(10), HasPos: true},
		{Color: blue, Pos: pctLen(30), HasPos: true},
	}, 100, 12)
	if !nearly(got[0].Pos, 0.8) || !nearly(got[1].Pos, 0.8) || !nearly(got[2].Pos, 0.8) {
		t.Errorf("positions = %v, want [0.8 0.8 0.8]", positions(got))
	}
}

func TestNormalizeStopsAbsoluteLengths(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}

	// An absolute position is a fraction of the gradient LINE's length, so the
	// same declaration lands differently on differently-sized boxes.
	got := NormalizeStops([]GradientStop{
		{Color: red, Pos: Length{20, UnitPx}, HasPos: true},
		{Color: blue, Pos: Length{80, UnitPx}, HasPos: true},
	}, 200, 12)
	if !nearly(got[0].Pos, 0.1) || !nearly(got[1].Pos, 0.4) {
		t.Errorf("positions = %v, want [0.1 0.4] on a 200-unit line", positions(got))
	}

	// An em position resolves against the font size, like every other Length.
	got = NormalizeStops([]GradientStop{
		{Color: red, Pos: Length{1, UnitEm}, HasPos: true},
		{Color: blue, Pos: Length{2, UnitEm}, HasPos: true},
	}, 100, 10)
	if !nearly(got[0].Pos, 0.1) || !nearly(got[1].Pos, 0.2) {
		t.Errorf("positions = %v, want [0.1 0.2] at 10pt font on a 100-unit line", positions(got))
	}
}

func TestNormalizeStopsOutOfRange(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}

	// Positions outside [0,1] are LEGAL and must not be clamped: they push the
	// ramp past the gradient line's ends, which is a real and different result.
	got := NormalizeStops([]GradientStop{
		{Color: red, Pos: pctLen(-20), HasPos: true},
		{Color: blue, Pos: pctLen(120), HasPos: true},
	}, 100, 12)
	if !nearly(got[0].Pos, -0.2) || !nearly(got[1].Pos, 1.2) {
		t.Errorf("positions = %v, want [-0.2 1.2] (out-of-range stops must not be clamped)", positions(got))
	}
}

func TestRadialRadii(t *testing.T) {
	// A 100x40 box with the centre at (25,10): distances to the sides are
	// left 25, right 75, top 10, bottom 30.
	const w, h, cx, cy = 100.0, 40.0, 25.0, 10.0

	tests := []struct {
		value  string
		rx, ry float64
	}{
		// closest-side: the nearest side per axis (ellipse), or the smaller of
		// the two (circle).
		{"radial-gradient(closest-side at 25px 10px, red, blue)", 25, 10},
		{"radial-gradient(circle closest-side at 25px 10px, red, blue)", 10, 10},

		// farthest-side: the farthest side per axis, or the larger (circle).
		{"radial-gradient(farthest-side at 25px 10px, red, blue)", 75, 30},
		{"radial-gradient(circle farthest-side at 25px 10px, red, blue)", 75, 75},

		// A corner CIRCLE's radius is the distance to that corner.
		{"radial-gradient(circle closest-corner at 25px 10px, red, blue)", math.Hypot(25, 10), math.Hypot(25, 10)},
		{"radial-gradient(circle farthest-corner at 25px 10px, red, blue)", math.Hypot(75, 30), math.Hypot(75, 30)},

		// A corner ELLIPSE keeps the closest/farthest-SIDE aspect ratio, scaled
		// by sqrt(2) so it passes through the corner.
		{"radial-gradient(closest-corner at 25px 10px, red, blue)", 25 * math.Sqrt2, 10 * math.Sqrt2},
		{"radial-gradient(farthest-corner at 25px 10px, red, blue)", 75 * math.Sqrt2, 30 * math.Sqrt2},

		// An explicit size resolves each axis against its own box dimension.
		{"radial-gradient(20px 50% at 25px 10px, red, blue)", 20, 20},
	}
	for _, tt := range tests {
		g, ok := parseGradient(tt.value)
		if !ok {
			t.Errorf("%s: parse failed", tt.value)
			continue
		}
		rx, ry := g.RadialRadii(w, h, cx, cy, 12)
		if !nearly(rx, tt.rx) || !nearly(ry, tt.ry) {
			t.Errorf("%s: radii = (%.4f,%.4f), want (%.4f,%.4f)", tt.value, rx, ry, tt.rx, tt.ry)
		}
	}
}

// TestRadialCornerEllipsePassesThroughCorner asserts the defining property of a
// corner-sized ellipse rather than the sqrt(2) shortcut, so the test stays valid
// if the derivation is ever rewritten.
func TestRadialCornerEllipsePassesThroughCorner(t *testing.T) {
	const w, h, cx, cy = 100.0, 40.0, 25.0, 10.0
	g, ok := parseGradient("radial-gradient(farthest-corner at 25px 10px, red, blue)")
	if !ok {
		t.Fatal("parse failed")
	}
	rx, ry := g.RadialRadii(w, h, cx, cy, 12)

	// The farthest corner from (25,10) on a 100x40 box is (100,40).
	dx, dy := 100.0-cx, 40.0-cy
	if v := (dx*dx)/(rx*rx) + (dy*dy)/(ry*ry); !nearly(v, 1) {
		t.Errorf("corner does not lie on the ending ellipse: x²/rx²+y²/ry² = %v, want 1", v)
	}
	// And it must keep the farthest-SIDE aspect ratio (75:30).
	if !nearly(rx/ry, 75.0/30.0) {
		t.Errorf("aspect ratio = %v, want %v (the farthest-side ratio)", rx/ry, 2.5)
	}
}

func TestParseBackgroundImageGradient(t *testing.T) {
	// The longhand routes a gradient to BackgroundGradient and a url() to
	// BackgroundImage, and the two are mutually exclusive.
	ref, grad, ok := parseBackgroundImage("linear-gradient(red, blue)")
	if !ok || grad == nil || ref != "" {
		t.Errorf("gradient: ref=%q grad=%v ok=%v", ref, grad != nil, ok)
	}
	ref, grad, ok = parseBackgroundImage("url(x.png)")
	if !ok || grad != nil || ref != "x.png" {
		t.Errorf("url: ref=%q grad=%v ok=%v", ref, grad != nil, ok)
	}
	ref, grad, ok = parseBackgroundImage("none")
	if !ok || grad != nil || ref != "" {
		t.Errorf("none: ref=%q grad=%v ok=%v", ref, grad != nil, ok)
	}
	// An <image> this engine does not paint must report ok=false so the caller
	// LEAVES the prior value rather than resetting the property.
	if _, _, ok := parseBackgroundImage("image-set(a.png 1x)"); ok {
		t.Error("image-set() must report ok=false so the prior value is kept")
	}
	if _, _, ok := parseBackgroundImage("conic-gradient(red, blue)"); ok {
		t.Error("conic-gradient() must report ok=false so the prior value is kept")
	}
}

func TestCascadeBackgroundGradientMutualExclusion(t *testing.T) {
	// A gradient replacing a url() must CLEAR the url(), and vice versa —
	// otherwise the painter would see two sources and have to guess.
	var cs ComputedStyle
	applyDeclaration(&cs, Declaration{Property: "background-image", Value: "url(a.png)"})
	if cs.BackgroundImage != "a.png" {
		t.Fatalf("BackgroundImage = %q", cs.BackgroundImage)
	}
	applyDeclaration(&cs, Declaration{Property: "background-image", Value: "linear-gradient(red, blue)"})
	if cs.BackgroundImage != "" || cs.BackgroundGradient == nil {
		t.Errorf("gradient must clear the url: image=%q grad=%v", cs.BackgroundImage, cs.BackgroundGradient != nil)
	}
	applyDeclaration(&cs, Declaration{Property: "background-image", Value: "url(b.png)"})
	if cs.BackgroundImage != "b.png" || cs.BackgroundGradient != nil {
		t.Errorf("url must clear the gradient: image=%q grad=%v", cs.BackgroundImage, cs.BackgroundGradient != nil)
	}
	// `none` clears both.
	applyDeclaration(&cs, Declaration{Property: "background-image", Value: "none"})
	if cs.BackgroundImage != "" || cs.BackgroundGradient != nil {
		t.Error("none must clear both sources")
	}
}

func TestShorthandBackgroundGradient(t *testing.T) {
	// The `background` shorthand must accept a gradient as its <image>
	// component, including alongside other components. splitComponents keeps a
	// parenthesized group whole, so the gradient's internal commas and spaces
	// must not split it.
	var cs ComputedStyle
	applyBackground(&cs, "linear-gradient(to right, red, blue) no-repeat")
	if cs.BackgroundGradient == nil {
		t.Fatal("shorthand did not set BackgroundGradient")
	}
	if cs.BackgroundRepeat != "no-repeat" {
		t.Errorf("BackgroundRepeat = %q, want no-repeat", cs.BackgroundRepeat)
	}
	if len(cs.BackgroundGradient.Stops) != 2 {
		t.Errorf("stops = %d, want 2", len(cs.BackgroundGradient.Stops))
	}

	// A `/ <size>` group must still split at the TOP-LEVEL slash only — a slash
	// inside the gradient (none here, but the scan must be paren-aware) or its
	// commas must not confuse it.
	cs = ComputedStyle{}
	applyBackground(&cs, "linear-gradient(red, blue) center / 50% 25%")
	if cs.BackgroundGradient == nil {
		t.Fatal("shorthand with a size did not set BackgroundGradient")
	}
	if cs.BackgroundSize.Kind != BgSizeExplicit ||
		cs.BackgroundSize.W != pctLen(50) || cs.BackgroundSize.H != pctLen(25) {
		t.Errorf("BackgroundSize = %+v", cs.BackgroundSize)
	}

	// The shorthand resets the gradient when it is absent.
	applyBackground(&cs, "red")
	if cs.BackgroundGradient != nil {
		t.Error("the shorthand must reset BackgroundGradient")
	}
}

func positions(stops []ResolvedStop) []float64 {
	out := make([]float64, len(stops))
	for i, s := range stops {
		out[i] = s.Pos
	}
	return out
}

func nearly(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
