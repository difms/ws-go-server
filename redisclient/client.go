package redisclient

import (
	"log"

	"github.com/go-redis/redis"
)

var Client *redis.Client

// Initialize Redis client
func Initialize() {
	Client = redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	_, err := Client.Ping().Result()
	if err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}
}

// SaveUserConnection stores user connection status in Redis
func SaveUserConnection(userID string) {
	Client.Set("user:"+userID+":node", "nodeID", 0)
}

// RemoveUserConnection deletes user connection status from Redis
func RemoveUserConnection(userID string) {
	Client.Del("user:" + userID + ":node")
}
