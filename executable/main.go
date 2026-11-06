package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/logging"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
)

func handleWs(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := c.Request.Context()

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

func serve(logger *zap.Logger, addr string) error {
	gin.DebugPrintRouteFunc = logging.RouteLogger(logger)

	r := gin.New()
	r.Use(logging.Middleware(logger), logging.Recovery(logger))

	r.GET("/", func(c *gin.Context) {
		if c.IsWebsocket() {
			handleWs(c)
			return
		}

		c.JSON(200, gin.H{
			"message": "Hello over HTTP!",
		})
	})

	r.GET("/ws", handleWs)

	logger.Info("listening", zap.String("addr", addr))
	return r.Run(addr)
}

func main() {
	logger, err := logging.New(gin.IsDebugging())
	if err != nil {
		panic(err)
	}
	defer logger.Sync()
	var cmd = &cli.Command{
		Name:                  "verde",
		Description:           "multi-directional sync between roblox",
		EnableShellCompletion: true,
		Commands: []*cli.Command{
			{
				Name: "serve",
				Action: func(ctx context.Context, command *cli.Command) error {
					cfg, err := config.Load(config.DefaultPath)
					if err != nil {
						return err
					}
					return serve(logger, cfg.Addr())
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
