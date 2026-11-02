package main

import (
	"context"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

func handleWs(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := context.Background()

	for {
		messageType, message, err := conn.Read(ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			if status != websocket.StatusNormalClosure &&
				status != websocket.StatusGoingAway {
				log.Printf("websocket read: %v", err)
			}
			return
		}

		log.Printf("received: %s", message)

		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = conn.Write(writeCtx, messageType, message)
		cancel()

		if err != nil {
			log.Printf("websocket write: %v", err)
			return
		}
	}
}
