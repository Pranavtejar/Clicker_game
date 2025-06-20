package main

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Template struct {
	tmpl *template.Template
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.tmpl.ExecuteTemplate(w, name, data)
}

type Room struct {
	RoomName    string
	TimeCreated time.Time
}

type PageData struct {
	Rooms []Room
}

type RoomHub struct {
	Clients      map[string]map[*websocket.Conn]bool
	ClientsMux   sync.Mutex
	Rooms        []Room
	Timers       map[string]int
	TimersMux    sync.Mutex
	UserClicks   map[string]map[string]int
	TimerStarted map[string]bool
}

func NewRoomHub() *RoomHub {
	return &RoomHub{
		Clients:      make(map[string]map[*websocket.Conn]bool),
		Timers:       make(map[string]int),
		UserClicks:   make(map[string]map[string]int),
		TimerStarted: make(map[string]bool),
	}
}

var upgrader = websocket.Upgrader{}

func broadcast(hub *RoomHub, room string, message map[string]interface{}) {
	hub.ClientsMux.Lock()
	defer hub.ClientsMux.Unlock()

	for conn := range hub.Clients[room] {
		err := conn.WriteJSON(message)
		if err != nil {
			conn.Close()
			delete(hub.Clients[room], conn)
		}
	}
}

func broadcastUserCount(hub *RoomHub, room string) {
	userCount := len(hub.Clients[room])
	broadcast(hub, room, map[string]interface{}{
		"type": "user_count",
		"data": userCount,
	})
}

func startTimer(hub *RoomHub, room string) {
	hub.TimersMux.Lock()
	if hub.TimerStarted[room] {
		hub.TimersMux.Unlock()
		return
	}
	hub.TimerStarted[room] = true
	hub.Timers[room] = 30
	hub.TimersMux.Unlock()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			hub.TimersMux.Lock()
			timeLeft := hub.Timers[room]
			if timeLeft <= 0 {
				hub.TimersMux.Unlock()
				break
			}
			hub.Timers[room]--
			hub.TimersMux.Unlock()

			broadcast(hub, room, map[string]interface{}{
				"type": "timer",
				"data": timeLeft,
			})

			time.Sleep(1 * time.Second)
		}

		hub.ClientsMux.Lock()
		clicks := hub.UserClicks[room]
		maxClicks := 0
		winner := ""
		for user, count := range clicks {
			if count > maxClicks {
				maxClicks = count
				winner = user
			}
		}
		hub.ClientsMux.Unlock()

		broadcast(hub, room, map[string]interface{}{
			"type": "winner",
			"data": winner,
		})
	}()
}

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	hub := NewRoomHub()

	e.Renderer = &Template{
		tmpl: template.Must(template.ParseGlob("templates/*.html")),
	}

	e.GET("/", func(c echo.Context) error {
		return c.Render(http.StatusOK, "index", PageData{Rooms: hub.Rooms})
	})

	e.POST("/create", func(c echo.Context) error {
		name := c.FormValue("search")
		room := Room{RoomName: name, TimeCreated: time.Now()}
		hub.Rooms = append(hub.Rooms, room)
		return c.Render(http.StatusOK, "rooms", room)
	})

	e.POST("/join", func(c echo.Context) error {
		room := c.FormValue("room")
		username := c.FormValue("username")
		redirect := "/room/" + url.PathEscape(room) + "?username=" + url.QueryEscape(username)
		c.Response().Header().Set("HX-Redirect", redirect)
		return c.NoContent(http.StatusOK)
	})

	e.GET("/room/:room", func(c echo.Context) error {
		room := c.Param("room")
		username := c.QueryParam("username")
		return c.Render(http.StatusOK, "room", map[string]string{
			"Room":     room,
			"Username": username,
		})
	})

	e.GET("/ws/:room", func(c echo.Context) error {
		room := c.Param("room")
		username := c.QueryParam("username")

		conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			return err
		}
		defer conn.Close()

		hub.ClientsMux.Lock()
		if hub.Clients[room] == nil {
			hub.Clients[room] = make(map[*websocket.Conn]bool)
		}
		hub.Clients[room][conn] = true
		if hub.UserClicks[room] == nil {
			hub.UserClicks[room] = make(map[string]int)
		}
		broadcastUserCount(hub, room)
		hub.ClientsMux.Unlock()

		startTimer(hub, room)

		hub.TimersMux.Lock()
		current := hub.Timers[room]
		hub.TimersMux.Unlock()
		conn.WriteJSON(map[string]interface{}{
			"type": "timer",
			"data": current,
		})

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}

			var data map[string]interface{}
			if err := json.Unmarshal(msg, &data); err == nil {
				if data["type"] == "signal" {
					hub.ClientsMux.Lock()
					hub.UserClicks[room][username]++
					hub.ClientsMux.Unlock()
				}
			}
		}

		hub.ClientsMux.Lock()
		delete(hub.Clients[room], conn)
		broadcastUserCount(hub, room)
		hub.ClientsMux.Unlock()

		return nil
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	e.Logger.Fatal(e.Start(":" + port))
}

