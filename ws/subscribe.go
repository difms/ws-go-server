package ws

import (
	"encoding/json"
	"fmt"
	"ws-go-server/redisclient"
)

// SubscribeToRedis listens for messages on Redis channels
func SubscribeToRedis() {
	pubsub := redisclient.Client.Subscribe("private-channel", "public-channel")
	ch := pubsub.Channel()

	for msg := range ch { // msg is of type redisclient.Message
		handleMessage(msg)
	}
}

// handleMessage processes and distributes messages from Redis
func handleMessage(msg *redisclient.Message) {
	var data map[string]interface{}
	json.Unmarshal([]byte(msg.Payload), &data)

	if msg.Channel == "private-channel" {
		userID := fmt.Sprintf("%v", data["user_id"])
		redisclient.SendPrivateMessage(userID, msg.Payload)
	} else if msg.Channel == "public-channel" {
		redisclient.BroadcastMessage(msg.Payload)
	}
}
