package socketio

import (
	"http"
	"os"
	"bytes"
	"strings"
	"fmt"
	"net"
	"io"
)

// The flashsocket transport.
type flashsocketTransport struct {
	wsTransport *websocketTransport
}

// Creates a new flashsocket transport with the given read and write timeouts.
func NewFlashsocketTransport(rtimeout, wtimeout int64) Transport {
	return &flashsocketTransport{&websocketTransport{rtimeout, wtimeout}}
}

// Returns the resource name.
func (t *flashsocketTransport) Resource() string {
	return "flashsocket"
}

// Creates a new socket that can be used with a connection.
func (t *flashsocketTransport) newSocket() socket {
	return &flashsocketSocket{t: t, s: t.wsTransport.newSocket()}
}

// flashsocketTransport implements the transport interface for flashsockets
type flashsocketSocket struct {
	t *flashsocketTransport // the transport configuration
	s socket
}

// Transport returns the transport the socket is based on.
func (s *flashsocketSocket) Transport() Transport {
	return s.t
}

// String returns the verbose representation of the socket.
func (s *flashsocketSocket) String() string {
	return s.t.Resource()
}

// Accepts a http connection & request pair. It upgrades the connection and calls
// proceed if succesfull.
//
// TODO: Remove the ugly channels and timeouts. They should not be needed!
func (s *flashsocketSocket) accept(w http.ResponseWriter, req *http.Request, proceed func()) (err os.Error) {
	return s.s.accept(w, req, proceed)
}

func (s *flashsocketSocket) Read(p []byte) (int, os.Error) {
	return s.s.Read(p)
}

func (s *flashsocketSocket) Write(p []byte) (int, os.Error) {
	return s.s.Write(p)
}

func (s *flashsocketSocket) Close() os.Error {
	return s.s.Close()
}

func generatePolicyFile(sio *SocketIO) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(`<?xml version="1.0"?>
<!DOCTYPE cross-domain-policy SYSTEM "http://www.macromedia.com/xml/dtds/cross-domain-policy.dtd">
<cross-domain-policy>
	<site-control permitted-cross-domain-policies="master-only" />
`)

	if sio.config.Origins != nil {
		for _, origin := range sio.config.Origins {
			parts := strings.Split(origin, ":", 2)
			if len(parts) < 1 {
				continue
			}
			host, port := "*", "*"
			if parts[0] != "" {
				host = parts[0]
			}
			if len(parts) == 2 && parts[1] != "" {
				port = parts[1]
			}

			fmt.Fprintf(buf, "\t<allow-access-from domain=\"%s\" to-ports=\"%s\" />\n", host, port)
		}
	}

	buf.WriteString("</cross-domain-policy>\n")
	return buf.Bytes()
}

func (sio *SocketIO) ListenAndServeFlashPolicy(laddr string) os.Error {
	var listener net.Listener

	listener, err := net.Listen("tcp", laddr)
	if err != nil {
		return err
	}

	policy := generatePolicyFile(sio)

	for {
		conn, err := listener.Accept()
		if err != nil {
			sio.Log("ServeFlashsocketPolicy:", err)
			continue
		}

		go func() {
			defer conn.Close()

			buf := make([]byte, 20)
			if _, err := io.ReadFull(conn, buf); err != nil {
				sio.Log("ServeFlashsocketPolicy:", err)
				return
			}
			if !bytes.Equal([]byte("<policy-file-request"), buf) {
				sio.Logf("ServeFlashsocketPolicy: expected \"<policy-file-request\" but got %q", buf)
				return
			}

			var nw int
			for nw < len(policy) {
				n, err := conn.Write(policy[nw:])
				if err != nil && err != os.EAGAIN {
					sio.Log("ServeFlashsocketPolicy:", err)
					return
				}
				if n > 0 {
					nw += n
					continue
				} else {
					sio.Log("ServeFlashsocketPolicy: wrote 0 bytes")
					return
				}
			}
			sio.Log("ServeFlashsocketPolicy: served", conn.RemoteAddr())
		}()
	}

	return nil
}
