package api

import (
	"encoding/base64"
	"encoding/json"
	"math"
)

// visibilityBlocks — сторона маски из server/API.md: 256×256 блоков на карту.
const visibilityBlocks = 256

// VisibilityMask — растровые маски видимости. Бит 1 — блок виден, 0 — туман.
type VisibilityMask struct {
	Blocks   int    `json:"blocks"`
	Visible  string `json:"visible"`
	Explored string `json:"explored,omitempty"`
}

type visPoint struct {
	x, y float64
}

type visSegment struct {
	a, b visPoint
}

// visibilityFor считает видимость для одного аккаунта: объединение лучей
// от всех его фишек. Геометрия остаётся на сервере, клиент получает только маску.
func visibilityFor(scene Scene, doors map[string]bool, tokens []Token, accountID string) VisibilityMask {
	return maskFromBits(visibleBits(scene, doors, tokens, accountID), nil)
}

type visOrigin struct {
	at     visPoint
	radius float64
}

func visibleBits(scene Scene, doors map[string]bool, tokens []Token, accountID string) []byte {
	width := float64(scene.Image.Width)
	height := float64(scene.Image.Height)
	if width <= 0 || height <= 0 {
		return make([]byte, visibilityBlocks*visibilityBlocks/8)
	}
	walls := sceneSegments(scene.Walls)
	openDoors := make([]visSegment, 0, 2)
	for _, door := range sceneDoors(scene.Doors) {
		if doors[door.id] {
			openDoors = append(openDoors, door.segment)
		}
	}

	var origins []visOrigin
	for _, token := range tokens {
		if token.OwnerID == accountID {
			origins = append(origins, visOrigin{
				at:     visPoint{x: token.X, y: token.Y},
				radius: darkvisionRadius(scene, token),
			})
		}
	}
	mask := make([]byte, visibilityBlocks*visibilityBlocks/8)
	if len(origins) == 0 {
		return mask
	}

	blockW := width / visibilityBlocks
	blockH := height / visibilityBlocks
	for row := 0; row < visibilityBlocks; row++ {
		centerY := (float64(row) + 0.5) * blockH
		for col := 0; col < visibilityBlocks; col++ {
			centerX := (float64(col) + 0.5) * blockW
			target := visPoint{x: centerX, y: centerY}
			for _, origin := range origins {
				if origin.radius <= 0 {
					continue
				}
				if math.Hypot(target.x-origin.at.x, target.y-origin.at.y) > origin.radius {
					continue
				}
				if !blocked(origin.at, target, walls, openDoors) {
					bit := row*visibilityBlocks + col
					mask[bit/8] |= 1 << uint(bit%8)
					break
				}
			}
		}
	}
	return mask
}

// darkvisionRadius — предел взгляда в пикселях. На свету он бесконечен,
// в темноте — только тёмное зрение фишки, пересчитанное из футов в пиксели.
func darkvisionRadius(scene Scene, token Token) float64 {
	if scene.Light != "dark" {
		return math.MaxFloat64
	}
	if token.Darkvision <= 0 {
		return 0
	}
	side, unitsPerCell := 50.0, 5.0
	var grid struct {
		Square struct {
			SidePx float64 `json:"side_px"`
		} `json:"square"`
		UnitsPerCell float64 `json:"units_per_cell"`
	}
	if err := json.Unmarshal(scene.Grid, &grid); err == nil {
		if grid.Square.SidePx > 0 {
			side = grid.Square.SidePx
		}
		if grid.UnitsPerCell > 0 {
			unitsPerCell = grid.UnitsPerCell
		}
	}
	return float64(token.Darkvision) / unitsPerCell * side
}

// maskFromBits собирает ответ и объединяет текущую видимость с накопленной.
func maskFromBits(visible, explored []byte) VisibilityMask {
	size := visibilityBlocks * visibilityBlocks / 8
	if len(visible) != size {
		visible = make([]byte, size)
	}
	if len(explored) != size {
		explored = make([]byte, size)
	}
	for i := range explored {
		explored[i] |= visible[i]
	}
	return VisibilityMask{
		Blocks:   visibilityBlocks,
		Visible:  base64.StdEncoding.EncodeToString(visible),
		Explored: base64.StdEncoding.EncodeToString(explored),
	}
}

func emptyVisibility() VisibilityMask {
	return maskFromBits(make([]byte, visibilityBlocks*visibilityBlocks/8), nil)
}

type doorSeg struct {
	id      string
	segment visSegment
}

func sceneSegments(raw json.RawMessage) []visSegment {
	var walls []struct {
		Points [][2]float64 `json:"points"`
	}
	if err := json.Unmarshal(raw, &walls); err != nil {
		return nil
	}
	out := make([]visSegment, 0, len(walls)*2)
	for _, wall := range walls {
		for i := 1; i < len(wall.Points); i++ {
			out = append(out, visSegment{
				a: visPoint{x: wall.Points[i-1][0], y: wall.Points[i-1][1]},
				b: visPoint{x: wall.Points[i][0], y: wall.Points[i][1]},
			})
		}
	}
	return out
}

func sceneDoors(raw json.RawMessage) []doorSeg {
	var doors []struct {
		ID   string     `json:"id"`
		From [2]float64 `json:"from"`
		To   [2]float64 `json:"to"`
	}
	if err := json.Unmarshal(raw, &doors); err != nil {
		return nil
	}
	out := make([]doorSeg, 0, len(doors))
	for _, door := range doors {
		out = append(out, doorSeg{
			id: door.ID,
			segment: visSegment{
				a: visPoint{x: door.From[0], y: door.From[1]},
				b: visPoint{x: door.To[0], y: door.To[1]},
			},
		})
	}
	return out
}

// blocked проверяет, пересекает ли луч стену. Пересечение в открытом дверном
// проёме не считается препятствием: иначе стена под дверью блокировала бы взгляд.
func blocked(from, to visPoint, walls []visSegment, openDoors []visSegment) bool {
	for _, wall := range walls {
		hit, ok := segmentIntersection(from, to, wall.a, wall.b)
		if !ok {
			continue
		}
		inDoor := false
		for _, door := range openDoors {
			if pointSegmentDistance(hit, door.a, door.b) <= 1.0 {
				inDoor = true
				break
			}
		}
		if !inDoor {
			return true
		}
	}
	return false
}

func segmentIntersection(a, b, c, d visPoint) (visPoint, bool) {
	rx, ry := b.x-a.x, b.y-a.y
	sx, sy := d.x-c.x, d.y-c.y
	denom := rx*sy - ry*sx
	if math.Abs(denom) < 1e-9 {
		return visPoint{}, false
	}
	qpx, qpy := c.x-a.x, c.y-a.y
	t := (qpx*sy - qpy*sx) / denom
	u := (qpx*ry - qpy*rx) / denom
	if t < 0 || t > 1 || u < 0 || u > 1 {
		return visPoint{}, false
	}
	return visPoint{x: a.x + t*rx, y: a.y + t*ry}, true
}

func pointSegmentDistance(p, a, b visPoint) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	length := dx*dx + dy*dy
	if length == 0 {
		return math.Hypot(p.x-a.x, p.y-a.y)
	}
	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / length
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}
