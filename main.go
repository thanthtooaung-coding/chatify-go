package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clients = make(map[*websocket.Conn]bool)
)

type Message struct {
	UserID    string `json:"userID"`
	Username  string `json:"username"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("Failed to upgrade to websocket: %v\n", err)
		return
	}
	defer ws.Close()

	clients[ws] = true
	fmt.Println("Client connected")

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			fmt.Println("Client disconnected")
			delete(clients, ws)
			break
		}

		var message Message
		err = json.Unmarshal(msg, &message)
		if err != nil {
			fmt.Printf("Failed to unmarshal message: %v\n", err)
			continue
		}

		fmt.Printf("Received message from user %s (%s) at %s: %s\n", message.Username, message.UserID, message.Timestamp, message.Content)

		for client := range clients {
			err := client.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				fmt.Printf("Failed to write message to client: %v\n", err)
				client.Close()
				delete(clients, client)
			}
		}
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
