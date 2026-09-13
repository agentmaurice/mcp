package browser

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

// DiffRegion represents a bounding box of changed pixels
type DiffRegion struct {
	X              int
	Y              int
	Width          int
	Height         int
	DiffPercentage float64
}

// DiffResult represents the result of a pixel diff operation
type DiffResult struct {
	Match          bool
	DiffPercentage float64
	DiffPixelCount int
	Width          int
	Height         int
	Regions        []DiffRegion
}

// pixelDistance computes Euclidean RGB distance between two pixels
func pixelDistance(r1, g1, b1, r2, g2, b2 uint8) float64 {
	dr := float64(int(r1) - int(r2))
	dg := float64(int(g1) - int(g2))
	db := float64(int(b1) - int(b2))
	return (dr*dr + dg*dg + db*db) / 195075.0 // normalized by max distance squared
}

// imageToNRGBA converts an image to NRGBA format
func imageToNRGBA(img image.Image) *image.NRGBA {
	bounds := img.Bounds()
	nrgba := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			nrgba.Set(x, y, img.At(x, y))
		}
	}
	return nrgba
}

// floodFill finds a connected region of different pixels using 8-neighbor flood fill
func floodFill(diffMap, visited []bool, width, height, startX, startY int) DiffRegion {
	minX, maxX := startX, startX
	minY, maxY := startY, startY
	pixelCount := 0

	// Simple iterative flood fill using a queue
	queue := make([]struct{ x, y int }, 0, width*height)
	queue = append(queue, struct{ x, y int }{startX, startY})
	visited[startY*width+startX] = true

	for len(queue) > 0 {
		pos := queue[0]
		queue = queue[1:]
		x, y := pos.x, pos.y
		pixelCount++

		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}

		// 8-neighbor fill
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := x+dx, y+dy
				if nx >= 0 && nx < width && ny >= 0 && ny < height {
					idx := ny*width + nx
					if !visited[idx] && diffMap[idx] {
						visited[idx] = true
						queue = append(queue, struct{ x, y int }{nx, ny})
					}
				}
			}
		}
	}

	width_ := maxX - minX + 1
	height_ := maxY - minY + 1
	return DiffRegion{
		X:              minX,
		Y:              minY,
		Width:          width_,
		Height:         height_,
		DiffPercentage: float64(pixelCount) / float64(width*height) * 100.0,
	}
}

