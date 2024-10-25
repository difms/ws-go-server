package main

import (
	"log"
	"net/http"
	"ws-go-server/auth"
	"ws-go-server/redisclient"
	"ws-go-server/ws"
)

func main() {
	// Initialize Redis client
	redisclient.Initialize()

	// Start Redis subscription in a separate goroutine
	go ws.SubscribeToRedis()

	// Start the HTTP server with WebSocket endpoint
	http.HandleFunc("/ws", auth.JWTMiddleware(ws.HandleWebSocket))
	log.Println("WS Server started!")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
