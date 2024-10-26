package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-redis/redis"
	"github.com/gorilla/websocket"
)

var jwtSecret = []byte("8cdb58bd22d362908bd6e5f58a4d7a476dc7e9a7dac618f17d370e4afb11a134")

// var redisPool *redis.Client
var redisPool *redis.Client

// upgrader - ДЛЯ ОТЛАДКИ
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var clients = make(map[string]*websocket.Conn)      // Карта userID -> соединение WebSocket
var userChannels = make(map[string]map[string]bool) // Карта userID -> map[channelName]bool

var nodeId = 1
var redisHost = "localhost:6379"
var userAuthURL = "http://127.0.0.1:8000/api/getUser"
var userAuthURLmethod = "GET"
var serverPort = ":8080"
var wsURL = "/ws"
var presenceURL = "/presence"
var useSSL = false // true для использования SSL

var deleteCacheMessages = false  // true чтобы удалять неотправленные сообщения через deleteCacheMessagesTime время
var deleteCacheMessagesTime = 24 // в часах

// Каналы - разделим публичные и приватные шаблоны каналов
var publicChannelPrefix = "public_"   // Префикс для публичных каналов, пример: public_room1
var privateChannelPrefix = "private_" // Префикс для приватных каналов, пример: private_user_{userID}

// Префикс для presence каналов
var presencePrefix = "presence_"

// Структура для присутствия (Presence) - сохраняет информацию о подключённых пользователях
type Presence struct {
	ChannelName string `json:"channel_name"` // Название канала
	UserID      string `json:"user_id"`      // ID пользователя
}

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

// addPresence добавляет пользователя в presence канал
func addPresence(channelName, userID string) error {
	presenceKey := fmt.Sprintf("%s%s", presencePrefix, channelName)
	return redisPool.SAdd(presenceKey, userID).Err()
}

// removePresence удаляет пользователя из presence канала
func removePresence(channelName, userID string) error {
	presenceKey := fmt.Sprintf("%s%s", presencePrefix, channelName)
	return redisPool.SRem(presenceKey, userID).Err()
}

// getPresence возвращает всех пользователей в конкретном канале
func getPresence(channelName string) ([]string, error) {
	presenceKey := fmt.Sprintf("%s%s", presencePrefix, channelName)
	return redisPool.SMembers(presenceKey).Result()
}

// AuthenticateWithSanctum проверяет токен и получает информацию о пользователе
func AuthenticateWithSanctum(token string) (*User, error) {
	client := &http.Client{}
	req, err := http.NewRequest(userAuthURLmethod, userAuthURL, nil) // Укажите правильный URL вашего API
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
		authHeader := r.URL.Query().Get("token")
		log.Println("authHeader", authHeader)

		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

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
		return
	}

	// Добавляем клиента в карту клиентов
	clients[userID] = conn

	defer func() {
		delete(clients, userID)
		conn.Close()

		// Удаляем пользователя из всех presence каналов при отключении
		if channels, exists := userChannels[userID]; exists {
			for channel := range channels {
				removePresence(channel, userID)
			}
			delete(userChannels, userID)
		}
	}()

	// Проверяем неподтвержденные сообщения для пользователя
	pendingMessages, err := redisPool.LRange(fmt.Sprintf("pending_messages:%s", userID), 0, -1).Result()
	if err != nil {
		log.Printf("Failed to retrieve pending messages from Redis: %v", err)
		return
	}

	for _, message := range pendingMessages {
		var notification Notification

		if err := json.Unmarshal([]byte(message), &notification); err != nil {
			log.Printf("Failed to unmarshal message: %v", err)
			return
		}

		messageJSON, err := json.Marshal(notification)
		if err != nil {
			log.Printf("Failed to marshal notification: %v", err)
			return
		}

		err = conn.WriteMessage(websocket.TextMessage, messageJSON)
		if err != nil {
			log.Printf("Failed to send message to user %s: %v", userID, err)
		} else {
			log.Printf("Sending message to user %s: %s", userID, message)
		}
	}

	redisPool.Del(fmt.Sprintf("pending_messages:%s", userID))

	// Чтение сообщений от клиента
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Println("Error reading message:", err)
			break
		}

		var request map[string]interface{}
		if err := json.Unmarshal(msg, &request); err != nil {
			log.Println("Error unmarshaling message:", err)
			continue
		}

		switch request["type"] {
		case "subscribe":
			channels, ok := request["channels"].([]interface{})
			if !ok {
				log.Println("Invalid channels format. Expected an array.")
				continue
			}

			// Очищаем предыдущие подписки пользователя
			if _, exists := userChannels[userID]; exists {
				for channel := range userChannels[userID] {
					removePresence(channel, userID)
				}
				delete(userChannels, userID)
			}

			// Добавляем новые подписки
			userChannels[userID] = make(map[string]bool)
			for _, channel := range channels {
				channelName, ok := channel.(string)
				if !ok {
					log.Println("Invalid channel name format. Expected a string.")
					continue
				}

				userChannels[userID][channelName] = true
				addPresence(channelName, userID)
				log.Printf("User %s subscribed to channel %s", userID, channelName)
			}

		default:
			log.Println("Unknown message type:", request["type"])
		}
	}
}

