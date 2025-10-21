package api

import (
	"jogodistribuido/servidor/tipos"
	"log"

	"jogodistribuido/servidor/cluster"

	"github.com/gin-gonic/gin"
)

// ServidorInterface define as operações que a API pode precisar do Servidor principal (não relacionadas a cluster)
type ServidorInterface interface {
	EncaminharParaLider(*gin.Context)
	FormarPacote() ([]tipos.Carta, error)
	NotificarCompraSucesso(string, []tipos.Carta)
	GetStatusEstoque() (map[string]int, int)
	GetFilaDeEspera() []*tipos.Cliente
	GetMeuEndereco() string
}

type Server struct {
	router         *gin.Engine
	endereco       string
	servidor       ServidorInterface
	clusterManager cluster.ClusterManagerInterface
}

func NewServer(endereco string, s ServidorInterface, cm cluster.ClusterManagerInterface) *Server {
	router := gin.New()
	router.Use(gin.Recovery())
	// router.Use(gin.Logger()) // Descomentado para depuração se necessário

	apiServer := &Server{
		router:         router,
		endereco:       endereco,
		servidor:       s,
		clusterManager: cm,
	}

	apiServer.setupRoutes()
	return apiServer
}

func (s *Server) Run() {
	log.Printf("API REST iniciada em %s", s.endereco)
	if err := s.router.Run(s.endereco); err != nil {
		log.Fatalf("Erro ao iniciar API: %v", err)
	}
}

func (s *Server) setupRoutes() {
	// Rotas públicas (sem autenticação)
	s.router.POST("/register", s.handleRegister)
	s.router.POST("/heartbeat", s.handleHeartbeat)
	s.router.GET("/servers", s.handleGetServers)

	// Rotas de eleição (usadas internamente pelos servidores)
	election := s.router.Group("/election")
	{
		election.POST("/vote", s.handleRequestVote)
		election.POST("/leader", s.handleAnnounceLeader)
	}

	// Rotas de matchmaking (protegidas por JWT)
	matchmaking := s.router.Group("/matchmaking", authMiddleware())
	{
		matchmaking.POST("/solicitar_oponente", s.handleSolicitarOponente)
		matchmaking.POST("/confirmar_partida", s.handleConfirmarPartida)
	}

	// Rotas de estoque (protegidas por JWT e requerem liderança)
	stock := s.router.Group("/estoque", authMiddleware(), s.leaderOnlyMiddleware())
	{
		stock.POST("/comprar_pacote", s.handleComprarPacote)
		stock.GET("/status", s.handleGetEstoque)
	}

	// Rotas para a lógica do jogo (sincronização Host/Sombra)
	game := s.router.Group("/game", authMiddleware())
	{
		game.POST("/start", s.handleGameStart)
		game.POST("/event", s.handleGameEvent)
		game.POST("/replicate", s.handleGameReplicate)
	}

	// Rotas de sincronização de partidas (mantidas para compatibilidade, agora dentro do grupo /partida)
	partida := s.router.Group("/partida", authMiddleware())
	{
		partida.POST("/encaminhar_comando", s.handleEncaminharComando)
		partida.POST("/sincronizar_estado", s.handleSincronizarEstado)
		partida.POST("/notificar_jogador", s.handleNotificarJogador)
		partida.POST("/atualizar_estado", s.handleAtualizarEstado)
		partida.POST("/notificar_pronto", s.handleNotificarPronto)
	}
}
