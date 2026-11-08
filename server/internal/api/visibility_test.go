package api

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func testMask(t *testing.T, mask VisibilityMask) []byte {
	t.Helper()
	if mask.Blocks != visibilityBlocks {
		t.Fatalf("blocks=%d", mask.Blocks)
	}
	data, err := base64.StdEncoding.DecodeString(mask.Visible)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if len(data) != visibilityBlocks*visibilityBlocks/8 {
		t.Fatalf("размер маски %d", len(data))
	}
	return data
}

func maskBit(data []byte, x, y int) bool {
	bit := y*visibilityBlocks + x
	return data[bit/8]&(1<<uint(bit%8)) != 0
}

func TestVisibilityWallBlocksView(t *testing.T) {
	scene := Scene{
		Image: Image{Width: 100, Height: 100},
		Walls: json.RawMessage(`[{"id":"w1","points":[[50,0],[50,100]]}]`),
		Doors: json.RawMessage(`[{"id":"d1","wall_id":"w1","from":[50,40],"to":[50,60],"type":"normal"}]`),
	}
	token := Token{ID: "t1", OwnerID: "bob", X: 25, Y: 50}

	closed := testMask(t, visibilityFor(scene, map[string]bool{"d1": false}, []Token{token}, "bob"))
	if !maskBit(closed, 20, 128) {
		t.Fatal("слева от стены не видно")
	}
	if maskBit(closed, 200, 128) {
		t.Fatal("за закрытой дверью видно")
	}

	open := testMask(t, visibilityFor(scene, map[string]bool{"d1": true}, []Token{token}, "bob"))
	if !maskBit(open, 200, 128) {
		t.Fatal("через открытую дверь не видно")
	}
	if maskBit(open, 200, 20) {
		t.Fatal("через стену вне двери видно")
	}
}

func TestMaskFromBitsKeepsExplored(t *testing.T) {
	size := visibilityBlocks * visibilityBlocks / 8
	visible := make([]byte, size)
	visible[0] = 1 // блок 0
	explored := make([]byte, size)
	explored[0] = 2 // блок 1 был виден раньше
	mask := maskFromBits(visible, explored)
	_ = testMask(t, mask)
	seenExplored, err := base64.StdEncoding.DecodeString(mask.Explored)
	if err != nil {
		t.Fatalf("explored base64: %v", err)
	}
	if !maskBit(seenExplored, 0, 0) || !maskBit(seenExplored, 1, 0) {
		t.Fatal("текущая или накопленная видимость потерялась")
	}
	if seenExplored[0] != 3 {
		t.Fatalf("объединение масок: %08b", seenExplored[0])
	}
	if mask.Explored == "" {
		t.Fatal("нет explored в ответе")
	}
}

func TestVisibilityDarkvisionRadius(t *testing.T) {
	scene := Scene{
		Image: Image{Width: 100, Height: 100},
		Light: "dark",
		Grid:  json.RawMessage(`{"mode":"square","square":{"side_px":10}}`),
		Walls: json.RawMessage(`[]`),
		Doors: json.RawMessage(`[]`),
	}
	token := Token{ID: "t1", OwnerID: "bob", X: 50, Y: 50, Darkvision: 5}
	mask := testMask(t, visibilityFor(scene, nil, []Token{token}, "bob"))
	if !maskBit(mask, 128, 128) {
		t.Fatal("фишка не видит себя в темноте")
	}
	if maskBit(mask, 200, 128) {
		t.Fatal("тёмное зрение видит дальше радиуса")
	}

	blind := Token{ID: "t2", OwnerID: "carol", X: 50, Y: 50}
	dark := testMask(t, visibilityFor(scene, nil, []Token{blind}, "carol"))
	for _, value := range dark {
		if value != 0 {
			t.Fatal("без тёмного зрения в темноте что-то видно")
		}
	}
}

func TestVisibilityNeedsOwnToken(t *testing.T) {
	scene := Scene{
		Image: Image{Width: 100, Height: 100},
		Walls: json.RawMessage(`[]`),
		Doors: json.RawMessage(`[]`),
	}
	token := Token{ID: "t1", OwnerID: "alice", X: 50, Y: 50}
	mask := testMask(t, visibilityFor(scene, nil, []Token{token}, "bob"))
	for _, value := range mask {
		if value != 0 {
			t.Fatal("чужая фишка открыла туман")
		}
	}
}
