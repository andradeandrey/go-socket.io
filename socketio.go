/*
	The socketio package is a simple abstraction layer for different web browser-
	supported transport mechanisms. It is meant to be fully compatible with the
	Socket.IO client side JavaScript socket API library by LearnBoost Labs
	(http://socket.io/), but through custom formatters it might fit other client
	implementations too.

	It (together with the LearnBoost's client-side libraries) provides an easy way for
	developers to access the most popular browser transport mechanism today:
	multipart- and long-polling XMLHttpRequests, HTML5 WebSockets and
	forever-frames [TODO]. The socketio package works hand-in-hand with the standard
	http package by plugging itself into a configurable ServeMux. It has an callback-style
	API for handling connection events. The callbacks are:

		- SocketIO.OnConnect
		- SocketIO.OnDisconnect
		- SocketIO.OnMessage

	Other utility-methods include:

		- SocketIO.Mux
		- SocketIO.Broadcast
		- SocketIO.BroadcastExcept
		- SocketIO.GetConn
		- Conn.Send

	Each new connection will be automatically assigned an unique session id and
	using those the clients can reconnect without losing messages: the server
	persists clients' pending messages (until some configurable point) if they can't
	be immediately delivered. All writes through `Conn.Send` by design asynchronous.

	Finally, the actual format on the wire is described by a separate `Codec`.
	The default codec is compatible with the LearnBoost's Socket.IO client.

	For example, here is a simple chat server:

		package main

		import (
			"http"
			"log"
			"socketio"
		)

		func main() {
			sio := socketio.NewSocketIO(nil, nil)
			sio.Mux("/socket.io/", nil)

			http.Handle("/", http.FileServer("www/", "/"))

			sio.OnConnect(func(c *socketio.Conn) {
				sio.Broadcast(struct{ announcement string }{"connected: " + c.String()})
			})

			sio.OnDisconnect(func(c *socketio.Conn) {
				sio.BroadcastExcept(c, struct{ announcement string }{"disconnected: " + c.String()})
			})

			sio.OnMessage(func(c *socketio.Conn, msg string) {
				sio.BroadcastExcept(c,
					struct{ message []string }{[]string{c.String(), msg}})
			})

			log.Println("Server started.")
			if err := http.ListenAndServe(":8080", nil); err != nil {
				log.Exitln("ListenAndServer:", err)
			}
		}
*/
package socketio

import (
	"http"
	"os"
	"strings"
	"sync"
)

// SocketIO handles transport abstraction and provide the user
// a handfull of callbacks to observe different events.
type SocketIO struct {
	sessions map[SessionID]*Conn // Holds the outstanding sessions.
	mutex    *sync.RWMutex       // Protects the sessions.
	config   Config              // Holds the configuration values.
	muxed    bool                // Is the server muxed already.

	totalPacketsSent     int64
	totalPacketsReceived int64
	totalSessions        int64
	totalRequests        int64

	// The callbacks set by the user
	callbacks struct {
		onConnect    func(*Conn)          // Invoked on new connection.
		onDisconnect func(*Conn)          // Invoked on a lost connection.
		onMessage    func(*Conn, Message) // Invoked on a message.
	}
}

// NewSocketIO creates a new socketio server with chosen transports and configuration
// options. If transports is nil, the DefaultTransports is used. If config is nil, the
// DefaultConfig is used.
func NewSocketIO(config *Config) *SocketIO {
	if config == nil {
		config = &DefaultConfig
	}

	return &SocketIO{
		config:   *config,
		sessions: make(map[SessionID]*Conn),
		mutex:    new(sync.RWMutex),
	}
}

// Broadcast schedules data to be sent to each connection.
func (sio *SocketIO) Broadcast(data interface{}) {
	sio.BroadcastExcept(nil, data)
}

// BroadcastExcept schedules data to be sent to each connection except
// c. It does not care about the type of data, but it must marshallable
// by the standard json-package.
func (sio *SocketIO) BroadcastExcept(c *Conn, data interface{}) {
	sio.mutex.RLock()
	defer sio.mutex.RUnlock()

	for _, v := range sio.sessions {
		if v != c {
			v.Send(data)
		}
	}
}

// GetConn digs for a session with sessionid and returns it.
func (sio *SocketIO) GetConn(sessionid SessionID) (c *Conn) {
	sio.mutex.RLock()
	c = sio.sessions[sessionid]
	sio.mutex.RUnlock()
	return
}

// Mux maps resources to the http.ServeMux mux under the resource given.
// The resource must end with a slash and if the mux is nil, the
// http.DefaultServeMux is used. It registers handlers for URLs like:
// <resource><t.resource>[/], e.g. /socket.io/websocket && socket.io/websocket/.
func (sio *SocketIO) Mux(resource string, mux *http.ServeMux) os.Error {
	if mux == nil {
		mux = http.DefaultServeMux
	}

	if sio.muxed {
		return os.NewError("Mux: already muxed")
	}

	if resource == "" || resource[len(resource)-1] != '/' {
		return os.NewError("Mux: resource must end with a slash")
	}

	for _, t := range sio.config.Transports {
		tt := t
		tresource := resource + tt.Resource()
		mux.HandleFunc(tresource+"/", func(w http.ResponseWriter, req *http.Request) {
			sio.handle(tt, w, req)
		})
		mux.HandleFunc(tresource, func(w http.ResponseWriter, req *http.Request) {
			sio.handle(tt, w, req)
		})
	}

	sio.muxed = true
	return nil
}

