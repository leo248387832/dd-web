package http

import (
	"github.com/gorilla/websocket"
)

type wsConn struct {
	Send    chan string
	Conn    *websocket.Conn
	Id      string
	Service *Service
}

func (wc *wsConn) Read() {
	defer func() {
		wc.Service.wsConsLock.Lock()
		wc.Service.wsCons[wc.Id] = wc
		wc.Service.wsConsLock.Unlock()
		close(wc.Send)
		delete(wc.Service.wsCons, wc.Id)
		_ = wc.Conn.Close()
	}()

	for {
		_, message, err := wc.Conn.ReadMessage()
		if err != nil {
			break
		}
		if string(message) == "ping" {
			wc.Service.graceStopLock.RLock()
			_graceStop := wc.Service.graceStop
			wc.Service.graceStopLock.RUnlock()
			if !_graceStop {
				wc.Send <- "pong"
			}
		}
	}
}

func (wc *wsConn) Write() {
	defer func() {
		_ = wc.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-wc.Send:
			if !ok {
				// 可能是已经关闭这个链接了
				_ = wc.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = wc.Conn.WriteMessage(websocket.TextMessage, []byte(message))
		}
	}
}
