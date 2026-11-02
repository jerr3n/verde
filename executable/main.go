package main

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/jerr3n/verde/executable/internal/logging"
)

func main() {
	logger, err := logging.New(gin.IsDebugging())
	if err != nil {
		panic(err)
	}
	defer logger.Sync()
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

	if err := r.Run(); err != nil {
		logger.Fatal("server stopped", zap.Error(err))
	}
}
