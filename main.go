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
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/main", 302)
	}).Methods("GET")
	r.HandleFunc("/{topic}", app.GetHandler).Methods("GET")
	r.HandleFunc("/{topic}", app.PostHandler).Methods("POST")
	fs := http.FileServer(http.Dir("./static/"))
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", fs))
	port := os.Getenv("PORT")
	addr := ":" + port
	http.ListenAndServe(addr, r)
}

type Topic struct {
	chansMutex   *sync.RWMutex
	chans        map[chan []byte]struct{}
	historyMutex *sync.RWMutex
	history      []*Update
}

func NewTopic() *Topic {
	return &Topic{
		chansMutex:   &sync.RWMutex{},
		chans:        make(map[chan []byte]struct{}),
		historyMutex: &sync.RWMutex{},
		history:      make([]*Update, 0),
	}
}

func (t *Topic) append(update *Update) {
	t.historyMutex.Lock()
	defer t.historyMutex.Unlock()
	t.history = append(t.history, update)
	if len(t.history) > historyLimit {
		t.history = t.history[len(t.history)-historyLimit:]
	}
}

func (t *Topic) updateCount() {
	fmtstr := "<style>#nc::before{content:\"%d\"}</style>"
	data := []byte(fmt.Sprintf(fmtstr, len(t.chans)))
	t.chansMutex.RLock()
	defer t.chansMutex.RUnlock()
	for ch, _ := range t.chans {
		select {
		case ch <- data:
		default:
			continue
		}
	}
}

func (t *Topic) send(update *Update) {
	t.append(update)
	fmtstr := "<div class=\"new\"><p>%s</p><time>%s</time></div>"
	msg := fmt.Sprintf(fmtstr, update.message, update.timestamp)
	data := []byte(msg)
	t.chansMutex.RLock()
	defer t.chansMutex.RUnlock()
	for ch, _ := range t.chans {
		select {
		case ch <- data:
		default:
			continue
		}
	}
}

func (t *Topic) sendHistory(w http.ResponseWriter) error {
	fmtstr := "<div><p>%s</p><time>%s</time></div>"
	t.historyMutex.RLock()
	defer t.historyMutex.RUnlock()
	for _, update := range t.history {
		msg := fmt.Sprintf(fmtstr, update.message, update.timestamp)
		_, err := w.Write([]byte(msg))
		if err != nil {
			return err
		}
	}
	return nil
}

type Update struct {
	timestamp string
	message   string
}

type App struct {
	topicsMutex *sync.RWMutex
	topics      map[string]*Topic
}

func NewApp() *App {
	return &App{
		topicsMutex: &sync.RWMutex{},
		topics:      make(map[string]*Topic),
	}
}

func (app *App) addChan(topic string, ch chan []byte) *Topic {
	app.topicsMutex.Lock()
	defer app.topicsMutex.Unlock()
	t, ok := app.topics[topic]
	if ok {
		t.chansMutex.Lock()
		defer t.chansMutex.Unlock()
	} else {
		t = NewTopic()
		app.topics[topic] = t
	}
	t.chans[ch] = struct{}{}
	return t
}

func (app *App) removeChan(topic string, ch chan []byte) {
	app.topicsMutex.Lock()
	defer app.topicsMutex.Unlock()
	t, ok := app.topics[topic]
	if !ok {
		return
	}
	t.chansMutex.Lock()
	defer t.chansMutex.Unlock()
	delete(t.chans, ch)
	if len(t.chans) == 0 {
		delete(app.topics, topic)
	}
}

func (app *App) GetHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	topic := vars["topic"]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	// Create and register connection channel
	ch := make(chan []byte, bufferSize)
	t := app.addChan(topic, ch)
	defer func() {
		app.removeChan(topic, ch)
		t.updateCount()
	}()
	// Write page head and history
	w.Write([]byte(pageHead))
	err := t.sendHistory(w)
	if err != nil {
		return
	}
	flusher.Flush()
	t.updateCount()
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
	vars := mux.Vars(r)
	topic := vars["topic"]
	app.topicsMutex.RLock()
	defer app.topicsMutex.RUnlock()
	t, ok := app.topics[topic]
	if !ok {
		http.Redirect(w, r, "/"+topic, 302)
		return
	}
	r.ParseForm()
	msg := r.PostForm.Get("msg")
	if len(msg) > maxMsgLen {
		http.Redirect(w, r, "/"+topic, 302)
		return
	}
	msg = template.HTMLEscapeString(msg)
	msg = strings.TrimSpace(msg)
	if len(msg) > 0 {
		timestamp := time.Now().UTC().Format("2006-01-02 15:04:05")
		t.send(&Update{timestamp: timestamp, message: msg})
	}
	http.Redirect(w, r, "/"+topic, 302)
}
