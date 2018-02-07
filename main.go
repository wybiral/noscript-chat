package main

import (
	"fmt"
	"github.com/gorilla/mux"
	"html/template"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Number of updates to keep in history
const historyLimit = 20

// Interval to send single-space ping to keep conntection alive
const pingRate = 1 * time.Second

// Maximum message length
const maxMsgLen = 1024

// Number of buffered messages per connection
const bufferSize = 5

// Leading portion of main page
const pageHead = `<!doctype html>
<html>
<head>
<title>noscript timeline</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" type="text/css" href="/static/style.css">
</head>
<body>
<header>
	<div id="count">Being seen by <span id="nc"></span> connection(s)</div>
	<form method="POST">
		<textarea name="msg" placeholder="Start typing..." autofocus></textarea>
		<div><button>Post</button></div>
	</form>
</header>
<main>
`

func main() {
	app := NewApp()
	r := mux.NewRouter()
	r.HandleFunc("/", app.GetHandler).Methods("GET")
	r.HandleFunc("/", app.PostHandler).Methods("POST")
	fs := http.FileServer(http.Dir("./static/"))
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", fs))
	port := os.Getenv("PORT")
	addr := ":" + port
	http.ListenAndServe(addr, r)
}

type Update struct {
	timestamp string
	message   string
}

type App struct {
	chansMutex   *sync.RWMutex
	chans        map[chan []byte]struct{}
	historyMutex *sync.RWMutex
	history      []*Update
}

func NewApp() *App {
	return &App{
		chansMutex:   &sync.RWMutex{},
		chans:        make(map[chan []byte]struct{}),
		historyMutex: &sync.RWMutex{},
		history:      make([]*Update, 0),
	}
}

// Add channel to listening connections
func (app *App) addChan(ch chan []byte) {
	app.chansMutex.Lock()
	defer app.chansMutex.Unlock()
	app.chans[ch] = struct{}{}
}

// Remove channel from listening connections
func (app *App) removeChan(ch chan []byte) {
	app.chansMutex.Lock()
	defer app.chansMutex.Unlock()
	delete(app.chans, ch)
}

// Append a single update to history
func (app *App) append(update *Update) {
	app.historyMutex.Lock()
	defer app.historyMutex.Unlock()
	app.history = append(app.history, update)
	if len(app.history) > historyLimit {
		app.history = app.history[len(app.history)-historyLimit:]
	}
}

// Update the connection count for all connections
func (app *App) updateCount() {
	fmtstr := "<style>#nc::before{content:\"%d\"}</style>"
	data := []byte(fmt.Sprintf(fmtstr, len(app.chans)))
	app.chansMutex.RLock()
	defer app.chansMutex.RUnlock()
	for ch, _ := range app.chans {
		select {
		case ch <- data:
		default:
			continue
		}
	}
}

// Send a single update to all connections
func (app *App) send(update *Update) {
	app.append(update)
	fmtstr := "<div class=\"new\"><p>%s</p><time>%s</time></div>"
	msg := fmt.Sprintf(fmtstr, update.message, update.timestamp)
	data := []byte(msg)
	app.chansMutex.RLock()
	defer app.chansMutex.RUnlock()
	for ch, _ := range app.chans {
		select {
		case ch <- data:
		default:
			continue
		}
	}
}

// Send chat history to ResponseWriter
func (app *App) sendHistory(w http.ResponseWriter) error {
	fmtstr := "<div><p>%s</p><time>%s</time></div>"
	app.historyMutex.RLock()
	defer app.historyMutex.RUnlock()
	for _, update := range app.history {
		msg := fmt.Sprintf(fmtstr, update.message, update.timestamp)
		_, err := w.Write([]byte(msg))
		if err != nil {
			return err
		}
	}
	return nil
}

func (app *App) GetHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	// Write page head and history
	w.Write([]byte(pageHead))
	err := app.sendHistory(w)
	if err != nil {
		return
	}
	flusher.Flush()
	// Create and register connection channel
	ch := make(chan []byte, bufferSize)
	app.addChan(ch)
	app.updateCount()
	defer func() {
		app.removeChan(ch)
		app.updateCount()
	}()
	for {
		select {
		case msg := <-ch:
			_, err = w.Write(msg)
			if err != nil {
				return
			}
		case <-time.After(pingRate):
			_, err := w.Write([]byte{' '})
			if err != nil {
				return
			}
		}
		flusher.Flush()
	}
}

func (app *App) PostHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	msg := r.PostForm.Get("msg")
	if len(msg) > maxMsgLen {
		http.Redirect(w, r, "/", 302)
		return
	}
	msg = template.HTMLEscapeString(msg)
	msg = strings.TrimSpace(msg)
	if len(msg) > 0 {
		timestamp := time.Now().UTC().Format("2006-01-02 15:04:05")
		app.send(&Update{timestamp: timestamp, message: msg})
	}
	http.Redirect(w, r, "/", 302)
}
