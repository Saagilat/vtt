package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Saagilat/vtt/server/internal/annotation"
	"github.com/Saagilat/vtt/server/internal/api"
)

func schemaPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "contract", "annotation.v1.schema.json")
}

func newContentHandler(t *testing.T) (http.Handler, *DB, api.Account) {
	t.Helper()
	db := openTemp(t)
	account, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := annotation.LoadSchema(schemaPath(t))
	if err != nil {
		t.Fatalf("схема: %v", err)
	}
	handler, err := api.New(api.Config{
		Directory: db, Sessions: db, Content: db, Validator: validator,
		Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	return handler, db, account
}

func login(t *testing.T, handler http.Handler, login, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/sessions", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("вход %s: %d %s", login, rec.Code, rec.Body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session.Token
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func multipartBody(t *testing.T, imageData, annotationData []byte, name string) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	imagePart, _ := writer.CreateFormFile("image", "map.png")
	imagePart.Write(imageData)
	annotationPart, _ := writer.CreateFormFile("annotation", "annotation.json")
	annotationPart.Write(annotationData)
	if name != "" {
		writer.WriteField("name", name)
	}
	writer.Close()
	return buffer.Bytes(), writer.FormDataContentType()
}

func annotationFor(width, height int) []byte {
	data, _ := json.Marshal(map[string]any{
		"format_version": 1,
		"image":          map[string]any{"width": width, "height": height},
		"grid":           map[string]any{"mode": "none"},
		"walls": []any{map[string]any{
			"id": "w1", "points": []any{[]any{10, 10}, []any{50, 10}},
		}},
		"doors": []any{},
		"areas": []any{},
	})
	return data
}

func TestCampaignCreateAndDetail(t *testing.T) {
	handler, _, account := newContentHandler(t)
	token := login(t, handler, "alice", "пароль")

	create := httptest.NewRequest("POST", "/v1/campaigns",
		strings.NewReader(`{"name":"Тени над Валдхеймом"}`))
	create.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("создание: %d %s", rec.Code, rec.Body)
	}
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}
	if campaign.ID == "" || campaign.Role != api.RoleOwner {
		t.Fatalf("кампания: %+v", campaign)
	}

	detail := httptest.NewRequest("GET", "/v1/campaigns/"+campaign.ID, nil)
	detail.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, detail)
	if rec.Code != http.StatusOK {
		t.Fatalf("детали: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), account.Login) ||
		!strings.Contains(rec.Body.String(), api.RoleOwner) {
		t.Fatalf("детали: %s", rec.Body)
	}
}

func TestImportSceneAndRead(t *testing.T) {
	handler, db, _ := newContentHandler(t)
	alice := login(t, handler, "alice", "пароль")
	bob, err := db.CreateAccount("bob", "пароль")
	if err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRequest("POST", "/v1/campaigns", strings.NewReader(`{"name":"Подземелье"}`))
	create.Header.Set("Authorization", "Bearer "+alice)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(bob.ID, campaign.ID, api.RolePlayer); err != nil {
		t.Fatal(err)
	}
	bobToken := login(t, handler, "bob", "пароль")

	imageData := testPNG(t, 200, 120)
	body, contentType := multipartBody(t, imageData, annotationFor(200, 120), "Пещера")
	importRequest := httptest.NewRequest("POST", "/v1/campaigns/"+campaign.ID+"/scenes:import",
		bytes.NewReader(body))
	importRequest.Header.Set("Content-Type", contentType)
	importRequest.Header.Set("Authorization", "Bearer "+alice)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, importRequest)
	if rec.Code != http.StatusCreated {
		t.Fatalf("импорт: %d %s", rec.Code, rec.Body)
	}
	var scene api.Scene
	if err := json.Unmarshal(rec.Body.Bytes(), &scene); err != nil {
		t.Fatal(err)
	}
	if scene.ID == "" || !scene.Active || len(scene.Walls) == 0 {
		t.Fatalf("сцена: %+v", scene)
	}

	for _, test := range []struct {
		token   string
		hasWall bool
	}{{alice, true}, {bobToken, false}} {
		get := httptest.NewRequest("GET",
			"/v1/campaigns/"+campaign.ID+"/scenes/"+scene.ID, nil)
		get.Header.Set("Authorization", "Bearer "+test.token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, get)
		if rec.Code != http.StatusOK {
			t.Fatalf("чтение сцены: %d %s", rec.Code, rec.Body)
		}
		hasWall := strings.Contains(rec.Body.String(), `"w1"`)
		if hasWall != test.hasWall {
			t.Fatalf("стены в сцене: %v, ждали %v: %s", hasWall, test.hasWall, rec.Body)
		}
	}

	sum := sha256.Sum256(imageData)
	first, err := db.AddImage(campaign.ID, imageData, "image/png", 200, 120, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.AddImage(campaign.ID, imageData, "image/png", 200, 120, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("повтор картинки создал копию: %s и %s", first.ID, second.ID)
	}

	badBody, badType := multipartBody(t, imageData, annotationFor(200, 120), "")
	bad := httptest.NewRequest("POST", "/v1/campaigns/"+campaign.ID+"/scenes:import",
		bytes.NewReader(badBody))
	bad.Header.Set("Content-Type", badType)
	bad.Header.Set("Authorization", "Bearer "+bobToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bad)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("импорт игроком: %d %s", rec.Code, rec.Body)
	}
}

