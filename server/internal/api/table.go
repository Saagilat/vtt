package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// tableHub — комнаты стола по кампаниям. Пока в комнате только чат и hello;
// токены и сцены добавятся поверх того же канала.
type tableHub struct {
	mu    sync.Mutex
	rooms map[string]*tableRoom
}

type tableClient struct {
	account Account
	role    string
	preview string
}

type tableRoom struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]tableClient
	seq     int64
	sceneID string
}

func newTableHub() *tableHub {
	return &tableHub{rooms: map[string]*tableRoom{}}
}

func (h *tableHub) room(id string) *tableRoom {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[id]
	if !ok {
		room = &tableRoom{clients: map[*websocket.Conn]tableClient{}}
		h.rooms[id] = room
	}
	return room
}

var tableUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

type tableMessage struct {
	Type        string  `json:"type"`
	Text        string  `json:"text,omitempty"`
	Channel     string  `json:"channel,omitempty"`
	To          string  `json:"to,omitempty"`
	IntentID    string  `json:"intent_id,omitempty"`
	TokenID     string  `json:"token_id,omitempty"`
	SceneID     string  `json:"scene_id,omitempty"`
	DoorID      string  `json:"door_id,omitempty"`
	Name        string  `json:"name,omitempty"`
	OwnerID     string  `json:"owner_id,omitempty"`
	CharacterID *string `json:"character_id,omitempty"`
	AccountID   string  `json:"account_id,omitempty"`
	Open        bool    `json:"open,omitempty"`
	X           float64 `json:"x,omitempty"`
	Y           float64 `json:"y,omitempty"`
	HP          *int    `json:"hp,omitempty"`
	HPMax       *int    `json:"hp_max,omitempty"`
	Initiative  *int    `json:"initiative,omitempty"`
	Darkvision  *int    `json:"darkvision_ft,omitempty"`
	Formula     string  `json:"formula,omitempty"`
	Visibility  string  `json:"visibility,omitempty"`
	Light       string  `json:"light,omitempty"`
}

func (r *tableRoom) send(conn *websocket.Conn, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return conn.WriteJSON(value)
}

func (r *tableRoom) broadcast(value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for client := range r.clients {
		_ = client.WriteJSON(value)
	}
}

// broadcastIf рассылает событие только тем, кому оно адресовано: скрытый
// бросок не должен уходить всему столу.
func (r *tableRoom) broadcastIf(value any, allow func(tableClient) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for client, who := range r.clients {
		if allow(who) {
			_ = client.WriteJSON(value)
		}
	}
}

type tableClientConn struct {
	conn *websocket.Conn
	who  tableClient
}

func (r *tableRoom) snapshot() []tableClientConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]tableClientConn, 0, len(r.clients))
	for conn, who := range r.clients {
		out = append(out, tableClientConn{conn: conn, who: who})
	}
	return out
}

// chatHistory оставляет только то, что человек вправе видеть.
func (s *server) chatHistory(campaignID, accountID, role string) []ChatMessage {
	all := s.content.ChatMessages(campaignID, 50)
	out := make([]ChatMessage, 0, len(all))
	for _, message := range all {
		switch message.Channel {
		case "table":
			out = append(out, message)
		case "master":
			if master(role) || message.Actor.ID == accountID {
				out = append(out, message)
			}
		case "whisper":
			if message.Actor.ID == accountID || message.ToAccountID == accountID {
				out = append(out, message)
			}
		}
	}
	return out
}

// broadcastSceneChanged сообщает столу, что геометрия сцены изменилась.
// Клиенты перечитывают сцену; маска видимости приходит следующим событием.
func (s *server) broadcastSceneChanged(campaignID, sceneID string, actor Account) {
	room := s.hub.room(campaignID)
	room.broadcast(map[string]any{
		"type":     "scene.changed",
		"seq":      room.nextSeq(),
		"time":     s.now().UTC().Format(time.RFC3339),
		"scene_id": sceneID,
		"actor":    actor,
	})
}

