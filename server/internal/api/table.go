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
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Channel  string `json:"channel,omitempty"`
	IntentID string `json:"intent_id,omitempty"`
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
	conn, err := tableUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	room := s.hub.room(campaign.ID)
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
			"tokens":   []any{},
		},
		"actions": []string{"chat.say"},
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
		default:
			_ = room.send(conn, map[string]any{
				"type":      "error",
				"intent_id": message.IntentID,
				"error":     map[string]string{"code": "bad_request", "message": "незнакомое действие"},
			})
		}
	}
}
