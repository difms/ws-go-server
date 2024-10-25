package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-redis/redis"
	"github.com/gorilla/websocket"
)

var jwtSecret = []byte("8cdb58bd22d362908bd6e5f58a4d7a476dc7e9a7dac618f17d370e4afb11a134")
var redisClient *redis.Client

// upgrader - ДЛЯ ОТЛАДКИ
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// upgrader - ДЛЯ ПРОДА
// var upgrader = websocket.Upgrader{
//     CheckOrigin: func(r *http.Request) bool {
//         origin := r.Header.Get("Origin")
//         return origin == "http://ваш-домен.com" // Укажите разрешённый домен
//     },
// }

var clients = make(map[string]*websocket.Conn) // Карта userID -> соединение WebSocket

var nodeId = 1
var redisHost = "localhost:6379"
var serverPort = ":8080"
var wsURL = "/ws"

// User представляет пользователя
type User struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

type Notification struct {
	UserID  int                    `json:"user_id"`
	Data    map[string]interface{} `json:"data"` // или структура для Data
	Channel string                 `json:"channel"`
}

// AuthenticateWithSanctum проверяет токен и получает информацию о пользователе
func AuthenticateWithSanctum(token string) (*User, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "http://127.0.0.1:8000/api/getUser", nil) // Укажите правильный URL вашего API
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to authenticate: %s", resp.Status)
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

// JWT Middleware для проверки и получения userID из токена
func jwtMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		//authHeader := r.Header.Get("Authorization")
		authHeader := r.URL.Query().Get("token")
		log.Println("authHeader", authHeader)

		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		// IT USE SANCTUM TOKEN + AUTH
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		log.Println("tokenString", tokenString)

		// Получаем информацию о пользователе по токену
		user, err := AuthenticateWithSanctum(tokenString)
		if err != nil {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		r.Header.Set("UserID", fmt.Sprintf("%d", user.ID)) // Передаем userID дальше
		next.ServeHTTP(w, r)

		// IT USE JWT TOKEN
		// tokenString := strings.Split(authHeader, "Bearer ")[1]
		// log.Println("tokenString", tokenString)

		// token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// 	return jwtSecret, nil
		// })

		// log.Println("parsedToken", token)

		// if err != nil {
		// 	http.Error(w, "Invalid token", http.StatusUnauthorized)
		// 	return
		// }

		// if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// 	userID := fmt.Sprintf("%v", claims["user_id"])
		// 	log.Println("userID", userID)
		// 	r.Header.Set("UserID", userID) // Передаем userID дальше
		// 	next.ServeHTTP(w, r)
		// } else {
		// 	http.Error(w, "Invalid token", http.StatusUnauthorized)
		// }
	}
}