func (s *server) broadcastCharacterChanged(campaignID string, character Character, actor Account) {
	room := s.hub.room(campaignID)
	room.broadcast(map[string]any{
		"type":      "character.changed",
		"seq":       room.nextSeq(),
		"time":      s.now().UTC().Format(time.RFC3339),
		"character": character,
		"actor":     actor,
	})
}

func (s *server) broadcastNoteChanged(campaignID string, note Note, actor Account) {
	room := s.hub.room(campaignID)
	room.broadcast(map[string]any{
		"type":  "note.changed",
		"seq":   room.nextSeq(),
		"time":  s.now().UTC().Format(time.RFC3339),
		"note":  note,
		"actor": actor,
	})
}

func (s *server) broadcastNoteRemoved(campaignID, noteID string, actor Account) {
	room := s.hub.room(campaignID)
	room.broadcast(map[string]any{
		"type":    "note.removed",
		"seq":     room.nextSeq(),
		"time":    s.now().UTC().Format(time.RFC3339),
		"note_id": noteID,
		"actor":   actor,
	})
}

func (s *server) accountVisibility(campaignID, sceneID, accountID string) (VisibilityMask, bool) {
	scene, ok := s.content.Scene(campaignID, sceneID)
	if !ok {
		return VisibilityMask{}, false
	}
	tokens := s.content.Tokens(campaignID, sceneID)
	doors := s.content.Doors(campaignID, sceneID)
	visible := visibleBits(scene, doors, tokens, accountID)
	explored := s.content.ExploredMask(campaignID, sceneID, accountID)
	if len(explored) != len(visible) {
		explored = make([]byte, len(visible))
	}
	for i := range explored {
		explored[i] |= visible[i]
	}
	s.content.SaveExploredMask(campaignID, sceneID, accountID, explored)
	return maskFromBits(visible, explored), true
}

func (r *tableRoom) setPreview(conn *websocket.Conn, accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if who, ok := r.clients[conn]; ok {
		who.preview = accountID
		r.clients[conn] = who
	}
}

// sendVisibility пересчитывает маску каждому игроку отдельно: у него своя фишка
// и свой туман. Мастеру маска не нужна, пока он не смотрит глазами игрока.
func (s *server) sendVisibility(room *tableRoom, campaignID, sceneID string) {
	if sceneID == "" {
		return
	}
	for _, client := range room.snapshot() {
		accountID := client.who.account.ID
		if master(client.who.role) {
			if client.who.preview == "" {
				continue
			}
			accountID = client.who.preview
		}
		mask, ok := s.accountVisibility(campaignID, sceneID, accountID)
		if !ok {
			continue
		}
		event := map[string]any{
			"type": "visibility",
			"seq":  room.nextSeq(),
			"time": s.now().UTC().Format(time.RFC3339),
			"mask": mask,
		}
		if master(client.who.role) {
			if target, ok := s.dir.ByID(accountID); ok {
				event["as_account"] = target
			}
		}
		_ = room.send(client.conn, event)
	}
}

func (r *tableRoom) nextSeq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return r.seq
}

func (r *tableRoom) currentScene() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sceneID
}

func (r *tableRoom) setScene(sceneID string) {
	r.mu.Lock()
	r.sceneID = sceneID
	r.mu.Unlock()
}

func doorExists(scene Scene, doorID string) bool {
	var doors []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(scene.Doors, &doors); err != nil {
		return false
	}
	for _, door := range doors {
		if door.ID == doorID {
			return true
		}
	}
	return false
}

