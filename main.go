package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clients   = make(map[*websocket.Conn]bool)
	clientsMu sync.Mutex
)

type Message struct {
	UserID    string `json:"userID"`
	Username  string `json:"username"`
	Content   string `json:"content"`
	Image     string `json:"image"`
	Timestamp string `json:"timestamp"`
}

type ActiveUsersMessage struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

func broadcastActiveUsers() {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	count := len(clients)
	message := ActiveUsersMessage{
		Type:  "activeUsers",
		Count: count,
	}

	messageJSON, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Failed to marshal active users message: %v\n", err)
		return
	}

	for client := range clients {
		err := client.WriteMessage(websocket.TextMessage, messageJSON)
		if err != nil {
			fmt.Printf("Failed to write active users message to client: %v\n", err)
			client.Close()
			delete(clients, client)
		}
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("Failed to upgrade to websocket: %v\n", err)
		return
	}
	defer func() {
		clientsMu.Lock()
		delete(clients, ws)
		clientsMu.Unlock()
		broadcastActiveUsers()
		ws.Close()
	}()

	clientsMu.Lock()
	clients[ws] = true
	clientsMu.Unlock()

	fmt.Println("Client connected")
	broadcastActiveUsers()

	ws.SetReadLimit(512)
	ws.SetCloseHandler(func(code int, text string) error {
		fmt.Println("Client disconnected with code:", code)
		clientsMu.Lock()
		delete(clients, ws)
		clientsMu.Unlock()
		broadcastActiveUsers()
		return nil
	})

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			fmt.Println("Error reading message or client disconnected:", err)
			break
		}

		var message Message
		err = json.Unmarshal(msg, &message)
		if err != nil {
			fmt.Printf("Failed to unmarshal message: %v\n", err)
			continue
		}

		fmt.Printf("Received message from user %s (%s): %s\n", message.Username, message.UserID, message.Content)
		if message.Image != "" {
			fmt.Println("Message contains an image")
		}

		clientsMu.Lock()
		for client := range clients {
			err := client.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				fmt.Printf("Failed to write message to client: %v\n", err)
				client.Close()
				delete(clients, client)
			}
		}
		clientsMu.Unlock()
	}
}

func main() {
	http.HandleFunc("/ws", handleConnections)

	fmt.Println("Starting server on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}
