// Package clip provides an implementation for clipping polygons.
// Currently the only implementation is using the github.com/ctessum/go.clipper library.
package clip

import (
	"GoSlice/data"
	"fmt"
	clipper "github.com/ctessum/go.clipper"
)

// Clipper is an interface that provides methods needed by GoSlice to clip polygons.
type Clipper interface {
	// GenerateLayerParts partitions the whole layer into several partition parts.
	GenerateLayerParts(l data.Layer) (data.PartitionedLayer, bool)
	// InsetLayer returns all new paths generated by insetting all parts of the layer.
	// The result is built the following way: [part][wall][insetNum]data.Paths
	//
	//  * Part is the part in the partitionedLayer
	//  * Wall is the wall of the part. The first wall is the outer perimeter
	//  * InsetNum is the number of the inset (starting by the outer walls with 0)
	//    and all following are from holes inside of the polygon.
	InsetLayer(layer data.PartitionedLayer, offset data.Micrometer, insetCount int) [][][]data.Paths

	// Inset insets the given layer part.
	// The result is built the following way: [wall][insetNum]data.Paths
	//
	//  * Wall is the wall of the part. The first wall is the outer perimeter
	//  * InsetNum is the number of the inset (starting by the outer walls with 0)
	//    and all following are from holes inside of the polygon.
	Inset(part data.LayerPart, offset data.Micrometer, insetCount int) [][]data.Paths

	// Fill creates an infill pattern for the given paths.
	// The parameter overlapPercentage should normally be a value between 0 and 100.
	// But it can also be smaller or greater than that if needed.
	// The generated infill will overlap the paths by the percentage of this param.
	// LineWidth is used for both, the calculation of the overlap and the calculation between the lines.
	Fill(paths data.Paths, lineWidth data.Micrometer, overlapPercentage int) data.Paths
}

// clipperClipper implements Clipper using the external clipper library.
type clipperClipper struct {
}

// NewClipper returns a new instance of a polygon Clipper.
func NewClipper() Clipper {
	return clipperClipper{}
}

// clipperPoint converts the GoSlice point representation to the
// representation which is used by the external clipper lib.
func clipperPoint(p data.MicroPoint) *clipper.IntPoint {
	return &clipper.IntPoint{
		X: clipper.CInt(p.X()),
		Y: clipper.CInt(p.Y()),
	}
}

// clipperPaths converts the GoSlice Paths representation
// to the representation which is used by the external clipper lib.
func clipperPaths(p data.Paths) clipper.Paths {
	var result clipper.Paths
	for _, path := range p {
		var newPath clipper.Path
		for _, point := range path {
			newPath = append(newPath, clipperPoint(point))
		}
		result = append(result, newPath)
	}

	return result
}

// microPoint converts the external clipper lib representation of a point
// to the representation which is used by GoSlice.
func microPoint(p *clipper.IntPoint) data.MicroPoint {
	return data.NewMicroPoint(data.Micrometer(p.X), data.Micrometer(p.Y))
}

// microPath converts the external clipper lib representation of a path
// to the representation which is used by GoSlice.
// The parameter simplify enables simplifying of the path using
// the default simplification settings.
func microPath(p clipper.Path, simplify bool) data.Path {
	var result data.Path
	for _, point := range p {
		result = append(result, microPoint(point))
	}

	if simplify {
		return result.Simplify(-1, -1)
	}
	return result
}

// microPaths converts the external clipper lib representation of paths
// to the representation which is used by GoSlice.
// The parameter simplify enables simplifying of the paths using
// the default simplification settings.
func microPaths(p clipper.Paths, simplify bool) data.Paths {
	var result data.Paths
	for _, path := range p {
		result = append(result, microPath(path, simplify))
	}
	return result
}

func (c clipperClipper) GenerateLayerParts(l data.Layer) (data.PartitionedLayer, bool) {
	polyList := clipper.Paths{}
	// convert all polygons to clipper polygons
	for _, layerPolygon := range l.Polygons() {
		var path = clipper.Path{}

		prev := 0
		// convert all points of this polygons
		for j, layerPoint := range layerPolygon {
			// ignore first as the next check would fail otherwise
			if j == 0 {
				path = append(path, clipperPoint(layerPolygon[0]))
				continue
			}

			// filter too near points
			// check this always with the previous point
			if layerPoint.Sub(layerPolygon[prev]).ShorterThanOrEqual(100) {
				continue
			}

			path = append(path, clipperPoint(layerPoint))
			prev = j
		}

		polyList = append(polyList, path)
	}

	var layerParts []data.LayerPart

	clip := clipper.NewClipper(clipper.IoNone)
	clip.AddPaths(polyList, clipper.PtSubject, true)
	resultPolys, ok := clip.Execute2(clipper.CtUnion, clipper.PftEvenOdd, clipper.PftEvenOdd)
	if !ok {
		return nil, false
	}

	polysForNextRound := []*clipper.PolyNode{}

	for _, c := range resultPolys.Childs() {
		polysForNextRound = append(polysForNextRound, c)
	}
	for {
		if polysForNextRound == nil {
			break
		}
		thisRound := polysForNextRound
		polysForNextRound = nil

		for _, p := range thisRound {
			var holes data.Paths

			for _, child := range p.Childs() {
				holes = append(holes, microPath(child.Contour(), false))
				for _, c := range child.Childs() {
					polysForNextRound = append(polysForNextRound, c)
				}
			}

			layerParts = append(layerParts, data.NewUnknownLayerPart(microPath(p.Contour(), false), holes))
		}
	}
	return data.NewPartitionedLayer(layerParts), true
}

