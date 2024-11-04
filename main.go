package main

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

var (
	ctx         = context.Background()
	redisClient *redis.Client
	upgrader    = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clients   = make(map[*websocket.Conn]bool) // Track connected clients
	broadcast = make(chan []redis.Z)           // Channel for leaderboard broadcasts
)

func main() {
	// Setup Redis
	redisClient = redis.NewClient(&redis.Options{
		Addr:     "redis-16183.c326.us-east-1-3.ec2.redns.redis-cloud.com:16183",
		Password: "viT5CvXId0jkM5GCRauoY7joWa3xUP4m",
	})

	// Initialize leaderboard
	if err := redisClient.ZAdd(ctx, "leaderboard", &redis.Z{Score: 0, Member: "default"}).Err(); err != nil {
		log.Fatalf("Failed to initialize leaderboard: %v", err)
	}

	// Setup Router
	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept"},
		AllowCredentials: true,
	}))

	r.GET("/leaderboard", getLeaderboard)
	r.POST("/leaderboard", updateScore)
	r.GET("/ws", handleWebSocket) // WebSocket endpoint

	// Start background goroutine to broadcast leaderboard updates
	go handleBroadcasts()

	r.Run(":8080")
}

func getLeaderboard(c *gin.Context) {
	scores, err := redisClient.ZRevRangeWithScores(ctx, "leaderboard", 0, 9).Result()
	if err != nil {
		log.Printf("Error fetching leaderboard: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, scores)
}

func updateScore(c *gin.Context) {
	var data struct {
		Username string `json:"username"`
		Score    int64  `json:"score"`
	}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Add user if not in leaderboard, then increment score
	if err := redisClient.ZAddNX(ctx, "leaderboard", &redis.Z{Score: 0, Member: data.Username}).Err(); err != nil {
		log.Printf("Error initializing user in leaderboard: %v", err)
	}

	newScore, err := redisClient.ZIncrBy(ctx, "leaderboard", float64(data.Score), data.Username).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Fetch updated leaderboard and broadcast to WebSocket clients
	updatedLeaderboard, _ := redisClient.ZRevRangeWithScores(ctx, "leaderboard", 0, 9).Result()
	broadcast <- updatedLeaderboard

	c.JSON(http.StatusOK, gin.H{"status": "score updated", "newScore": newScore})
}

func handleWebSocket(c *gin.Context) {
	// Upgrade HTTP request to WebSocket connection
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to set WebSocket upgrade: %v", err)
		return
	}
	defer conn.Close()
	clients[conn] = true

	// Keep connection open until client disconnects
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Client disconnected: %v", err)
			delete(clients, conn)
			break
		}
	}
}

func handleBroadcasts() {
	for {
		// Wait for a new leaderboard update
		leaderboard := <-broadcast
		// Send leaderboard to all connected WebSocket clients
		for client := range clients {
			err := client.WriteJSON(leaderboard)
			if err != nil {
				log.Printf("WebSocket error: %v", err)
				client.Close()
				delete(clients, client)
			}
		}
	}
}