// Функция подписки на Redis каналы
func subscribeToRedis() {
	pubsub := redisPool.PSubscribe(fmt.Sprintf("%s*", publicChannelPrefix), fmt.Sprintf("%s*", privateChannelPrefix))
	log.Printf("Подписались на каналы %s* и %s*", publicChannelPrefix, privateChannelPrefix)
	ch := pubsub.Channel()

	for msg := range ch {
		var notification Notification
		if err := json.Unmarshal([]byte(msg.Payload), &notification); err != nil {
			log.Printf("Failed to unmarshal message: %v", err)
			continue
		}

		messageJSON, err := json.Marshal(notification)
		if err != nil {
			log.Printf("Failed to marshal notification: %v", err)
			continue
		}

		// Обработка публичных сообщений
		if strings.HasPrefix(msg.Channel, publicChannelPrefix) {
			// Отправляем публичное сообщение всем подключенным клиентам
			for userID, channels := range userChannels {
				if _, subscribed := channels[msg.Channel]; subscribed {
					if conn, ok := clients[userID]; ok {
						err = conn.WriteMessage(websocket.TextMessage, messageJSON)
						if err != nil {
							log.Printf("Failed to send message to user %s: %v", userID, err)
						} else {
							log.Printf("Message sent to user %s on channel %s", userID, msg.Channel)
						}
					}
				}
			}
			// Обработка приватных сообщений
		} else if strings.HasPrefix(msg.Channel, privateChannelPrefix) {
			// Извлекаем ID пользователя из имени канала
			parts := strings.Split(msg.Channel, "_")
			if len(parts) > 2 { // Проверяем, что частей достаточно
				userID := parts[2] // ID пользователя - третья часть имени канала

				// Отправляем сообщение, если пользователь онлайн
				if conn, ok := clients[userID]; ok {
					err = conn.WriteMessage(websocket.TextMessage, messageJSON)
					if err != nil {
						log.Printf("Failed to send message to user %s: %v", userID, err)
					} else {
						log.Printf("Message sent to user %s on channel %s", userID, msg.Channel)
					}
				} else {
					// Сохраняем сообщение в Redis, если пользователь оффлайн
					log.Printf("User %s is offline. Saving message to Redis.", userID)
					err := redisPool.LPush(fmt.Sprintf("pending_messages:%s", userID), messageJSON).Err()
					if err != nil {
						log.Printf("Failed to save message to Redis: %v", err)
					} else {
						log.Printf("Message saved to Redis for user %s", userID)
					}

					// Устанавливаем время жизни для списка непрочитанных сообщений, если включена функция `deleteCacheMessages`
					if deleteCacheMessages {
						expirationTime := time.Duration(deleteCacheMessagesTime) * time.Hour
						err = redisPool.Expire(fmt.Sprintf("pending_messages:%s", userID), expirationTime).Err()
						if err != nil {
							log.Println("Failed to set expiration for pending messages:", err)
						}
					}
				}
			}
		}
	}
}

func getChannelPresenceHandler(w http.ResponseWriter, r *http.Request) {
	channelName := r.URL.Query().Get("channel")
	if channelName == "" {
		http.Error(w, "Channel name is required", http.StatusBadRequest)
		return
	}

	users, err := getPresence(channelName)
	if err != nil {
		http.Error(w, "Failed to get presence", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(users)
}

func main() {
	// Инициализация клиента Redis
	// redisPool = redis.NewClient(&redis.Options{
	// 	Addr: redisHost,
	// })

	// Создание пула соединений Redis
	redisPool = redis.NewClient(&redis.Options{
		Addr:     redisHost,
		Password: "", // Если используется пароль
		DB:       0,  // Номер базы данных
		PoolSize: 10, // Размер пула (количество соединений)
	})

	// Проверка соединения с Redis
	_, err := redisPool.Ping().Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Запуск подписки на канал Redis в отдельной горутине
	go subscribeToRedis()

	// Запуск HTTP сервера для WebSocket
	http.HandleFunc(wsURL, jwtMiddleware(handleWebSocket))
	http.HandleFunc(presenceURL, getChannelPresenceHandler)
	log.Println("WS Server started on port:", serverPort)
	log.Fatal(http.ListenAndServe(serverPort, nil))

	if useSSL {
		// Запуск HTTPS сервера
		log.Fatal(http.ListenAndServeTLS(serverPort, "server.crt", "server.key", http.HandlerFunc(jwtMiddleware(handleWebSocket))))
	} else {
		// Запуск обычного HTTP сервера
		log.Fatal(http.ListenAndServe(serverPort, http.HandlerFunc(jwtMiddleware(handleWebSocket))))
	}
}
