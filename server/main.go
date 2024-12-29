// main.go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Game Constants
const (
	CANVAS_WIDTH       = 800
	CANVAS_HEIGHT      = 600
	PADDLE_WIDTH       = 10
	PADDLE_HEIGHT      = 60
	BALL_RADIUS        = 5
	PADDLE_SPEED       = 5.0
	INITIAL_BALL_SPEED = 5.0
)

// Command represents player input
type Command int

const (
	Stay Command = iota
	Up
	Down
)

// Vector2D represents a 2D vector
type Vector2D struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Player represents a single player in the game
type Player struct {
	ID        string    `json:"id"`
	TeamID    int       `json:"teamId"`
	Position  float64   `json:"position"`
	Command   Command   `json:"command"`
	LastInput time.Time `json:"lastInput"`
}

// Ball represents the game ball
type Ball struct {
	Position Vector2D `json:"position"`
	Velocity Vector2D `json:"velocity"`
}

// Team represents a collection of players on the same side
type Team struct {
	ID      int                `json:"id"`
	Players map[string]*Player `json:"players"`
	PaddleY float64            `json:"paddleY"`
	mu      sync.RWMutex
}

// GameState represents the current state of the game
type GameState struct {
	Ball      *Ball `json:"ball"`
	Team1     *Team `json:"team1"`
	Team2     *Team `json:"team2"`
	Score1    int   `json:"score1"`
	Score2    int   `json:"score2"`
	IsRunning bool  `json:"isRunning"`
}

// Game represents a single ping pong game instance
type Game struct {
	ID        string     `json:"id"`
	State     *GameState `json:"state"`
	mu        sync.RWMutex
	IsRunning bool
}

// GameManager handles multiple concurrent games
type GameManager struct {
	Games map[string]*Game
	mu    sync.RWMutex
}

// WebSocket-related types
type Client struct {
	conn     *websocket.Conn
	playerId string
	teamId   int
	gameId   string
}

