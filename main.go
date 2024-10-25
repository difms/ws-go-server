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

// User представляет пользователя
type User struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

type Notification struct {
	UserID uint `json:"user_id"`
	Data   struct {
		Event   string `json:"event"`
		Message string `json:"message"`
	} `json:"data"`
	Channel string `json:"channel"`
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
	log.Println("clients", clients)

	// Сохраняем информацию о подключении в Redis
	err = redisClient.Set(fmt.Sprintf("user:%s:node", userID), "nodeID", 0).Err()
	if err != nil {
		log.Println("Failed to save to Redis:", err)
		return
	}

	defer func() {
		delete(clients, userID)
		conn.Close()
		redisClient.Del(fmt.Sprintf("user:%s:node", userID)) // Удаляем запись при отключении
	}()

	// // Проверяем неподтвержденные сообщения для пользователя
	// pendingMessages, _ := redisClient.LRange(fmt.Sprintf("pending_messages:%s", userID), 0, -1).Result()
	// for _, message := range pendingMessages {
	// 	err := conn.WriteMessage(websocket.TextMessage, []byte(message))
	// 	log.Printf("Sending message to user %s: %s", userID, message)
	// 	if err != nil {
	// 		log.Printf("Failed to send message to user %s: %v", userID, err)
	// 	}
	// }

	// // Чистим сообщения после отправки
	// redisClient.Del(fmt.Sprintf("pending_messages:%s", userID))

	// Проверяем неподтвержденные сообщения для пользователя
	pendingMessages, err := redisClient.LRange(fmt.Sprintf("pending_messages:%s", userID), 0, -1).Result()
	if err != nil {
		log.Printf("Failed to retrieve pending messages from Redis: %v", err)
		return
	}

	for _, message := range pendingMessages {
		err := conn.WriteMessage(websocket.TextMessage, []byte(message))
		if err != nil {
			log.Printf("Failed to send message to user %s: %v", userID, err)
		} else {
			log.Printf("Sending message to user %s: %s", userID, message)
		}
	}

	// Чистим сообщения после отправки
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

		if msg.Channel == "private-channel" {
			userID := fmt.Sprintf("%v", data["user_id"])
			//message := fmt.Sprintf("%v", data["message"])

			// Проверяем, подключен ли пользователь к этой ноде
			if conn, ok := clients[userID]; ok {
				// Отправляем приватное сообщение пользователю
				err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload)) // Отправляем JSON-формат
				if err != nil {
					log.Printf("Failed to send message to user %s: %v", userID, err)
				} else {
					log.Printf("Приватное сообщение отправлено пользователю %s", userID)
				}
			} else {
				log.Printf("User %s is not connected. Saving message to Redis.", userID)

				// Здесь вы можете сохранить сообщение в виде байтов
				err := redisClient.LPush(fmt.Sprintf("pending_messages:%s", userID), msg.Payload).Err()
				if err != nil {
					log.Printf("Failed to save message to Redis: %v", err)
				} else {
					log.Printf("Message saved to Redis for user %s", userID)
				}
				// // Пользователь не подключен, сохраняем сообщение в Redis
				// messageData := map[string]interface{}{
				// 	"user_id": userID,
				// 	"message": message,
				// }

				// log.Printf("Save md: ", messageData)

				// // Сериализация сообщения в JSON
				// messageJSON, err := json.Marshal(messageData)
				// if err != nil {
				// 	log.Printf("Failed to marshal message: %v", err)
				// 	continue
				// }

				// log.Printf("Save md json: ", messageJSON)

				// redisClient.LPush(fmt.Sprintf("pending_messages:%s", userID), messageJSON)
			}
		} else if msg.Channel == "public-channel" {
			// // Отправляем публичное сообщение всем подключённым клиентам
			for _, conn := range clients {
				err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload)) // Отправляем JSON-формат
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
		Addr: "localhost:6379",
	})

	// Запуск подписки на канал Redis в отдельной горутине
	go subscribeToRedis()

	// Запуск HTTP сервера для WebSocket
	http.HandleFunc("/ws", jwtMiddleware(handleWebSocket))
	log.Println("WS Server started!")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
