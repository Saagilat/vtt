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
		"doors": []any{map[string]any{
			"id": "d1", "wall_id": "w1", "type": "normal",
			"from": []any{10, 10}, "to": []any{50, 10},
		}},
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

	character, ok := db.CreateCharacter(campaign.ID, "", "Мира",
		[]byte(`{"class":"воин","level":2,"hp":9,"max_hp":14}`))
	if !ok {
		t.Fatal("лист для фишки не создался")
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "token.place", "character_id": character.ID, "x": 10, "y": 12, "intent_id": "i2b",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("token.place с листом: %v", err)
	}
	linked, ok := reply["token"].(map[string]any)
	if reply["type"] != "token.placed" || !ok || linked["character_id"] != character.ID ||
		linked["name"] != "Мира" || linked["hp"] != float64(9) || linked["hp_max"] != float64(14) {
		t.Fatalf("фишка с листом: %+v", reply)
	}
	linkedID, _ := linked["id"].(string)
	if err := conn.WriteJSON(map[string]any{
		"type": "token.update", "token_id": linkedID, "hp": 4, "intent_id": "i2c",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("character.changed: %v", err)
	}
	if reply["type"] != "character.changed" {
		t.Fatalf("нет события листа: %+v", reply)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("token.updated: %v", err)
	}
	if reply["type"] != "token.updated" {
		t.Fatalf("нет события фишки: %+v", reply)
	}
	character, ok = db.Character(campaign.ID, character.ID)
	if !ok {
		t.Fatal("лист потерялся")
	}
	var sheet map[string]any
	if err := json.Unmarshal(character.Data, &sheet); err != nil || sheet["hp"] != float64(4) {
		t.Fatalf("HP листа после фишки: %s", character.Data)
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
		"type": "door.set", "door_id": "d1", "open": true, "intent_id": "i4b",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("door.set: %v", err)
	}
	if reply["type"] != "door.set" || reply["open"] != true {
		t.Fatalf("ответ двери: %+v", reply)
	}
	if !db.Doors(campaign.ID, scene2.ID)["d1"] {
		t.Fatalf("дверь не сохранилась открытой")
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

func TestCharacters(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaignFor(alice.ID, "Вечер")
	if err != nil {
		t.Fatal(err)
	}
	character, ok := db.CreateCharacter(campaign.ID, alice.ID, "Мира",
		[]byte(`{"class":"следопыт","level":3,"hp":24,"max_hp":24}`))
	if !ok {
		t.Fatal("лист не создался")
	}
	if character.OwnerLogin != "alice" {
		t.Fatalf("лист: %+v", character)
	}
	var sheet map[string]any
	if err := json.Unmarshal(character.Data, &sheet); err != nil || sheet["level"] != float64(3) {
		t.Fatalf("данные листа: %s", character.Data)
	}
	updated, ok := db.UpdateCharacter(campaign.ID, character.ID, "Мира Тень",
		[]byte(`{"class":"следопыт","level":4,"hp":18,"max_hp":30}`))
	if !ok || updated.Name != "Мира Тень" {
		t.Fatalf("обновлённый лист: %+v", updated)
	}
	sheet = nil
	if err := json.Unmarshal(updated.Data, &sheet); err != nil || sheet["hp"] != float64(18) {
		t.Fatalf("данные обновлённого листа: %s", updated.Data)
	}
	items := db.Characters(campaign.ID)
	if len(items) != 1 || items[0].ID != character.ID {
		t.Fatalf("список листов: %+v", items)
	}
	if _, ok := db.Character(campaign.ID, "нет-такого"); ok {
		t.Fatal("найден несуществующий лист")
	}
}

func TestCharacterREST(t *testing.T) {
	handler, db, account := newContentHandler(t)
	alice := login(t, handler, "alice", "пароль")
	create := httptest.NewRequest("POST", "/v1/campaigns", strings.NewReader(`{"name":"Вечер"}`))
	create.Header.Set("Authorization", "Bearer "+alice)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}

	body := `{"name":"Мира","data":{"class":"следопыт","level":3,"hp":24,"max_hp":24}}`
	createCharacter := httptest.NewRequest("POST", "/v1/campaigns/"+campaign.ID+"/characters",
		strings.NewReader(body))
	createCharacter.Header.Set("Authorization", "Bearer "+alice)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, createCharacter)
	if rec.Code != http.StatusCreated {
		t.Fatalf("создание листа: %d %s", rec.Code, rec.Body)
	}
	var character api.Character
	if err := json.Unmarshal(rec.Body.Bytes(), &character); err != nil {
		t.Fatal(err)
	}
	if character.OwnerID != account.ID || character.Name != "Мира" {
		t.Fatalf("лист: %+v", character)
	}

	patch := httptest.NewRequest("PATCH",
		"/v1/campaigns/"+campaign.ID+"/characters/"+character.ID,
		strings.NewReader(`{"name":"Мира Тень","data":{"class":"следопыт","level":4,"hp":18}}`))
	patch.Header.Set("Authorization", "Bearer "+alice)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("правка листа: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &character); err != nil {
		t.Fatal(err)
	}
	if character.Name != "Мира Тень" || !strings.Contains(string(character.Data), `"level":4`) {
		t.Fatalf("правка: %+v", character)
	}

	bobAccount, err := db.CreateAccount("bob", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	bob := login(t, handler, "bob", "пароль")
	if err := db.AddMember(bobAccount.ID, campaign.ID, "player"); err != nil {
		t.Fatal(err)
	}
	foreign := httptest.NewRequest("PATCH",
		"/v1/campaigns/"+campaign.ID+"/characters/"+character.ID,
		strings.NewReader(`{"data":{}}`))
	foreign.Header.Set("Authorization", "Bearer "+bob)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, foreign)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("игрок не должен править чужой лист: %d %s", rec.Code, rec.Body)
	}
}

