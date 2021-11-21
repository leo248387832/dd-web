package http

import (
	"context"
	_ "embed"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"sync"
	"time"
)

//go:embed index.html
var indexTemplate string

type Service struct {
	hs                  *http.Server
	ctx                 context.Context
	cancel              context.CancelFunc
	ddMsgChan           chan map[string]string
	lastProgress        string
	wsCons              map[string]*wsConn
	wsConsLock          sync.Mutex
	authKey             string
	msgSendDone         bool
	msgSendDoneLock     sync.RWMutex
	ddMsgChanClosed     bool
	ddMsgChanClosedLock sync.RWMutex
	graceStop           bool
	graceStopLock       sync.RWMutex
}

// CreateService 创建http服务
func CreateService(addr string, ctx context.Context, cancel context.CancelFunc, ddMsgChan chan map[string]string, authKey string) *Service {
	s := &Service{
		ctx:       ctx,
		cancel:    cancel,
		ddMsgChan: ddMsgChan,
		wsCons:    make(map[string]*wsConn),
		authKey:   authKey,
	}
	s.hs = &http.Server{
		Addr:         addr,
		Handler:      s.createHandler(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s
}

// createHandler 创建路由
func (s *Service) createHandler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// 默认页面
	r.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(200, "请在地址栏中输入正确的AuthKey以访问DD进度")
	})

	// 主界面
	r.GET("/"+s.authKey+"/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(200, indexTemplate)
	})

	// Websocket
	r.GET("/"+s.authKey+"/ws", s.wsHandler)

	// 退出进程
	r.GET("/"+s.authKey+"/proc_exit", func(c *gin.Context) {
		if s.cancel != nil {
			c.JSON(http.StatusOK, gin.H{
				"code":    0,
				"message": "进程已经开始退出",
				"data":    map[string]interface{}{},
			})
			s.cancel()
		} else {
			c.JSON(http.StatusOK, gin.H{
				"code":    1,
				"message": "cancel func == nil",
				"data":    map[string]interface{}{},
			})
		}
	})
	return r
}

// Start 启动Http服务
func (s *Service) Start() {
	if s.hs != nil {
		go func() {
			err := s.hs.ListenAndServe()
			if err != nil {
				log.Println("Http Service Exit: ", err.Error())
			}
			s.cancel()
		}()
	}

	if s.ddMsgChan != nil {
		go func() {
			defer func() {
				if err := recover(); err != nil {
					log.Print("s.ddMsgChan Message Recv Error")
					s.ddMsgChanClosedLock.Lock()
					s.ddMsgChanClosed = true
					s.ddMsgChanClosedLock.Unlock()
				}
			}()
			for {
			LOOP:
				select {
				case message, ok := <-s.ddMsgChan:
					if !ok {
						s.ddMsgChanClosedLock.Lock()
						s.ddMsgChanClosed = true
						s.ddMsgChanClosedLock.Unlock()
						break LOOP
					}
					s.msgSendDoneLock.Lock()
					s.msgSendDone = false
					s.msgSendDoneLock.Unlock()
					if message["type"] == "progress" {
						s.lastProgress = message["data"]
					}
					for id, conn := range s.wsCons {
						select {
						case conn.Send <- message["data"]:
						default:
							close(conn.Send)
							delete(s.wsCons, id)
						}
					}
					s.msgSendDoneLock.Lock()
					s.msgSendDone = true
					s.msgSendDoneLock.Unlock()
				}
			}
		}()
	} else {
		s.ddMsgChanClosedLock.Lock()
		s.ddMsgChanClosed = true
		s.ddMsgChanClosedLock.Unlock()
	}
}

// wsHandler Websocket
func (s *Service) wsHandler(c *gin.Context) {
	conn, err := (&websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}).Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		http.NotFound(c.Writer, c.Request)
		return
	}

	wc := &wsConn{
		Id:      conn.LocalAddr().String() + conn.RemoteAddr().String() + "_ws_conn_network_id",
		Conn:    conn,
		Send:    make(chan string, 2333),
		Service: s,
	}
	s.wsConsLock.Lock()
	s.wsCons[wc.Id] = wc
	s.wsConsLock.Unlock()
	if s.lastProgress != "" {
		// 跟前端同步进度
		wc.Send <- s.lastProgress
	}
	go wc.Read()
	go wc.Write()
}

// Stop 停止Http服务
func (s *Service) Stop(waitMsgClean bool) {
	s.graceStopLock.Lock()
	s.graceStop = true
	s.graceStopLock.Unlock()
	if waitMsgClean {
		log.Println("Waiting For the Message Channel Clean...")
		for {
			chLen := len(s.ddMsgChan)
			log.Println("Now Channel Len: ", chLen)
			s.msgSendDoneLock.RLock()
			_msgSendDone := s.msgSendDone
			s.msgSendDoneLock.RUnlock()
			s.ddMsgChanClosedLock.RLock()
			_ddMsgChanClosed := s.ddMsgChanClosed
			s.ddMsgChanClosedLock.RUnlock()
			haveWsConnData := false
			for id, conn := range s.wsCons {
				if len(conn.Send) != 0 {
					log.Println("Ws Client[", id, "] Have Data")
					haveWsConnData = true
				}
			}
			if chLen == 0 && _msgSendDone && _ddMsgChanClosed && !haveWsConnData {
				log.Println("Channel Already Clean")
				// 等待12秒,保证send队列全部发送完成
				time.Sleep(time.Second * 6)
				break
			}
		}
	}
	if len(s.wsCons) != 0 {
		for id, conn := range s.wsCons {
			close(conn.Send)
			delete(s.wsCons, id)
		}
	}
	if s.hs != nil {
		_ = s.hs.Close()
	}
}
