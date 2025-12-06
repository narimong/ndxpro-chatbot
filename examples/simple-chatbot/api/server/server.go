package server

import (
	"github.com/gin-gonic/gin"

	"simple-chatbot/api/handler"
	"simple-chatbot/api/service"
)

type Server struct {
	router          *gin.Engine
	sessionHandler  *handler.SessionHandler
	socketIOHandler *handler.SocketIOHandler
}

func New(sessionStore *service.SessionStore, chatService *service.ChatService) *Server {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(LoggerMiddleware())
	router.Use(CORSMiddleware())

	sessionHandler := handler.NewSessionHandler(sessionStore)
	socketIOHandler := handler.NewSocketIOHandler(sessionStore, chatService)

	s := &Server{
		router:          router,
		sessionHandler:  sessionHandler,
		socketIOHandler: socketIOHandler,
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.router.GET("/api/v1/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := s.router.Group("/api/v1")
	{
		sessions := api.Group("/sessions")
		{
			sessions.POST("", s.sessionHandler.Create)
			sessions.GET("", s.sessionHandler.List)
			sessions.GET("/:id", s.sessionHandler.Get)
			sessions.DELETE("/:id", s.sessionHandler.Delete)
			sessions.POST("/:id/clear", s.sessionHandler.Clear)
		}
	}

	s.router.GET("/socket.io/*any", gin.WrapH(s.socketIOHandler.Handler()))
	s.router.POST("/socket.io/*any", gin.WrapH(s.socketIOHandler.Handler()))
}

func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

func (s *Server) Router() *gin.Engine {
	return s.router
}
