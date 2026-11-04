package main

import (
	"context"
	"errors"
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

func parseCfg() (*config.Cfg, error) {
	load, err := config.Load("project.yml")
	if err != nil {
		return nil, err
	}
	if load.Serve.Address == "" {
		load.Serve.Address = "127.0.0.1"
	}
	if load.Serve.Port < 1 || load.Serve.Port > 65535 {
		return nil, errors.New("invalid port")
	}
	if load.Conflicts.OnConnect == "" {
		load.Conflicts.OnConnect = config.OnConnectDisk
	}
	if load.Conflicts.OnConnect != config.OnConnectDisk && load.Conflicts.OnConnect != config.OnConnectStudio && load.Conflicts.OnConnect != config.OnConnectInteractive {
		return nil, errors.New("invalid onconnect value")
	}
	if len(load.Sync) == 0 {
		return nil, errors.New("need at least one sync value")
	}
	paths := map[string]bool{}
	instances := map[string]bool{}
	for i, sync := range load.Sync {
		if paths[sync.Path] {
			return nil, fmt.Errorf("duplicate path <%s> at element %d", sync.Path, i)
		}
		if instances[sync.Instance] {
			return nil, fmt.Errorf("duplicate instance <%s> at element %d", sync.Instance, i)
		}
		paths[sync.Path] = true
		instances[sync.Instance] = true
	}
	return load, nil
}

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
					addr := ":8080"
					if port := os.Getenv("PORT"); port != "" { // same default as gin's r.Run()
						addr = ":" + port
					}
					return serve(logger, addr)
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