// Обработка подключения WebSocket
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("UserID")
	log.Println("userID from middleware", userID)

	// Попытка апгрейда соединения
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Failed to upgrade:", err)
		return // Завершить функцию, чтобы избежать повторных вызовов WriteHeader
	}

	clients[userID] = conn // Добавляем клиента

	// Сохраняем информацию о подключении в Redis
	err = redisClient.Set(fmt.Sprintf("user:%s:node", userID), nodeId, 0).Err()
	if err != nil {
		log.Println("Failed to save to Redis:", err)
		return
	}

	defer func() {
		delete(clients, userID)
		conn.Close()
		redisClient.Del(fmt.Sprintf("user:%s:node", userID)) // Удаляем запись при отключении
	}()

	// Проверяем неподтвержденные сообщения для пользователя
	pendingMessages, err := redisClient.LRange(fmt.Sprintf("pending_messages:%s", userID), 0, -1).Result()
	if err != nil {
		log.Printf("Failed to retrieve pending messages from Redis: %v", err)
		return
	}

	for _, message := range pendingMessages {
		// Отправляем сообщение как есть, не изменяя его структуры
		// err := conn.WriteMessage(websocket.TextMessage, []byte(message))

		// Отправляем сообщение как Notification
		var notification Notification

		// Преобразуем JSON-строку `message` в структуру `Notification`
		if err := json.Unmarshal([]byte(message), &notification); err != nil {
			log.Printf("Failed to unmarshal message: %v", err)
			return
		}

		// Cериализуем всю структуру, а не только `Data`
		messageJSON, err := json.Marshal(notification)
		if err != nil {
			log.Printf("Failed to marshal notification: %v", err)
			return
		}

		// Отправка клиенту
		err = conn.WriteMessage(websocket.TextMessage, messageJSON)
		if err != nil {
			log.Printf("Failed to send message to user: %v", err)
		}

		if err != nil {
			log.Printf("Failed to send message to user %s: %v", userID, err)
		} else {
			log.Printf("Sending message to user %s: %s", userID, message)
		}
	}

	// Чистим сообщения в redis после отправки клиенту
	redisClient.Del(fmt.Sprintf("pending_messages:%s", userID))

	for {
		// Ожидание сообщений от клиента
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// Функция подписки на Redis каналы
func subscribeToRedis() {
	pubsub := redisClient.Subscribe("private-channel", "public-channel") // Подписка на оба канала
	log.Println("Подписались на redis: private-channel и public-channel")
	ch := pubsub.Channel()

	for msg := range ch {
		// Разбираем сообщение
		var data map[string]interface{}
		json.Unmarshal([]byte(msg.Payload), &data)

		var notification Notification

		// Преобразуем JSON-строку `message` в структуру `Notification`
		if err := json.Unmarshal([]byte(msg.Payload), &notification); err != nil {
			log.Printf("Failed to unmarshal message: %v", err)
			return
		}

		// Убедитесь, что вы сериализуете всю структуру, а не только `Data`
		messageJSON, err := json.Marshal(notification)
		if err != nil {
			log.Printf("Failed to marshal notification: %v", err)
			return
		}

		// Если мессадж является приватным, для конкретного юзера
		if msg.Channel == "private-channel" {
			userID := fmt.Sprintf("%v", data["user_id"])
			//message := fmt.Sprintf("%v", data["message"])

			// Проверяем, подключен ли пользователь к этой ноде
			if conn, ok := clients[userID]; ok {
				// Отправляем приватное сообщение пользователю как есть
				//err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload)) // Отправляем JSON-формат

				// Отправляем приватное сообщение пользователю как преобразованное в Notification
				err = conn.WriteMessage(websocket.TextMessage, messageJSON)
				if err != nil {
					log.Printf("Failed to send message to user %s: %v", userID, err)
				} else {
					log.Printf("Приватное сообщение отправлено пользователю %s", userID)
				}
			} else {
				log.Printf("User id: %s - Client is not connected. Saving message to Redis.", userID)

				// Если клиент не подключен - сохраняем сообщения для него в redis для дальнейшей отправки в исходном формате
				err := redisClient.LPush(fmt.Sprintf("pending_messages:%s", userID), msg.Payload).Err()
				log.Printf("Message saved to Redis for user: %s", msg.Payload)
				if err != nil {
					log.Printf("Failed to save message to Redis: %v", err)
				} else {
					log.Printf("Message saved to Redis for user %s", userID)
				}
			}

			// Если мессадж является публичным
		} else if msg.Channel == "public-channel" {
			// // Отправляем публичное сообщение всем подключённым клиентам
			for _, conn := range clients {
				// Отправляем приватное сообщение пользователю как есть
				//err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload)) // Отправляем JSON-формат

				// Отправляем приватное сообщение пользователю как преобразованное в Notification
				err = conn.WriteMessage(websocket.TextMessage, messageJSON)
				if err != nil {
					log.Printf("Failed to send public message: %v", err)
				}
			}
			log.Println("Публичное сообщение отправлено всем пользователям")
		}
	}
}

func main() {
	// Инициализация клиента Redis
	redisClient = redis.NewClient(&redis.Options{
		Addr: redisHost,
	})

	// Запуск подписки на канал Redis в отдельной горутине
	go subscribeToRedis()

	// Запуск HTTP сервера для WebSocket
	http.HandleFunc(wsURL, jwtMiddleware(handleWebSocket))
	log.Println("WS Server started on port:", serverPort)
	log.Fatal(http.ListenAndServe(serverPort, nil))
}
