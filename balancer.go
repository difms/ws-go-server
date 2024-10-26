package main

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var (
	// Список серверов где доступен ws сервер (обработчик)
	servers = []string{
		"ws://localhost:8081",
		"ws://125.37.32.144:8081",
		"https://ws.dfms.pw",
	}
	maxClients = 20000 // Максимальное количество подключений на сервер
)

type Server struct {
	URL             string
	ConnectionCount int
	mu              sync.Mutex
}

func (s *Server) Increment() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ConnectionCount++
}

func (s *Server) Decrement() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ConnectionCount--
}

func (s *Server) IsOverloaded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ConnectionCount >= maxClients
}

// Ведем список серверов
var serverList []*Server

// Инициализация серверов
func initServers() {
	for _, url := range servers {
		serverList = append(serverList, &Server{URL: url})
	}
}

// Выбор сервера для подключения
func getAvailableServer() *Server {
	for _, server := range serverList {
		if !server.IsOverloaded() {
			return server
		}
	}
	return nil
}

// Обработка подключения WebSocket
func handleConnection(w http.ResponseWriter, r *http.Request) {
	server := getAvailableServer()
	if server == nil {
		http.Error(w, "No available servers", http.StatusServiceUnavailable)
		return
	}

	// Подключение к выбранному серверу
	conn, _, err := websocket.DefaultDialer.Dial(server.URL, nil)
	if err != nil {
		http.Error(w, "Failed to connect to server", http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Увеличиваем количество подключений на сервере
	server.Increment()
	defer server.Decrement()

	// Обработка сообщений (здесь добавьте свою логику)
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}
		fmt.Println("Received:", string(message))
	}
}

func main() {
	initServers()
	http.HandleFunc("/ws", handleConnection)
	fmt.Println("Load Balancer started on :8080")
	http.ListenAndServe(":8080", nil)
}