func TestTokenCharacterSync(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaignFor(alice.ID, "Вечер")
	if err != nil {
		t.Fatal(err)
	}
	image, err := db.AddImage(campaign.ID, testPNG(t, 64, 64), "image/png", 64, 64, "sync")
	if err != nil {
		t.Fatal(err)
	}
	scene, err := db.CreateScene(campaign.ID, image.ID, "Зал", annotationFor(64, 64), true)
	if err != nil {
		t.Fatal(err)
	}
	character, ok := db.CreateCharacter(campaign.ID, alice.ID, "Мира",
		[]byte(`{"class":"следопыт","level":3,"hp":7,"max_hp":12}`))
	if !ok {
		t.Fatal("лист не создался")
	}
	token, ok := db.CreateToken(campaign.ID, scene.ID, alice.ID, "Мира", 10, 10)
	if !ok {
		t.Fatal("фишка не создалась")
	}
	linked, ok := db.SetTokenCharacter(campaign.ID, scene.ID, token.ID, character.ID)
	if !ok || linked.CharacterID != character.ID || linked.CharacterName != "Мира" {
		t.Fatalf("связь с листом: %+v", linked)
	}
	if updated, ok := db.SetTokenStats(campaign.ID, scene.ID, token.ID, 7, 12, 0); !ok || updated.HP != 7 {
		t.Fatalf("HP фишки: %+v", updated)
	}
	if _, ok := db.SetCharacterHP(campaign.ID, character.ID, 3, 12); !ok {
		t.Fatal("HP листа не сохранились")
	}
	if !db.SetCharacterTokensHP(campaign.ID, character.ID, 3, 12) {
		t.Fatal("HP фишек не синхронизировались")
	}
	synced, ok := db.Token(campaign.ID, scene.ID, token.ID)
	if !ok || synced.HP != 3 || synced.HPMax != 12 {
		t.Fatalf("синхронизация HP: %+v", synced)
	}
	character, ok = db.Character(campaign.ID, character.ID)
	if !ok {
		t.Fatal("лист потерялся")
	}
	var sheet map[string]any
	if err := json.Unmarshal(character.Data, &sheet); err != nil || sheet["hp"] != float64(3) {
		t.Fatalf("HP в листе: %s", character.Data)
	}
	unlinked, ok := db.SetTokenCharacter(campaign.ID, scene.ID, token.ID, "")
	if !ok || unlinked.CharacterID != "" {
		t.Fatalf("связь не снялась: %+v", unlinked)
	}
}

