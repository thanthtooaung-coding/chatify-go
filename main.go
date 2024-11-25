package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rabbitmq/amqp091-go"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clients   = make(map[*websocket.Conn]bool)
	clientsMu sync.Mutex
	rabbitMQ  *amqp091.Connection
	rabbitCh  *amqp091.Channel
)

var messageQueue []Message
var queueMu sync.Mutex

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

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

func initRabbitMQ() {
	var err error
	rabbitMQURL := os.Getenv("CHATIFY_RABBITMQ_URL")
	if rabbitMQURL == "" {
		rabbitMQURL = "amqp://guest:guest@localhost:5672/"
	}
	rabbitMQ, err = amqp091.Dial(rabbitMQURL)
	if err != nil {
		log.Printf("Failed to connect to RabbitMQ: %v", err)
		log.Println("Make sure RabbitMQ is running and accessible.")
		log.Println("If RabbitMQ is running on a different host or port, update the connection string.")
		log.Fatal("Exiting due to RabbitMQ connection failure.")
	}

	rabbitCh, err = rabbitMQ.Channel()
	if err != nil {
		log.Fatalf("Failed to open a channel: %v", err)
	}

	_, err = rabbitCh.QueueDeclare(
		"chat_messages", // name
		false,           // durable
		false,           // delete when unused
		false,           // exclusive
		false,           // no-wait
		nil,             // arguments
	)
	if err != nil {
		log.Fatalf("Failed to declare a queue: %v", err)
	}

	log.Println("Successfully connected to RabbitMQ and declared queue.")
}

func publishMessage(message []byte) {
	err := rabbitCh.Publish(
		"",              // exchange
		"chat_messages", // routing key
		false,           // mandatory
		false,           // immediate
		amqp091.Publishing{
			ContentType: "application/json",
			Body:        message,
		})
	failOnError(err, "Failed to publish a message")
}

func consumeMessages() {
	msgs, err := rabbitCh.Consume(
		"chat_messages", // queue
		"",              // consumer
		true,            // auto-ack
		false,           // exclusive
		false,           // no-local
		false,           // no-wait
		nil,             // args
	)
	failOnError(err, "Failed to register a consumer")

	go func() {
		for d := range msgs {

			var message Message
			err := json.Unmarshal(d.Body, &message)
			if err != nil {
				log.Printf("Error unmarshalling message: %v", err)
				continue
			}

			addMessageToQueue(message)

			broadcastMessage(d.Body)
		}
	}()
}

func broadcastMessage(message []byte) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	for client := range clients {
		err := client.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			fmt.Printf("Failed to write message to client: %v\n", err)
			client.Close()
			delete(clients, client)
		}
	}
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

	sendLastMessages(ws)

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

		publishMessage(msg)
	}
}

func addMessageToQueue(message Message) {
	queueMu.Lock()
	defer queueMu.Unlock()

	if len(messageQueue) >= 20 {
		messageQueue = messageQueue[1:]
	}

	messageQueue = append(messageQueue, message)
}

func sendLastMessages(ws *websocket.Conn) {
	queueMu.Lock()
	defer queueMu.Unlock()

	for _, message := range messageQueue {
		messageJSON, err := json.Marshal(message)
		if err != nil {
			fmt.Printf("Error marshaling message: %v\n", err)
			continue
		}

		err = ws.WriteMessage(websocket.TextMessage, messageJSON)
		if err != nil {
			fmt.Printf("Error sending message to client: %v\n", err)
			break
		}
	}
}

func cleanupOldMessages() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		currentTime := time.Now()
		queueMu.Lock()
		var updatedQueue []Message
		for _, message := range messageQueue {
			messageTime, err := time.Parse(time.RFC3339, message.Timestamp)
			if err != nil {
				fmt.Printf("Failed to parse timestamp: %v\n", err)
				continue
			}
			// Keep messages within the last 1 hour
			if currentTime.Sub(messageTime) <= time.Hour {
				updatedQueue = append(updatedQueue, message)
			}
		}
		messageQueue = updatedQueue
		queueMu.Unlock()
	}
}

func main() {
	initRabbitMQ()
	defer rabbitMQ.Close()
	defer rabbitCh.Close()

	go cleanupOldMessages()

	consumeMessages()

	http.HandleFunc("/ws", handleConnections)

	fmt.Println("Starting server on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}