func (c clipperClipper) InsetLayer(layer data.PartitionedLayer, offset data.Micrometer, insetCount int) [][][]data.Paths {
	var result [][][]data.Paths
	for _, part := range layer.LayerParts() {
		result = append(result, c.Inset(part, offset, insetCount))
	}

	return result
}

func (c clipperClipper) Inset(part data.LayerPart, offset data.Micrometer, insetCount int) [][]data.Paths {
	var insets [][]data.Paths

	o := clipper.NewClipperOffset()

	for insetNr := 0; insetNr < insetCount; insetNr++ {
		// insets for the outline
		o.Clear()
		o.AddPaths(clipperPaths(data.Paths{part.Outline()}), clipper.JtSquare, clipper.EtClosedPolygon)
		o.AddPaths(clipperPaths(part.Holes()), clipper.JtSquare, clipper.EtClosedPolygon)

		o.MiterLimit = 2
		allNewInsets := o.Execute(float64(-int(offset)*insetNr) - float64(offset/2))

		if len(allNewInsets) <= 0 {
			break
		} else {
			for wallNr, wall := range microPaths(allNewInsets, true) {
				if len(insets) <= wallNr {
					insets = append(insets, []data.Paths{})
				}

				// It can happen that clipper generates new walls which the previous insets didn't have
				// for example if it generates a filling polygon in the corners.
				// We add empty paths so that the insetNr is still correct.
				for len(insets[wallNr]) <= insetNr {
					insets[wallNr] = append(insets[wallNr], []data.Path{})
				}

				insets[wallNr][insetNr] = append(insets[wallNr][insetNr], wall)
			}
		}
	}

	return insets
}

func (c clipperClipper) Fill(paths data.Paths, lineWidth data.Micrometer, overlapPercentage int) data.Paths {
	min, max := paths.Size()
	cPaths := clipperPaths(paths)
	result := c.getLinearFill(cPaths, min, max, lineWidth, overlapPercentage)
	return microPaths(result, false)
}

// getLinearFill provides a infill which uses simple parallel lines
func (c clipperClipper) getLinearFill(polys clipper.Paths, minScanlines data.MicroPoint, maxScanlines data.MicroPoint, lineWidth data.Micrometer, overlapPercentage int) clipper.Paths {
	cl := clipper.NewClipper(clipper.IoNone)
	co := clipper.NewClipperOffset()
	var result clipper.Paths

	overlap := float32(lineWidth) * (100.0 - float32(overlapPercentage)) / 100.0

	lines := clipper.Paths{}
	numLine := 0
	// generate the lines
	for x := minScanlines.X(); x <= maxScanlines.X(); x += lineWidth {
		// switch line direction based on even / odd
		if numLine%2 == 1 {
			lines = append(lines, clipper.Path{
				&clipper.IntPoint{
					X: clipper.CInt(x),
					Y: clipper.CInt(maxScanlines.Y()),
				},
				&clipper.IntPoint{
					X: clipper.CInt(x),
					Y: clipper.CInt(minScanlines.Y()),
				},
			})
		} else {
			lines = append(lines, clipper.Path{
				&clipper.IntPoint{
					X: clipper.CInt(x),
					Y: clipper.CInt(minScanlines.Y()),
				},
				&clipper.IntPoint{
					X: clipper.CInt(x),
					Y: clipper.CInt(maxScanlines.Y()),
				},
			})
		}
		numLine++
	}

	// clip the paths with the lines using intersection
	for _, path := range polys {
		inset := clipper.Paths{path}

		// generate the inset for the overlap (only if needed)
		if overlapPercentage != 0 {
			co.AddPaths(inset, clipper.JtSquare, clipper.EtClosedPolygon)
			co.MiterLimit = 2
			inset = co.Execute(float64(-overlap))
		}

		// clip the lines by the resulting inset
		cl.AddPaths(inset, clipper.PtClip, true)
		cl.AddPaths(lines, clipper.PtSubject, false)

		tree, ok := cl.Execute2(clipper.CtIntersection, clipper.PftEvenOdd, clipper.PftEvenOdd)
		if !ok {
			fmt.Println("getLinearFill failed")
			return nil
		}

		for _, c := range tree.Childs() {
			result = append(result, c.Contour())
		}

		cl.Clear()
		co.Clear()
	}

	return result
}