func (s *server) table(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	requested := r.URL.Query().Get("scene_id")
	if requested == "" {
		for _, scene := range s.content.Scenes(campaign.ID) {
			if scene.Active {
				requested = scene.ID
				break
			}
		}
	}
	room := s.hub.room(campaign.ID)
	room.mu.Lock()
	if room.sceneID == "" {
		room.sceneID = requested
	}
	sceneID := room.sceneID
	room.mu.Unlock()
	if sceneID != "" {
		if _, ok := s.content.Scene(campaign.ID, sceneID); !ok {
			sceneID = ""
			if _, ok := s.content.Scene(campaign.ID, requested); ok {
				sceneID = requested
			}
			room.mu.Lock()
			room.sceneID = sceneID
			room.mu.Unlock()
		}
	}
	tokens := []Token{}
	if sceneID != "" {
		tokens = s.content.Tokens(campaign.ID, sceneID)
	}
	doors := s.content.Doors(campaign.ID, sceneID)
	var visibility any
	if !master(campaign.Role) && sceneID != "" {
		if mask, ok := s.accountVisibility(campaign.ID, sceneID, account.ID); ok {
			visibility = mask
		}
	}
	actions := []string{"chat.say", "token.move", "token.update", "roll"}
	if master(campaign.Role) {
		actions = append(actions, "token.place", "scene.show", "door.set", "turn.next", "visibility.peek", "scene.light")
	}
	turnToken, turnRound := s.content.Turn(campaign.ID, sceneID)
	conn, err := tableUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	hello := map[string]any{
		"type": "hello",
		"seq":  room.nextSeq(),
		"time": s.now().UTC().Format(time.RFC3339),
		"you": map[string]any{
			"account": account,
			"role":    campaign.Role,
		},
		"state": map[string]any{
			"campaign": map[string]string{"id": campaign.ID, "name": campaign.Name},
			"scene_id": sceneID,
			"tokens":   tokens,
			"doors":    doors,
			"turn": map[string]any{
				"token_id": turnToken,
				"round":    turnRound,
			},
			"visibility": visibility,
			"chat":       s.chatHistory(campaign.ID, account.ID, campaign.Role),
		},
		"actions": actions,
	}
	room.mu.Lock()
	room.clients[conn] = tableClient{account: account, role: campaign.Role}
	err = conn.WriteJSON(hello)
	room.mu.Unlock()
	if err != nil {
		room.mu.Lock()
		delete(room.clients, conn)
		room.mu.Unlock()
		return
	}
	defer func() {
		room.mu.Lock()
		delete(room.clients, conn)
		room.mu.Unlock()
	}()

	for {
		var message tableMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		switch message.Type {
		case "chat.say":
			text := strings.TrimSpace(message.Text)
			if text == "" || len(text) > 2000 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "пустое или слишком длинное сообщение"},
				})
				continue
			}
			channel := message.Channel
			if channel == "" {
				channel = "table"
			}
			toAccountID := ""
			switch channel {
			case "table", "master":
			case "whisper":
				toAccountID = message.To
				if toAccountID == "" {
					_ = room.send(conn, map[string]any{
						"type":      "error",
						"intent_id": message.IntentID,
						"error":     map[string]string{"code": "bad_request", "message": "кому шёпот?"},
					})
					continue
				}
				if _, ok := s.content.Campaign(toAccountID, campaign.ID); !ok {
					_ = room.send(conn, map[string]any{
						"type":      "error",
						"intent_id": message.IntentID,
						"error":     map[string]string{"code": "bad_request", "message": "получатель не в кампании"},
					})
					continue
				}
			default:
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "незнакомый канал чата"},
				})
				continue
			}
			stored, ok := s.content.AddChatMessage(campaign.ID, account.ID, toAccountID, text, channel)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "conflict", "message": "сообщение не сохранилось"},
				})
				continue
			}
			event := map[string]any{
				"type":       "chat.said",
				"seq":        room.nextSeq(),
				"time":       s.now().UTC().Format(time.RFC3339),
				"message_id": stored.ID,
				"text":       text,
				"channel":    channel,
				"actor":      account,
			}
			if toAccountID != "" {
				if target, ok := s.dir.ByID(toAccountID); ok {
					event["to"] = target
				}
			}
			switch channel {
			case "master":
				room.broadcastIf(event, func(who tableClient) bool {
					return master(who.role) || who.account.ID == account.ID
				})
			case "whisper":
				room.broadcastIf(event, func(who tableClient) bool {
					return who.account.ID == toAccountID || who.account.ID == account.ID
				})
			default:
				room.broadcast(event)
			}
		case "roll":
			result, err := parseRoll(message.Formula)
			if err != nil {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": err.Error()},
				})
				continue
			}
			visibility := message.Visibility
			if visibility == "" {
				visibility = "table"
			}
			if visibility != "table" && visibility != "self" && visibility != "master" {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "незнакомая видимость броска"},
				})
				continue
			}
			event := map[string]any{
				"type":       "roll",
				"seq":        room.nextSeq(),
				"time":       s.now().UTC().Format(time.RFC3339),
				"formula":    result.Formula,
				"values":     result.Values,
				"modifier":   result.Modifier,
				"total":      result.Total,
				"visibility": visibility,
				"actor":      account,
			}
			switch visibility {
			case "self":
				_ = room.send(conn, event)
			case "master":
				room.broadcastIf(event, func(who tableClient) bool {
					return master(who.role) || who.account.ID == account.ID
				})
			default:
				room.broadcast(event)
			}
		case "visibility.peek":
			if !master(campaign.Role) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "смотреть глазами игрока может мастер"},
				})
				continue
			}
			room.setPreview(conn, message.AccountID)
			if message.AccountID == "" {
				continue
			}
			target, ok := s.dir.ByID(message.AccountID)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "игрока нет"},
				})
				continue
			}
			if _, ok := s.content.Campaign(message.AccountID, campaign.ID); !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "игрок не в кампании"},
				})
				continue
			}
			current := room.currentScene()
			mask, ok := s.accountVisibility(campaign.ID, current, message.AccountID)
			if !ok {
				continue
			}
			_ = room.send(conn, map[string]any{
				"type":       "visibility",
				"seq":        room.nextSeq(),
				"time":       s.now().UTC().Format(time.RFC3339),
				"mask":       mask,
				"as_account": target,
			})
		case "scene.light":
			if !master(campaign.Role) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "светом управляет мастер"},
				})
				continue
			}
			if message.Light != "bright" && message.Light != "dark" {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "свет: bright или dark"},
				})
				continue
			}
			current := room.currentScene()
			if !s.content.SetSceneLight(campaign.ID, current, message.Light) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "свет не сохранился"},
				})
				continue
			}
			s.broadcastSceneChanged(campaign.ID, current, account)
			s.sendVisibility(room, campaign.ID, current)
		case "scene.show":
			if !master(campaign.Role) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "сцену показывает мастер"},
				})
				continue
			}
			var shown SceneSummary
			found := false
			for _, item := range s.content.Scenes(campaign.ID) {
				if item.ID == message.SceneID {
					shown = item
					found = true
					break
				}
			}
			if !found {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "сцены нет"},
				})
				continue
			}
			s.content.SetActiveScene(campaign.ID, shown.ID)
			turnToken, turnRound := s.content.Turn(campaign.ID, shown.ID)
			room.setScene(shown.ID)
			room.broadcast(map[string]any{
				"type":   "scene.shown",
				"seq":    room.nextSeq(),
				"time":   s.now().UTC().Format(time.RFC3339),
				"scene":  shown,
				"tokens": s.content.Tokens(campaign.ID, shown.ID),
				"doors":  s.content.Doors(campaign.ID, shown.ID),
				"turn":   map[string]any{"token_id": turnToken, "round": turnRound},
				"actor":  account,
			})
			s.sendVisibility(room, campaign.ID, shown.ID)
		case "door.set":
			if !master(campaign.Role) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "дверями управляет мастер"},
				})
				continue
			}
			current := room.currentScene()
			scene, ok := s.content.Scene(campaign.ID, current)
			if !ok || !doorExists(scene, message.DoorID) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "двери нет"},
				})
				continue
			}
			if !s.content.SetDoor(campaign.ID, current, message.DoorID, message.Open) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "дверь не сохранилась"},
				})
				continue
			}
			room.broadcast(map[string]any{
				"type":    "door.set",
				"seq":     room.nextSeq(),
				"time":    s.now().UTC().Format(time.RFC3339),
				"door_id": message.DoorID,
				"open":    message.Open,
				"actor":   account,
			})
			s.sendVisibility(room, campaign.ID, current)
		case "turn.next":
			if !master(campaign.Role) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "ходом управляет мастер"},
				})
				continue
			}
			current := room.currentScene()
			tokens := s.content.Tokens(campaign.ID, current)
			if len(tokens) == 0 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "на сцене нет фишек"},
				})
				continue
			}
			sort.SliceStable(tokens, func(i, j int) bool {
				return tokens[i].Initiative > tokens[j].Initiative
			})
			currentID, round := s.content.Turn(campaign.ID, current)
			if round < 1 {
				round = 1
			}
			next := 0
			if currentID != "" {
				for i, token := range tokens {
					if token.ID == currentID {
						next = i + 1
						break
					}
				}
				if next >= len(tokens) {
					next = 0
					round++
				}
			}
			s.content.SetTurn(campaign.ID, current, tokens[next].ID, round)
			room.broadcast(map[string]any{
				"type":     "turn.set",
				"seq":      room.nextSeq(),
				"time":     s.now().UTC().Format(time.RFC3339),
				"token_id": tokens[next].ID,
				"round":    round,
				"actor":    account,
			})
		case "token.place":
			current := room.currentScene()
			if !master(campaign.Role) {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "ставить фишки может мастер"},
				})
				continue
			}
			if current == "" || message.X < 0 || message.Y < 0 || message.X > 100000 || message.Y > 100000 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "нет сцены или плохие координаты"},
				})
				continue
			}
			ownerID := message.OwnerID
			name := message.Name
			characterID := ""
			hp, hpMax := 10, 10
			if message.CharacterID != nil && *message.CharacterID != "" {
				character, ok := s.content.Character(campaign.ID, *message.CharacterID)
				if !ok {
					_ = room.send(conn, map[string]any{
						"type":      "error",
						"intent_id": message.IntentID,
						"error":     map[string]string{"code": "bad_request", "message": "листа нет"},
					})
					continue
				}
				characterID = character.ID
				if ownerID == "" {
					ownerID = character.OwnerID
				}
				if strings.TrimSpace(name) == "" {
					name = character.Name
				}
				hp, hpMax = characterStats(character)
			}
			if message.HPMax != nil {
				hpMax = *message.HPMax
			}
			if message.HP != nil {
				hp = *message.HP
			}
			if hpMax < 1 || hpMax > 9999 {
				hpMax = 10
			}
			if hp < 0 {
				hp = 0
			}
			if hp > hpMax {
				hp = hpMax
			}
			if ownerID == "" {
				ownerID = account.ID
			} else if _, ok := s.content.Campaign(ownerID, campaign.ID); !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "владелец не в кампании"},
				})
				continue
			}
			token, ok := s.content.CreateToken(campaign.ID, current, ownerID, name, message.X, message.Y)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишка не создалась"},
				})
				continue
			}
			if characterID != "" {
				if linked, ok := s.content.SetTokenCharacter(campaign.ID, current, token.ID, characterID); ok {
					token = linked
				}
			}
			if updated, ok := s.content.SetTokenStats(campaign.ID, current, token.ID, hp, hpMax, token.Initiative); ok {
				token = updated
			}
			if message.Darkvision != nil {
				if *message.Darkvision < 0 || *message.Darkvision > 300 {
					_ = room.send(conn, map[string]any{
						"type":      "error",
						"intent_id": message.IntentID,
						"error":     map[string]string{"code": "bad_request", "message": "тёмное зрение от 0 до 300 футов"},
					})
					continue
				}
				if updated, ok := s.content.SetTokenDarkvision(campaign.ID, current, token.ID, *message.Darkvision); ok {
					token = updated
				}
			}
			room.broadcast(map[string]any{
				"type":  "token.placed",
				"seq":   room.nextSeq(),
				"time":  s.now().UTC().Format(time.RFC3339),
				"token": token,
				"actor": account,
			})
			s.sendVisibility(room, campaign.ID, current)
		case "token.move":
			current := room.currentScene()
			token, ok := s.content.Token(campaign.ID, current, message.TokenID)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишки нет"},
				})
				continue
			}
			if !master(campaign.Role) && token.OwnerID != account.ID {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "чужая фишка"},
				})
				continue
			}
			if message.X < 0 || message.Y < 0 || message.X > 100000 || message.Y > 100000 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "плохие координаты"},
				})
				continue
			}
			moved, ok := s.content.MoveToken(campaign.ID, current, message.TokenID, message.X, message.Y)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишка не сдвинулась"},
				})
				continue
			}
			room.broadcast(map[string]any{
				"type":  "token.moved",
				"seq":   room.nextSeq(),
				"time":  s.now().UTC().Format(time.RFC3339),
				"token": moved,
				"actor": account,
			})
			s.sendVisibility(room, campaign.ID, current)
		case "token.update":
			current := room.currentScene()
			token, ok := s.content.Token(campaign.ID, current, message.TokenID)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишки нет"},
				})
				continue
			}
			if !master(campaign.Role) && token.OwnerID != account.ID {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "forbidden", "message": "чужая фишка"},
				})
				continue
			}
			characterID := token.CharacterID
			characterHP, characterHPMax := 0, 0
			if message.CharacterID != nil {
				if !master(campaign.Role) {
					_ = room.send(conn, map[string]any{
						"type":      "error",
						"intent_id": message.IntentID,
						"error":     map[string]string{"code": "forbidden", "message": "лист назначает мастер"},
					})
					continue
				}
				characterID = *message.CharacterID
				if characterID != "" {
					character, ok := s.content.Character(campaign.ID, characterID)
					if !ok {
						_ = room.send(conn, map[string]any{
							"type":      "error",
							"intent_id": message.IntentID,
							"error":     map[string]string{"code": "bad_request", "message": "листа нет"},
						})
						continue
					}
					characterHP, characterHPMax = characterStats(character)
				}
			}
			hp, hpMax, initiative := token.HP, token.HPMax, token.Initiative
			if characterHPMax > 0 && message.HPMax == nil {
				hpMax = characterHPMax
			}
			if characterHPMax > 0 && message.HP == nil {
				hp = characterHP
			}
			if message.HPMax != nil {
				hpMax = *message.HPMax
			}
			if hpMax < 1 || hpMax > 9999 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "предел HP от 1 до 9999"},
				})
				continue
			}
			if message.HP != nil {
				hp = *message.HP
			}
			if hp < 0 {
				hp = 0
			}
			if hp > hpMax {
				hp = hpMax
			}
			if message.Initiative != nil {
				initiative = *message.Initiative
			}
			if initiative < -100 || initiative > 1000 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "инициатива от -100 до 1000"},
				})
				continue
			}
			darkvision := token.Darkvision
			if message.Darkvision != nil {
				darkvision = *message.Darkvision
			}
			if darkvision < 0 || darkvision > 300 {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "bad_request", "message": "тёмное зрение от 0 до 300 футов"},
				})
				continue
			}
			updated, ok := s.content.SetTokenStats(campaign.ID, current, token.ID, hp, hpMax, initiative)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишка не сохранилась"},
				})
				continue
			}
			if message.CharacterID != nil && characterID != token.CharacterID {
				if linked, ok := s.content.SetTokenCharacter(campaign.ID, current, token.ID, characterID); ok {
					updated = linked
				}
			}
			if characterID != "" && (hp != token.HP || hpMax != token.HPMax) {
				if character, ok := s.content.SetCharacterHP(campaign.ID, characterID, hp, hpMax); ok {
					s.broadcastCharacterChanged(campaign.ID, character, account)
				}
			}
			if darkvision != token.Darkvision {
				if changed, ok := s.content.SetTokenDarkvision(campaign.ID, current, token.ID, darkvision); ok {
					updated = changed
				}
			}
			room.broadcast(map[string]any{
				"type":  "token.updated",
				"seq":   room.nextSeq(),
				"time":  s.now().UTC().Format(time.RFC3339),
				"token": updated,
				"actor": account,
			})
			if darkvision != token.Darkvision {
				s.sendVisibility(room, campaign.ID, current)
			}
		default:
			_ = room.send(conn, map[string]any{
				"type":      "error",
				"intent_id": message.IntentID,
				"error":     map[string]string{"code": "bad_request", "message": "незнакомое действие"},
			})
		}
	}
}