func TestImportRejectsBrokenAnnotation(t *testing.T) {
	handler, _, _ := newContentHandler(t)
	token := login(t, handler, "alice", "пароль")
	create := httptest.NewRequest("POST", "/v1/campaigns", strings.NewReader(`{"name":"Карта"}`))
	create.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}

	broken := []byte(`{"format_version":1,"image":{"width":200,"height":120},
		"grid":{"mode":"square"},"walls":[],"doors":[],"areas":[]}`)
	body, contentType := multipartBody(t, testPNG(t, 200, 120), broken, "")
	request := httptest.NewRequest("POST", "/v1/campaigns/"+campaign.ID+"/scenes:import",
		bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("плохая разметка: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `contract_code`) {
		t.Fatalf("нет contract_code: %s", rec.Body)
	}
}

func TestTableChat(t *testing.T) {
	handler, db, _ := newContentHandler(t)
	token := login(t, handler, "alice", "пароль")

	create := httptest.NewRequest("POST", "/v1/campaigns", strings.NewReader(`{"name":"Стол"}`))
	create.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}
	image, err := db.AddImage(campaign.ID, testPNG(t, 64, 64), "image/png", 64, 64, "sha-test")
	if err != nil {
		t.Fatal(err)
	}
	scene, err := db.CreateScene(campaign.ID, image.ID, "Зал", annotationFor(64, 64), true)
	if err != nil {
		t.Fatal(err)
	}
	scene2, err := db.CreateScene(campaign.ID, image.ID, "Пещера", annotationFor(64, 64), false)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/v1/campaigns/" + campaign.ID + "/table?scene_id=" + scene.ID
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("websocket: %v", err)
	}
	defer conn.Close()

	var hello map[string]any
	if err := conn.ReadJSON(&hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("первый кадр: %+v", hello)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "chat.say", "text": "привет столу", "intent_id": "i1",
	}); err != nil {
		t.Fatal(err)
	}
	var reply map[string]any
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if reply["type"] != "chat.said" || reply["text"] != "привет столу" {
		t.Fatalf("ответ чата: %+v", reply)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "token.place", "name": "Герой", "x": 20, "y": 30, "intent_id": "i2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("token.place: %v", err)
	}
	placed, ok := reply["token"].(map[string]any)
	if reply["type"] != "token.placed" || !ok || placed["name"] != "Герой" {
		t.Fatalf("ответ фишки: %+v", reply)
	}
	tokenID, _ := placed["id"].(string)
	if err := conn.WriteJSON(map[string]any{
		"type": "token.move", "token_id": tokenID, "x": 40, "y": 50, "intent_id": "i3",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("token.move: %v", err)
	}
	moved, ok := reply["token"].(map[string]any)
	if reply["type"] != "token.moved" || !ok || moved["x"] != float64(40) {
		t.Fatalf("ответ движения: %+v", reply)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "scene.show", "scene_id": scene2.ID, "intent_id": "i4",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("scene.show: %v", err)
	}
	shown, ok := reply["scene"].(map[string]any)
	if reply["type"] != "scene.shown" || !ok || shown["id"] != scene2.ID {
		t.Fatalf("ответ сцены: %+v", reply)
	}
	for _, item := range db.Scenes(campaign.ID) {
		if item.ID == scene2.ID && !item.Active {
			t.Fatalf("сцена не стала активной")
		}
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "token.place", "name": "Второй", "x": 5, "y": 6, "intent_id": "i5",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("token на новой сцене: %v", err)
	}
	second, ok := reply["token"].(map[string]any)
	if reply["type"] != "token.placed" || !ok || second["scene_id"] != scene2.ID {
		t.Fatalf("фишка не переехала на новую сцену: %+v", reply)
	}
}
