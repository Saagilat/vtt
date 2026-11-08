package api

import (
	"net/http"
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

type tableRoom struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
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
		room = &tableRoom{clients: map[*websocket.Conn]bool{}}
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
	Type     string  `json:"type"`
	Text     string  `json:"text,omitempty"`
	Channel  string  `json:"channel,omitempty"`
	IntentID string  `json:"intent_id,omitempty"`
	TokenID  string  `json:"token_id,omitempty"`
	SceneID  string  `json:"scene_id,omitempty"`
	Name     string  `json:"name,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
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
	actions := []string{"chat.say", "token.move"}
	if master(campaign.Role) {
		actions = append(actions, "token.place", "scene.show")
	}
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
		},
		"actions": actions,
	}
	room.mu.Lock()
	room.clients[conn] = true
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
			room.setScene(shown.ID)
			room.broadcast(map[string]any{
				"type":   "scene.shown",
				"seq":    room.nextSeq(),
				"time":   s.now().UTC().Format(time.RFC3339),
				"scene":  shown,
				"tokens": s.content.Tokens(campaign.ID, shown.ID),
				"actor":  account,
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
			token, ok := s.content.CreateToken(campaign.ID, current, account.ID, message.Name, message.X, message.Y)
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
		default:
			_ = room.send(conn, map[string]any{
				"type":      "error",
				"intent_id": message.IntentID,
				"error":     map[string]string{"code": "bad_request", "message": "незнакомое действие"},
			})
		}
	}
}