func TestInviteJoinByCode(t *testing.T) {
	handler, db, account := newContentHandler(t)
	alice := login(t, handler, "alice", "пароль")
	if _, err := db.CreateAccount("bob", "пароль"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAccount("carol", "пароль"); err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRequest("POST", "/v1/campaigns", strings.NewReader(`{"name":"Вечер"}`))
	create.Header.Set("Authorization", "Bearer "+alice)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}

	inviteRequest := httptest.NewRequest("POST", "/v1/campaigns/"+campaign.ID+"/invites", nil)
	inviteRequest.Header.Set("Authorization", "Bearer "+alice)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, inviteRequest)
	if rec.Code != http.StatusCreated {
		t.Fatalf("приглашение: %d %s", rec.Code, rec.Body)
	}
	var invite api.Invite
	if err := json.Unmarshal(rec.Body.Bytes(), &invite); err != nil {
		t.Fatal(err)
	}
	if len(invite.Code) < 6 {
		t.Fatalf("короткий код: %q", invite.Code)
	}

	bobToken := login(t, handler, "bob", "пароль")
	accept := httptest.NewRequest("POST", "/v1/invites/"+invite.Code+"/accept", nil)
	accept.Header.Set("Authorization", "Bearer "+bobToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, accept)
	if rec.Code != http.StatusOK {
		t.Fatalf("вступление: %d %s", rec.Code, rec.Body)
	}
	var joined api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &joined); err != nil {
		t.Fatal(err)
	}
	if joined.ID != campaign.ID || joined.Role != api.RolePlayer {
		t.Fatalf("вступил не туда: %+v", joined)
	}

	// Повтор того же человека идемпотентен, чужой — уже использованный код.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, accept)
	if rec.Code != http.StatusOK {
		t.Fatalf("повторное вступление: %d %s", rec.Code, rec.Body)
	}
	carolToken := login(t, handler, "carol", "пароль")
	carolAccept := httptest.NewRequest("POST", "/v1/invites/"+invite.Code+"/accept", nil)
	carolAccept.Header.Set("Authorization", "Bearer "+carolToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, carolAccept)
	if rec.Code != http.StatusConflict {
		t.Fatalf("чужой код: %d %s", rec.Code, rec.Body)
	}

	expired, err := db.CreateInvite(campaign.ID, account.ID,
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	expiredRequest := httptest.NewRequest("POST", "/v1/invites/"+expired.Code+"/accept", nil)
	expiredRequest.Header.Set("Authorization", "Bearer "+carolToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, expiredRequest)
	if rec.Code != http.StatusConflict {
		t.Fatalf("истёкший код: %d %s", rec.Code, rec.Body)
	}

	// Код создаёт только владелец.
	bobMembership := httptest.NewRequest("POST", "/v1/campaigns/"+campaign.ID+"/invites", nil)
	bobMembership.Header.Set("Authorization", "Bearer "+bobToken)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bobMembership)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("код игроком: %d %s", rec.Code, rec.Body)
	}
}

func TestExploredMaskStored(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaignFor(alice.ID, "Туман")
	if err != nil {
		t.Fatal(err)
	}
	image, err := db.AddImage(campaign.ID, testPNG(t, 64, 64), "image/png", 64, 64, "sha-fog")
	if err != nil {
		t.Fatal(err)
	}
	scene, err := db.CreateScene(campaign.ID, image.ID, "Карта", annotationFor(64, 64), true)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte{1, 2, 3}
	if !db.SaveExploredMask(campaign.ID, scene.ID, alice.ID, first) {
		t.Fatal("маска не сохранилась")
	}
	got := db.ExploredMask(campaign.ID, scene.ID, alice.ID)
	if string(got) != string(first) {
		t.Fatalf("маска: %v", got)
	}
	second := []byte{9, 8}
	if !db.SaveExploredMask(campaign.ID, scene.ID, alice.ID, second) {
		t.Fatal("маска не перезаписалась")
	}
	if string(db.ExploredMask(campaign.ID, scene.ID, alice.ID)) != string(second) {
		t.Fatal("маска не заменилась")
	}
}

