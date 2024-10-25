package ws

import (
	"log"
	"net/http"
	"ws-go-server/redisclient"

	"github.com/gorilla/websocket"
)

var clients = make(map[string]*websocket.Conn)

// Upgrader for WebSocket connection
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// HandleWebSocket handles WebSocket connections
func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("UserID")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Failed to upgrade:", err)
		return
	}

	clients[userID] = conn
	defer func() {
		delete(clients, userID)
		conn.Close()
		redisclient.RemoveUserConnection(userID)
	}()

	redisclient.SaveUserConnection(userID)
	redisclient.SendPendingMessages(userID, conn)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