type Message struct {
	Type     string     `json:"type"`
	PlayerId string     `json:"playerId,omitempty"`
	TeamId   int        `json:"teamId,omitempty"`
	Command  string     `json:"command,omitempty"`
	State    *GameState `json:"state,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type WSGameServer struct {
	clients     map[*Client]bool
	register    chan *Client
	unregister  chan *Client
	broadcast   chan []byte
	gameManager *GameManager
	mu          sync.RWMutex
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // In production, implement proper origin checking
	},
}

// NewGameManager creates a new game manager
func NewGameManager() *GameManager {
	return &GameManager{
		Games: make(map[string]*Game),
	}
}

// NewGame creates a new game instance
func NewGame(id string) *Game {
	return &Game{
		ID: id,
		State: &GameState{
			Ball: &Ball{
				Position: Vector2D{X: float64(CANVAS_WIDTH) / 2, Y: float64(CANVAS_HEIGHT) / 2},
				Velocity: Vector2D{X: INITIAL_BALL_SPEED, Y: 0},
			},
			Team1: &Team{
				ID:      1,
				Players: make(map[string]*Player),
				PaddleY: float64(CANVAS_HEIGHT) / 2,
			},
			Team2: &Team{
				ID:      2,
				Players: make(map[string]*Player),
				PaddleY: float64(CANVAS_HEIGHT) / 2,
			},
			Score1:    0,
			Score2:    0,
			IsRunning: true,
		},
		IsRunning: true,
	}
}

// ProcessTeamInput calculates the most common command from team players
func (t *Team) ProcessTeamInput() Command {
	t.mu.RLock()
	defer t.mu.RUnlock()

	commands := make(map[Command]int)
	for _, player := range t.Players {
		commands[player.Command]++
	}

	maxCount := 0
	finalCommand := Stay

	for cmd, count := range commands {
		if count > maxCount {
			maxCount = count
			finalCommand = cmd
		}
	}

	return finalCommand
}

// UpdatePaddlePosition updates the paddle position based on team input
func (t *Team) UpdatePaddlePosition(cmd Command) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch cmd {
	case Up:
		t.PaddleY -= PADDLE_SPEED
	case Down:
		t.PaddleY += PADDLE_SPEED
	}

	// Ensure paddle stays within screen bounds
	if t.PaddleY < 0 {
		t.PaddleY = 0
	}
	if t.PaddleY > float64(CANVAS_HEIGHT-PADDLE_HEIGHT) {
		t.PaddleY = float64(CANVAS_HEIGHT - PADDLE_HEIGHT)
	}
}

// UpdatePlayerInput updates a player's input command
func (g *Game) UpdatePlayerInput(playerID string, teamID int, cmd Command) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var team *Team
	if teamID == 1 {
		team = g.State.Team1
	} else {
		team = g.State.Team2
	}

	team.mu.Lock()
	if player, exists := team.Players[playerID]; exists {
		player.Command = cmd
		player.LastInput = time.Now()
	}
	team.mu.Unlock()
}

// UpdateGameState updates the game state every 100ms
func (g *Game) UpdateGameState() {
	g.mu.Lock()
	defer g.mu.Unlock()

	ball := g.State.Ball

	// Update ball position
	ball.Position.X += ball.Velocity.X
	ball.Position.Y += ball.Velocity.Y

	// Ball collision with top and bottom walls
	if ball.Position.Y <= float64(BALL_RADIUS) ||
		ball.Position.Y >= float64(CANVAS_HEIGHT-BALL_RADIUS) {
		ball.Velocity.Y = -ball.Velocity.Y
	}

	// Ball collision with paddles
	// Left paddle (Team 1)
	if ball.Position.X <= float64(PADDLE_WIDTH+BALL_RADIUS) {
		if ball.Position.Y >= g.State.Team1.PaddleY &&
			ball.Position.Y <= g.State.Team1.PaddleY+float64(PADDLE_HEIGHT) {
			ball.Velocity.X = -ball.Velocity.X
			// Add some Y velocity based on where the ball hits the paddle
			relativeIntersectY := (g.State.Team1.PaddleY + float64(PADDLE_HEIGHT)/2) - ball.Position.Y
			normalizedIntersectY := relativeIntersectY / (float64(PADDLE_HEIGHT) / 2)
			ball.Velocity.Y = -normalizedIntersectY * INITIAL_BALL_SPEED
		}
	}

	// Right paddle (Team 2)
	if ball.Position.X >= float64(CANVAS_WIDTH-PADDLE_WIDTH-BALL_RADIUS) {
		if ball.Position.Y >= g.State.Team2.PaddleY &&
			ball.Position.Y <= g.State.Team2.PaddleY+float64(PADDLE_HEIGHT) {
			ball.Velocity.X = -ball.Velocity.X
			// Add some Y velocity based on where the ball hits the paddle
			relativeIntersectY := (g.State.Team2.PaddleY + float64(PADDLE_HEIGHT)/2) - ball.Position.Y
			normalizedIntersectY := relativeIntersectY / (float64(PADDLE_HEIGHT) / 2)
			ball.Velocity.Y = -normalizedIntersectY * INITIAL_BALL_SPEED
		}
	}

	// Score points
	if ball.Position.X < 0 {
		g.State.Score2++
		g.ResetBall()
	} else if ball.Position.X > float64(CANVAS_WIDTH) {
		g.State.Score1++
		g.ResetBall()
	}
}

// ResetBall resets the ball to the center of the screen
func (g *Game) ResetBall() {
	g.State.Ball.Position = Vector2D{
		X: float64(CANVAS_WIDTH) / 2,
		Y: float64(CANVAS_HEIGHT) / 2,
	}

	// Serve towards the player who just lost a point
	if g.State.Score1 > g.State.Score2 {
		g.State.Ball.Velocity = Vector2D{X: -INITIAL_BALL_SPEED, Y: 0}
	} else {
		g.State.Ball.Velocity = Vector2D{X: INITIAL_BALL_SPEED, Y: 0}
	}
}

// StartGameLoop starts the main game loop
func (g *Game) StartGameLoop() {
	g.IsRunning = true

	// Input processing ticker (250ms)
	inputTicker := time.NewTicker(250 * time.Millisecond)
	// Physics update ticker (100ms)
	physicsTicker := time.NewTicker(100 * time.Millisecond)

	go func() {
		for g.IsRunning {
			select {
			case <-inputTicker.C:
				// Process team inputs
				team1Cmd := g.State.Team1.ProcessTeamInput()
				team2Cmd := g.State.Team2.ProcessTeamInput()

				// Update paddle positions
				g.State.Team1.UpdatePaddlePosition(team1Cmd)
				g.State.Team2.UpdatePaddlePosition(team2Cmd)

			case <-physicsTicker.C:
				// Update game physics
				g.UpdateGameState()
			}
		}

		inputTicker.Stop()
		physicsTicker.Stop()
	}()
}

// NewWSGameServer creates a new WebSocket game server
func NewWSGameServer(gameManager *GameManager) *WSGameServer {
	return &WSGameServer{
		clients:     make(map[*Client]bool),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		broadcast:   make(chan []byte),
		gameManager: gameManager,
	}
}

// Run starts the WebSocket server main loop
func (s *WSGameServer) Run() {
	for {
		select {
		case client := <-s.register:
			s.mu.Lock()
			s.clients[client] = true
			s.mu.Unlock()

			// Assign player to a team and game
			game := s.findOrCreateGame()
			client.gameId = game.ID

			// Determine team (assign to team with fewer players)
			if len(game.State.Team1.Players) <= len(game.State.Team2.Players) {
				client.teamId = 1
				game.State.Team1.Players[client.playerId] = &Player{
					ID:     client.playerId,
					TeamID: 1,
				}
			} else {
				client.teamId = 2
				game.State.Team2.Players[client.playerId] = &Player{
					ID:     client.playerId,
					TeamID: 2,
				}
			}

			// Send team assignment to client
			assignMsg, _ := json.Marshal(Message{
				Type:   "playerAssigned",
				TeamId: client.teamId,
			})
			client.conn.WriteMessage(websocket.TextMessage, assignMsg)

		case client := <-s.unregister:
			s.mu.Lock()
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				client.conn.Close()
			}
			s.mu.Unlock()

		case message := <-s.broadcast:
			s.mu.RLock()
			for client := range s.clients {
				client.conn.WriteMessage(websocket.TextMessage, message)
			}
			s.mu.RUnlock()
		}
	}
}

// findOrCreateGame finds an available game or creates a new one
func (s *WSGameServer) findOrCreateGame() *Game {
	s.gameManager.mu.Lock()
	defer s.gameManager.mu.Unlock()

	// Find a game that's not full
	for _, game := range s.gameManager.Games {
		if len(game.State.Team1.Players)+len(game.State.Team2.Players) < 4 {
			return game
		}
	}

	// Create new game if none available
	newGame := NewGame(fmt.Sprintf("game-%d", time.Now().UnixNano()))
	s.gameManager.Games[newGame.ID] = newGame
	newGame.StartGameLoop()

	// Start broadcasting game state
	go s.broadcastGameState(newGame)

	return newGame
}

// broadcastGameState broadcasts the game state to all clients
func (s *WSGameServer) broadcastGameState(game *Game) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		if !game.IsRunning {
			return
		}

		game.mu.RLock()
		stateMsg, _ := json.Marshal(Message{
			Type:  "gameState",
			State: game.State,
		})
		game.mu.RUnlock()

		s.broadcast <- stateMsg
	}
}

// HandleConnection handles new WebSocket connections
func (s *WSGameServer) HandleConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	client := &Client{
		conn: conn,
	}

	s.register <- client

	// Handle incoming messages
	go func() {
		defer func() {
			s.unregister <- client
		}()

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("error: %v", err)
				}
				break
			}

			var msg Message
			if err := json.Unmarshal(message, &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "register":
				client.playerId = msg.PlayerId

			case "input":
				if game, exists := s.gameManager.Games[client.gameId]; exists {
					var cmd Command

					switch msg.Command {
					case "up":
						cmd = Up
					case "down":
						cmd = Down
					default:
						cmd = Stay
					}
					game.UpdatePlayerInput(client.playerId, client.teamId, cmd)
				}
			}
		}
	}()
}

// RemovePlayer removes a player from their game
func (s *WSGameServer) RemovePlayer(client *Client) {
	if game, exists := s.gameManager.Games[client.gameId]; exists {
		game.mu.Lock()
		if client.teamId == 1 {
			delete(game.State.Team1.Players, client.playerId)
		} else {
			delete(game.State.Team2.Players, client.playerId)
		}

		// End game if no players left
		if len(game.State.Team1.Players) == 0 && len(game.State.Team2.Players) == 0 {
			game.IsRunning = false
			delete(s.gameManager.Games, game.ID)
		}
		game.mu.Unlock()
	}
}

// Cleanup performs cleanup when a client disconnects
func (s *WSGameServer) Cleanup(client *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.clients[client]; ok {
		s.RemovePlayer(client)
		delete(s.clients, client)
		client.conn.Close()
	}
}

// StartHTTPServer starts the HTTP server and configures routes
func StartHTTPServer(wsServer *WSGameServer) {
	// Configure CORS middleware
	corsMiddleware := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next(w, r)
		}
	}

	// Configure routes
	http.HandleFunc("/game", corsMiddleware(wsServer.HandleConnection))

	// Add health check endpoint
	http.HandleFunc("/health", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Add status endpoint
	http.HandleFunc("/status", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		wsServer.mu.RLock()
		activeGames := len(wsServer.gameManager.Games)
		activePlayers := len(wsServer.clients)
		wsServer.mu.RUnlock()

		status := struct {
			ActiveGames   int `json:"activeGames"`
			ActivePlayers int `json:"activePlayers"`
		}{
			ActiveGames:   activeGames,
			ActivePlayers: activePlayers,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}))
}

func main() {
	// Initialize the game manager
	gameManager := NewGameManager()

	// Create and start the WebSocket server
	wsServer := NewWSGameServer(gameManager)
	go wsServer.Run()

	// Configure logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting server on :8080")

	// Start HTTP server
	StartHTTPServer(wsServer)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
