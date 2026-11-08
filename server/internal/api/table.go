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
	Type       string  `json:"type"`
	Text       string  `json:"text,omitempty"`
	Channel    string  `json:"channel,omitempty"`
	IntentID   string  `json:"intent_id,omitempty"`
	TokenID    string  `json:"token_id,omitempty"`
	SceneID    string  `json:"scene_id,omitempty"`
	DoorID     string  `json:"door_id,omitempty"`
	Name       string  `json:"name,omitempty"`
	OwnerID    string  `json:"owner_id,omitempty"`
	Open       bool    `json:"open,omitempty"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	HP         *int    `json:"hp,omitempty"`
	HPMax      *int    `json:"hp_max,omitempty"`
	Initiative *int    `json:"initiative,omitempty"`
	Formula    string  `json:"formula,omitempty"`
	Visibility string  `json:"visibility,omitempty"`
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

// sendVisibility пересчитывает маску каждому игроку отдельно: у него своя фишка
// и свой туман. Мастеру маска не нужна — он видит геометрию целиком.
func (s *server) sendVisibility(room *tableRoom, campaignID, sceneID string) {
	if sceneID == "" {
		return
	}
	scene, ok := s.content.Scene(campaignID, sceneID)
	if !ok {
		return
	}
	tokens := s.content.Tokens(campaignID, sceneID)
	doors := s.content.Doors(campaignID, sceneID)
	for _, client := range room.snapshot() {
		if master(client.who.role) {
			continue
		}
		mask := visibilityFor(scene, doors, tokens, client.who.account.ID)
		_ = room.send(client.conn, map[string]any{
			"type": "visibility",
			"seq":  room.nextSeq(),
			"time": s.now().UTC().Format(time.RFC3339),
			"mask": mask,
		})
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
		if scene, ok := s.content.Scene(campaign.ID, sceneID); ok {
			mask := visibilityFor(scene, doors, tokens, account.ID)
			visibility = mask
		}
	}
	actions := []string{"chat.say", "token.move", "token.update", "roll"}
	if master(campaign.Role) {
		actions = append(actions, "token.place", "scene.show", "door.set", "turn.next")
	}
	turnToken, turnRound := s.content.Turn(campaign.ID)
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
			seq := room.nextSeq()
			room.broadcast(map[string]any{
				"type":    "chat.said",
				"seq":     seq,
				"time":    s.now().UTC().Format(time.RFC3339),
				"text":    text,
				"channel": channel,
				"actor":   account,
			})
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
			s.content.SetTurn(campaign.ID, "", 0)
			room.setScene(shown.ID)
			room.broadcast(map[string]any{
				"type":   "scene.shown",
				"seq":    room.nextSeq(),
				"time":   s.now().UTC().Format(time.RFC3339),
				"scene":  shown,
				"tokens": s.content.Tokens(campaign.ID, shown.ID),
				"doors":  s.content.Doors(campaign.ID, shown.ID),
				"turn":   map[string]any{"token_id": "", "round": 0},
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
			currentID, round := s.content.Turn(campaign.ID)
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
			s.content.SetTurn(campaign.ID, tokens[next].ID, round)
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
			token, ok := s.content.CreateToken(campaign.ID, current, ownerID, message.Name, message.X, message.Y)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишка не создалась"},
				})
				continue
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
			hp, hpMax, initiative := token.HP, token.HPMax, token.Initiative
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
			updated, ok := s.content.SetTokenStats(campaign.ID, current, token.ID, hp, hpMax, initiative)
			if !ok {
				_ = room.send(conn, map[string]any{
					"type":      "error",
					"intent_id": message.IntentID,
					"error":     map[string]string{"code": "not_found", "message": "фишка не сохранилась"},
				})
				continue
			}
			room.broadcast(map[string]any{
				"type":  "token.updated",
				"seq":   room.nextSeq(),
				"time":  s.now().UTC().Format(time.RFC3339),
				"token": updated,
				"actor": account,
			})
		default:
			_ = room.send(conn, map[string]any{
				"type":      "error",
				"intent_id": message.IntentID,
				"error":     map[string]string{"code": "bad_request", "message": "незнакомое действие"},
			})
		}
	}
}