// mergeRegions merges clusters of regions within maxDistance (16px Manhattan)
func mergeRegions(regions []DiffRegion, maxDistance int) []DiffRegion {
	if len(regions) <= 1 {
		return regions
	}

	merged := make([]DiffRegion, 0, len(regions))
	used := make([]bool, len(regions))

	for i := 0; i < len(regions); i++ {
		if used[i] {
			continue
		}

		cluster := regions[i]
		used[i] = true

		// Merge nearby regions
		for j := i + 1; j < len(regions); j++ {
			if used[j] {
				continue
			}

			// Manhattan distance from cluster center to region center
			c1x := cluster.X + cluster.Width/2
			c1y := cluster.Y + cluster.Height/2
			c2x := regions[j].X + regions[j].Width/2
			c2y := regions[j].Y + regions[j].Height/2

			dist := abs(c1x-c2x) + abs(c1y-c2y)
			if dist <= maxDistance {
				// Merge region j into cluster
				minX := cluster.X
				if regions[j].X < minX {
					minX = regions[j].X
				}
				minY := cluster.Y
				if regions[j].Y < minY {
					minY = regions[j].Y
				}
				maxX := cluster.X + cluster.Width - 1
				if regions[j].X+regions[j].Width-1 > maxX {
					maxX = regions[j].X + regions[j].Width - 1
				}
				maxY := cluster.Y + cluster.Height - 1
				if regions[j].Y+regions[j].Height-1 > maxY {
					maxY = regions[j].Y + regions[j].Height - 1
				}

				cluster.X = minX
				cluster.Y = minY
				cluster.Width = maxX - minX + 1
				cluster.Height = maxY - minY + 1
				cluster.DiffPercentage = float64(cluster.Width*cluster.Height) / float64(cluster.Width*cluster.Height) * 100.0

				used[j] = true
			}
		}

		merged = append(merged, cluster)
	}

	return merged
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ComputePixelDiff compares two PNG images and returns diff regions
func ComputePixelDiff(beforeBytes, afterBytes []byte, threshold float64) (*DiffResult, error) {
	if len(beforeBytes) == 0 || len(afterBytes) == 0 {
		return nil, fmt.Errorf("invalid image data")
	}

	// Default threshold if not provided
	if threshold <= 0 {
		threshold = 10
	}

	// Decode images
	beforeImg, err := png.Decode(bytes.NewReader(beforeBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to decode before image: %w", err)
	}

	afterImg, err := png.Decode(bytes.NewReader(afterBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to decode after image: %w", err)
	}

	// Convert to NRGBA for pixel access
	beforeNRGBA := imageToNRGBA(beforeImg)
	afterNRGBA := imageToNRGBA(afterImg)

	bounds := beforeNRGBA.Bounds()
	afterBounds := afterNRGBA.Bounds()

	// Images must be same size
	if bounds.Dx() != afterBounds.Dx() || bounds.Dy() != afterBounds.Dy() {
		return nil, fmt.Errorf("images have different dimensions")
	}

	width := bounds.Dx()
	height := bounds.Dy()
	totalPixels := width * height

	// Create diff map
	diffMap := make([]bool, totalPixels)
	diffPixelCount := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			beforeR, beforeG, beforeB, _ := beforeNRGBA.At(x, y).RGBA()
			afterR, afterG, afterB, _ := afterNRGBA.At(x, y).RGBA()

			// Convert from 16-bit to 8-bit
			r1 := uint8(beforeR >> 8)
			g1 := uint8(beforeG >> 8)
			b1 := uint8(beforeB >> 8)
			r2 := uint8(afterR >> 8)
			g2 := uint8(afterG >> 8)
			b2 := uint8(afterB >> 8)

			dist := pixelDistance(r1, g1, b1, r2, g2, b2)
			if dist > threshold/100.0 { // threshold is normalized
				idx := (y - bounds.Min.Y) * width + (x - bounds.Min.X)
				diffMap[idx] = true
				diffPixelCount++
			}
		}
	}

	// Find regions via flood fill
	visited := make([]bool, totalPixels)
	regions := make([]DiffRegion, 0)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			if diffMap[idx] && !visited[idx] {
				region := floodFill(diffMap, visited, width, height, x, y)
				regions = append(regions, region)
			}
		}
	}

	// Merge nearby regions (within 16px Manhattan distance)
	regions = mergeRegions(regions, 16)

	// Sort by size (descending) and cap at 100
	if len(regions) > 100 {
		// Simple selection of top 100 by area
		type regionArea struct {
			index int
			area  int
		}
		areas := make([]regionArea, len(regions))
		for i, r := range regions {
			areas[i] = regionArea{i, r.Width * r.Height}
		}

		// Sort by area descending
		for i := 0; i < len(areas); i++ {
			for j := i + 1; j < len(areas); j++ {
				if areas[j].area > areas[i].area {
					areas[i], areas[j] = areas[j], areas[i]
				}
			}
		}

		topRegions := make([]DiffRegion, 100)
		for i := 0; i < 100; i++ {
			topRegions[i] = regions[areas[i].index]
		}
		regions = topRegions
	}

	diffPercentage := float64(diffPixelCount) / float64(totalPixels) * 100.0

	return &DiffResult{
		Match:          diffPercentage == 0,
		DiffPercentage: diffPercentage,
		DiffPixelCount: diffPixelCount,
		Width:          width,
		Height:         height,
		Regions:        regions,
	}, nil
}
