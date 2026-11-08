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
