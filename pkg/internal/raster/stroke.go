package raster

import (
	"image"
	"image/color"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"

	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

// capFunc maps a render.LineCap to the rasterx CapFunc that draws it.
func capFunc(c render.LineCap) rasterx.CapFunc {
	switch c {
	case render.RoundCap:
		return rasterx.RoundCap
	case render.SquareCap:
		return rasterx.SquareCap
	default:
		return rasterx.ButtCap
	}
}

// joinParams maps a render.LineJoin to the rasterx JoinMode and the GapFunc used
// to bridge the convex side of a join. For miter joins we use MiterClip so the
// miter limit applies and the join falls back to a flat (bevel) bridge past the
// limit. Round joins use a circular arc gap; bevel uses a flat gap.
func joinParams(j render.LineJoin) (rasterx.JoinMode, rasterx.GapFunc) {
	switch j {
	case render.RoundJoin:
		return rasterx.Round, rasterx.RoundGap
	case render.BevelJoin:
		return rasterx.Bevel, rasterx.FlatGap
	default: // MiterJoin
		return rasterx.MiterClip, rasterx.FlatGap
	}
}

// toFixed converts a device-space point (already offset into the local mask
// coordinate frame) to fixed.Point26_6.
func toFixed(x, y float64) fixed.Point26_6 {
	return rasterx.ToFixedP(x, y)
}

// Stroke renders path's outline using rasterx's Dasher/Stroker, which honors
// line caps (butt/round/square), joins (miter with miter-limit fallback to
// bevel, round, bevel) and dash patterns. The outline is rasterized to an alpha
// coverage mask over the path's device bounds, then composited via the same path
// Fill uses — so the clip and the stroke color's alpha (StrokePaint.Color.A,
// which already folds in /CA) are honored. Degenerate or empty paths paint
// nothing and never panic.
func (d *Device) Stroke(path *render.Path, paint render.StrokePaint) {
	if path == nil || path.Empty() {
		return
	}
	w := paint.Width
	if w <= 0 {
		w = 1 // PDF zero-width means "thinnest renderable line".
	}

	// Size the coverage mask to the path's device bounds, expanded by the stroke
	// half-width (plus a little for round/square caps and miter spikes) so the
	// outline isn't clipped by the bounding box. The miter can extend up to
	// miterLimit*halfWidth past the vertex, so include that.
	bb := pathDeviceBounds(path)
	if bb.Empty() {
		return
	}
	miter := paint.MiterLimit
	if miter < 1 {
		miter = 10 // PDF default miter limit
	}
	pad := int(w*0.5*miter) + 2
	bb.Min.X -= pad
	bb.Min.Y -= pad
	bb.Max.X += pad
	bb.Max.Y += pad
	// Nothing this stroke covers is writable: skip before rasterizing. On a
	// region Device this is what keeps replaying the page's item list cheap.
	if !bb.Overlaps(d.img.Bounds()) {
		return
	}
	// Clipped to the PAGE, not to this Device's surface, for the same reason
	// rasterizeMask does it: rasterx derives its sampling from the mask
	// rectangle, so a stroke truncated at a region edge gets different
	// coverage than the same stroke rasterized whole, and the region stops
	// being pixel-identical to the page. compositeOffset discards the part of
	// the mask this Device cannot write.
	bb = bb.Intersect(d.page)
	if bb.Empty() {
		return
	}

	dx, dy := bb.Dx(), bb.Dy()
	// Coverage mask: draw opaque white through the dasher into an *image.Alpha,
	// which stores the rasterized alpha as straight coverage. Coordinates are
	// offset by bb.Min so the local frame starts at (0,0).
	cov := image.NewAlpha(image.Rect(0, 0, dx, dy))
	scanner := rasterx.NewScannerGV(dx, dy, cov, cov.Bounds())
	scanner.SetColor(color.Opaque)
	dasher := rasterx.NewDasher(dx, dy, scanner)

	jm, gap := joinParams(paint.Join)
	capL := capFunc(paint.Cap)
	// rasterx fixed widths/limits are in 26.6 fixed point (×64).
	fw := fixed.Int26_6(w*64 + 0.5)
	fml := fixed.Int26_6(miter*64 + 0.5)
	dasher.SetStroke(fw, fml, capL, capL, gap, jm, paint.DashArray, paint.DashPhase)

	ox, oy := float64(bb.Min.X), float64(bb.Min.Y)
	if !replayStroke(dasher, path, ox, oy) {
		return // no drawable segments
	}
	dasher.Draw()

	d.compositeOffset(cov, bb.Min, paint.Color, paint.BlendMode)
}

// replayStroke feeds path into a rasterx Adder (the Dasher), offsetting by
// (ox,oy) into the local mask frame. It returns false if the path contained no
// drawable segment (e.g. only a MoveTo), so the caller can skip the draw.
func replayStroke(a rasterx.Adder, path *render.Path, ox, oy float64) bool {
	var started, drew bool
	var sx, sy float64 // subpath start (for Close)
	var cx, cy float64 // current point
	start := func(x, y float64) {
		a.Start(toFixed(x-ox, y-oy))
		sx, sy, cx, cy = x, y, x, y
		started = true
	}
	for _, s := range path.Segments {
		switch s.Kind {
		case render.MoveTo:
			if started {
				a.Stop(false)
			}
			start(s.P0.X, s.P0.Y)
		case render.LineTo:
			if !started {
				start(s.P0.X, s.P0.Y)
				continue
			}
			a.Line(toFixed(s.P0.X-ox, s.P0.Y-oy))
			cx, cy = s.P0.X, s.P0.Y
			drew = true
		case render.CubeTo:
			if !started {
				start(cx, cy)
			}
			a.CubeBezier(
				toFixed(s.P0.X-ox, s.P0.Y-oy),
				toFixed(s.P1.X-ox, s.P1.Y-oy),
				toFixed(s.P2.X-ox, s.P2.Y-oy),
			)
			cx, cy = s.P2.X, s.P2.Y
			drew = true
		case render.Close:
			if started {
				a.Line(toFixed(sx-ox, sy-oy))
				a.Stop(true)
				cx, cy = sx, sy
				started = false
				drew = true
			}
		}
	}
	if started {
		a.Stop(false)
	}
	return drew
}

// compositeOffset blends src color through a coverage mask whose local (0,0)
// origin maps to device point dst, honoring the active clip, src alpha, and the
// named blend mode. It mirrors compositeBlend() but translates local mask
// coordinates to device space.
func (d *Device) compositeOffset(mask *image.Alpha, dst image.Point, c color.RGBA, blendMode string) {
	sep, isSep, nonsep, isNonsep := lookupBlend(blendMode)
	blend := isSep || isNonsep

	b := mask.Bounds()
	clip := d.activeClip()
	imgB := d.img.Bounds()
	for ly := b.Min.Y; ly < b.Max.Y; ly++ {
		dy := dst.Y + ly
		if dy < imgB.Min.Y || dy >= imgB.Max.Y {
			continue
		}
		for lx := b.Min.X; lx < b.Max.X; lx++ {
			cov := mask.AlphaAt(lx, ly).A
			if cov == 0 {
				continue
			}
			dx := dst.X + lx
			if dx < imgB.Min.X || dx >= imgB.Max.X {
				continue
			}
			if clip != nil {
				cov = mulU8(cov, clip.AlphaAt(dx, dy).A)
				if cov == 0 {
					continue
				}
			}
			a := mulU8(c.A, cov)
			if a == 0 {
				continue
			}
			src := c
			if blend {
				src = blendSource(d.img.RGBAAt(dx, dy), c, sep, nonsep, isSep)
			}
			over(d.img, dx, dy, src, a)
		}
	}
}
