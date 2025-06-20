package main

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
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

var (
	rooms      []Room
	clients    = make(map[string]map[*websocket.Conn]bool)
	clicks     = make(map[string]map[string]int)
	timers     = make(map[string]int)
	timerStart = make(map[string]bool)
	upgrader   = websocket.Upgrader{}
	clientsMux sync.Mutex
	timersMux  sync.Mutex
)

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	e.Renderer = &Template{
		tmpl: template.Must(template.ParseGlob("templates/*.html")),
	}

	e.GET("/", func(c echo.Context) error {
		return c.Render(http.StatusOK, "index", PageData{Rooms: rooms})
	})

	e.POST("/create", func(c echo.Context) error {
		name := c.FormValue("search")
		room := Room{RoomName: name, TimeCreated: time.Now()}
		rooms = append(rooms, room)
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

		clientsMux.Lock()
		if clients[room] == nil {
			clients[room] = make(map[*websocket.Conn]bool)
		}
		clients[room][conn] = true
		if clicks[room] == nil {
			clicks[room] = make(map[string]int)
		}
		broadcastUserCount(room)
		clientsMux.Unlock()

		startTimer(room)

		timersMux.Lock()
		current := timers[room]
		timersMux.Unlock()
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
			if err := json.Unmarshal(msg, &data); err == nil && data["type"] == "signal" {
				clientsMux.Lock()
				clicks[room][username]++
				clientsMux.Unlock()
			}
		}

		clientsMux.Lock()
		delete(clients[room], conn)
		broadcastUserCount(room)
		clientsMux.Unlock()

		return nil
	})

	e.Logger.Fatal(e.Start(":8080"))
}

func broadcastUserCount(room string) {
	userCount := len(clients[room])
	for conn := range clients[room] {
		err := conn.WriteJSON(map[string]interface{}{
			"type": "user_count",
			"data": userCount,
		})
		if err != nil {
			conn.Close()
			delete(clients[room], conn)
		}
	}
}

func startTimer(room string) {
	timersMux.Lock()
	if timerStart[room] {
		timersMux.Unlock()
		return
	}
	timerStart[room] = true
	timers[room] = 30
	timersMux.Unlock()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			timersMux.Lock()
			timeLeft := timers[room]
			if timeLeft <= 0 {
				timersMux.Unlock()
				break
			}
			timers[room]--
			timersMux.Unlock()

			clientsMux.Lock()
			for conn := range clients[room] {
				err := conn.WriteJSON(map[string]interface{}{
					"type": "timer",
					"data": timeLeft,
				})
				if err != nil {
					conn.Close()
					delete(clients[room], conn)
				}
			}
			clientsMux.Unlock()

			time.Sleep(1 * time.Second)
		}

		clientsMux.Lock()
		roomClicks := clicks[room]
		max := 0
		winner := ""
		for name, count := range roomClicks {
			if count > max {
				max = count
				winner = name
			}
		}
		for conn := range clients[room] {
			conn.WriteJSON(map[string]interface{}{
				"type": "winner",
				"data": winner,
			})
		}
		clientsMux.Unlock()
	}()
}