// OnConnect sets f to be invoked when a new session is established. It passes
// the established connection as an argument to the callback.
func (sio *SocketIO) OnConnect(f func(*Conn)) os.Error {
	if sio.muxed {
		return os.NewError("OnConnect: already muxed")
	}
	sio.callbacks.onConnect = f
	return nil
}

// OnDisconnect sets f to be invoked when a session is considered to be lost. It passes
// the established connection as an argument to the callback. After disconnection
// the connection is considered to be destroyed, and it should not be used anymore.
func (sio *SocketIO) OnDisconnect(f func(*Conn)) os.Error {
	if sio.muxed {
		return os.NewError("OnDisconnect: already muxed")
	}
	sio.callbacks.onDisconnect = f
	return nil
}

// OnMessage sets f to be invoked when a message arrives. It passes
// the established connection along with the received message as arguments
// to the callback.
func (sio *SocketIO) OnMessage(f func(*Conn, Message)) os.Error {
	if sio.muxed {
		return os.NewError("OnMessage: already muxed")
	}
	sio.callbacks.onMessage = f
	return nil
}

func (sio *SocketIO) Log(v ...interface{}) {
	if sio.config.Logger != nil {
		sio.config.Logger.Println(v...)
	}
}

func (sio *SocketIO) Logf(format string, v ...interface{}) {
	if sio.config.Logger != nil {
		sio.config.Logger.Printf(format, v...)
	}
}

// Handle is invoked on every http-request coming through the muxer.
// It is responsible for parsing the request and passing the http conn/req -pair
// to the corresponding sio connections. It also creates new connections when needed.
// The URL and method must be one of the following:
//
// OPTIONS *
//     GET resource
//     GET resource/sessionid
//    POST resource/sessionid
func (sio *SocketIO) handle(t Transport, w http.ResponseWriter, req *http.Request) {
	var parts []string
	var c *Conn
	var err os.Error

	sio.mutex.Lock()
	sio.totalRequests++
	sio.mutex.Unlock()

	if origin, ok := req.Header["Origin"]; ok {
		if _, ok = sio.verifyOrigin(origin); !ok {
			sio.Log("sio/handle: unauthorized origin:", origin)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.SetHeader("Access-Control-Allow-Origin", origin)
		w.SetHeader("Access-Control-Allow-Credentials", "true")
		w.SetHeader("Access-Control-Allow-Methods", "POST, GET")
	}

	switch req.Method {
	case "OPTIONS":
		w.WriteHeader(http.StatusOK)
		return

	case "GET", "POST":
		break

	default:
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// TODO: fails if the session id matches the transport
	if i := strings.LastIndex(req.URL.Path, t.Resource()); i >= 0 {
		pathLen := len(req.URL.Path)
		if req.URL.Path[pathLen-1] == '/' {
			pathLen--
		}

		parts = strings.Split(req.URL.Path[i:pathLen], "/", -1)
	}

	switch len(parts) {
	case 1:
		// only resource was present, so create a new connection
		c, err = newConn(sio)
		if err != nil {
			sio.Log("sio/handle: unable to create a new connection:", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

	case 2:
		fallthrough

	case 3:
		// session id was present
		c = sio.GetConn(SessionID(parts[1]))
	}

	// we should now have a connection
	if c == nil {
		sio.Log("sio/handle: unable to map request to connection:", req.RawURL)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// pass the http conn/req pair to the connection
	if err = c.handle(t, w, req); err != nil {
		sio.Logf("sio/handle: conn/handle: %s: %s", c, err)
		w.WriteHeader(http.StatusUnauthorized)
	}
}

// OnConnect is invoked by a connection when a new connection has been
// established succesfully. The establised connection is passed as an
// argument. It stores the connection and calls the user's OnConnect callback.
func (sio *SocketIO) onConnect(c *Conn) {
	sio.mutex.Lock()
	sio.sessions[c.sessionid] = c
	sio.totalSessions++
	sio.mutex.Unlock()

	if sio.callbacks.onConnect != nil {
		sio.callbacks.onConnect(c)
	}
}

// OnDisconnect is invoked by a connection when the connection is considered
// to be lost. It removes the connection and calls the user's OnDisconnect callback.
func (sio *SocketIO) onDisconnect(c *Conn) {
	sio.mutex.Lock()
	sio.sessions[c.sessionid] = nil, false
	sio.totalPacketsSent += int64(c.numPacketsSent)
	sio.totalPacketsReceived += int64(c.numPacketsReceived)
	sio.mutex.Unlock()

	if sio.callbacks.onDisconnect != nil {
		sio.callbacks.onDisconnect(c)
	}
}

// OnMessage is invoked by a connection when a new message arrives. It passes
// this message to the user's OnMessage callback.
func (sio *SocketIO) onMessage(c *Conn, msg Message) {
	if sio.callbacks.onMessage != nil {
		sio.callbacks.onMessage(c, msg)
	}
}

func (sio *SocketIO) verifyOrigin(reqOrigin string) (string, bool) {
	if sio.config.Origins == nil {
		return "", false
	}

	url, err := http.ParseURL(reqOrigin)
	if err != nil || url.Host == "" {
		return "", false
	}

	host := strings.Split(url.Host, ":", 2)

	for _, o := range sio.config.Origins {
		origin := strings.Split(o, ":", 2)
		if origin[0] == "*" || origin[0] == host[0] {
			if len(origin) < 2 || origin[1] == "*" {
				return o, true
			}
			if len(host) < 2 {
				switch url.Scheme {
				case "http", "ws":
					if origin[1] == "80" {
						return o, true
					}

				case "https", "wss":
					if origin[1] == "443" {
						return o, true
					}
				}
			} else if origin[1] == host[1] {
				return o, true
			}
		}
	}

	return "", false
}