func TestChatHistory(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaignFor(alice.ID, "Чат")
	if err != nil {
		t.Fatal(err)
	}
	first, ok := db.AddChatMessage(campaign.ID, alice.ID, "", "первое", "table")
	if !ok {
		t.Fatal("первое сообщение не сохранилось")
	}
	second, ok := db.AddChatMessage(campaign.ID, alice.ID, "", "второе", "table")
	if !ok {
		t.Fatal("второе сообщение не сохранилось")
	}
	bob, err := db.CreateAccount("bob", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	whisper, ok := db.AddChatMessage(campaign.ID, alice.ID, bob.ID, "тихо", "whisper")
	if !ok || whisper.ToLogin != "bob" {
		t.Fatalf("шёпот: %+v", whisper)
	}
	items := db.ChatMessages(campaign.ID, 10)
	if len(items) != 3 || items[0].ID != first.ID || items[1].ID != second.ID {
		t.Fatalf("история: %+v", items)
	}
	if items[0].Actor.Login != "alice" {
		t.Fatalf("автор: %+v", items[0].Actor)
	}
}

func TestSceneTurnsIndependent(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaignFor(alice.ID, "Бой")
	if err != nil {
		t.Fatal(err)
	}
	image, err := db.AddImage(campaign.ID, testPNG(t, 64, 64), "image/png", 64, 64, "sha-turn")
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateScene(campaign.ID, image.ID, "Первая", annotationFor(64, 64), true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateScene(campaign.ID, image.ID, "Вторая", annotationFor(64, 64), false)
	if err != nil {
		t.Fatal(err)
	}
	if !db.SetTurn(campaign.ID, first.ID, "t1", 2) {
		t.Fatal("ход первой сцены не сохранился")
	}
	if !db.SetTurn(campaign.ID, second.ID, "t2", 1) {
		t.Fatal("ход второй сцены не сохранился")
	}
	if token, round := db.Turn(campaign.ID, first.ID); token != "t1" || round != 2 {
		t.Fatalf("первая сцена: %q, %d", token, round)
	}
	if token, round := db.Turn(campaign.ID, second.ID); token != "t2" || round != 1 {
		t.Fatalf("вторая сцена: %q, %d", token, round)
	}
}

func TestUpdateSceneAnnotation(t *testing.T) {
	handler, db, _ := newContentHandler(t)
	alice := login(t, handler, "alice", "пароль")
	create := httptest.NewRequest("POST", "/v1/campaigns", strings.NewReader(`{"name":"Редактор"}`))
	create.Header.Set("Authorization", "Bearer "+alice)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	var campaign api.Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}
	image, err := db.AddImage(campaign.ID, testPNG(t, 64, 64), "image/png", 64, 64, "sha-edit")
	if err != nil {
		t.Fatal(err)
	}
	scene, err := db.CreateScene(campaign.ID, image.ID, "Правка", annotationFor(64, 64), true)
	if err != nil {
		t.Fatal(err)
	}
	update := httptest.NewRequest("PUT",
		"/v1/campaigns/"+campaign.ID+"/scenes/"+scene.ID+"/annotation",
		bytes.NewReader(annotationFor(64, 64)))
	update.Header.Set("Authorization", "Bearer "+alice)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, update)
	if rec.Code != http.StatusOK {
		t.Fatalf("правка разметки: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"w1"`) {
		t.Fatalf("геометрия не вернулась: %s", rec.Body)
	}

	stale := httptest.NewRequest("PUT",
		"/v1/campaigns/"+campaign.ID+"/scenes/"+scene.ID+"/annotation?version=1",
		bytes.NewReader(annotationFor(64, 64)))
	stale.Header.Set("Authorization", "Bearer "+alice)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, stale)
	if rec.Code != http.StatusConflict {
		t.Fatalf("устаревшая правка: %d %s", rec.Code, rec.Body)
	}
}
