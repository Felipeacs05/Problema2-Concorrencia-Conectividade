package api

import (
	"encoding/json"
	"jogodistribuido/protocolo"
	"jogodistribuido/servidor/seguranca"
	"jogodistribuido/servidor/tipos"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// authMiddleware middleware para validar JWT em requisições REST
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token de autorização ausente"})
			c.Abort()
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		serverID, err := seguranca.ValidateJWT(tokenString) // Usa a função do pacote de segurança
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token inválido: " + err.Error()})
			c.Abort()
			return
		}

		c.Set("server_id", serverID)
		c.Next()
	}
}

// Handlers de descoberta
func (s *Server) handleRegister(c *gin.Context) {
	// Lê o body cru para suportar casos onde o campo pode ser `id` por compatibilidade
	body, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "falha ao ler body"})
		return
	}

	var novoServidor tipos.InfoServidor
	// Tenta decodificar diretamente
	if err := json.Unmarshal(body, &novoServidor); err != nil {
		// Tenta decodificar como mapa e extrair possíveis campos alternativos
		var alt map[string]interface{}
		if err2 := json.Unmarshal(body, &alt); err2 == nil {
			if v, ok := alt["endereco"].(string); ok && strings.TrimSpace(v) != "" {
				novoServidor.Endereco = strings.TrimSpace(v)
			} else if v, ok := alt["id"].(string); ok && strings.TrimSpace(v) != "" {
				// Compatibilidade com payloads que usam `id` em vez de `endereco`
				novoServidor.Endereco = strings.TrimSpace(v)
			}
		}
	}

	if strings.TrimSpace(novoServidor.Endereco) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "campo 'endereco' obrigatório"})
		return
	}

	// Atualiza metadados
	novoServidor.UltimoPing = time.Now()
	novoServidor.Ativo = true

	servidoresAtuais := s.clusterManager.RegistrarServidor(&novoServidor)
	log.Printf("Servidor registrado: %s", novoServidor.Endereco)
	c.JSON(http.StatusOK, servidoresAtuais)
}

func (s *Server) handleHeartbeat(c *gin.Context) {
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload de heartbeat inválido"})
		return
	}
	remetente, ok := payload["remetente"].(string)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Remetente do heartbeat ausente ou inválido"})
		return
	}
	s.clusterManager.ProcessarHeartbeat(remetente, payload)
	c.Status(http.StatusOK)
}

func (s *Server) handleGetServers(c *gin.Context) {
	c.JSON(http.StatusOK, s.clusterManager.GetServidores())
}

// Handlers de eleição
func (s *Server) handleRequestVote(c *gin.Context) {
	var req struct {
		Candidato string `json:"candidato"`
		Termo     int64  `json:"termo"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Requisição de voto inválida"})
		return
	}
	votoConcedido, termoAtual := s.clusterManager.ProcessarVoto(req.Candidato, req.Termo)
	c.JSON(http.StatusOK, gin.H{
		"voto_concedido": votoConcedido,
		"termo":          termoAtual,
	})
}

func (s *Server) handleAnnounceLeader(c *gin.Context) {
	var req struct {
		NovoLider string `json:"novo_lider"`
		Termo     int64  `json:"termo"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Anúncio de líder inválido"})
		return
	}
	s.clusterManager.DeclararLider(req.NovoLider, req.Termo)
	c.JSON(http.StatusOK, gin.H{"status": "líder anunciado recebido"})
}

// Middleware para verificar se a requisição deve ser processada pelo líder
func (s *Server) leaderOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.clusterManager.SouLider() {
			// Encaminha para o líder
			s.servidor.EncaminharParaLider(c)
			c.Abort()
			return
		}
		c.Next()
	}
}

// Handlers de estoque (protegidos pelo middleware)
func (s *Server) handleComprarPacote(c *gin.Context) {
	var req struct {
		ClienteID string `json:"cliente_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Requisição inválida"})
		return
	}

	pacote, err := s.servidor.FormarPacote()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Falha ao formar pacote"})
		return
	}

	go s.servidor.NotificarCompraSucesso(req.ClienteID, pacote)

	c.JSON(http.StatusOK, gin.H{"pacote": pacote, "mensagem": "Compra processada, notificação sendo enviada."})
}

func (s *Server) handleGetEstoque(c *gin.Context) {
	status, total := s.servidor.GetStatusEstoque()
	c.JSON(http.StatusOK, gin.H{"status": status, "total": total})
}

// Handlers de partida
func (s *Server) handleEncaminharComando(c *gin.Context) {
	var req struct {
		SalaID  string             `json:"sala_id"`
		Comando protocolo.Mensagem `json:"comando"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Requisição inválida"})
		return
	}

	log.Printf("[ENCAMINHAMENTO_RX] Comando '%s' recebido para a sala %s", req.Comando.Comando, req.SalaID)

	// Injeta o comando no canal da partida para ser processado pelo Host
	if err := s.servidor.ProcessarComandoRemoto(req.SalaID, req.Comando); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "comando encaminhado para processamento"})
}

func (s *Server) handleSincronizarEstado(c *gin.Context) {
	// ... (código a ser movido)
}

func (s *Server) handleNotificarJogador(c *gin.Context) {
	// ... (código a ser movido)
}

func (s *Server) handleIniciarRemoto(c *gin.Context) {
	var estado tipos.EstadoPartida
	if err := c.ShouldBindJSON(&estado); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Estado da partida inválido"})
		return
	}

	log.Printf("[SYNC_SOMBRA_RX] Recebido estado inicial da partida %s. Turno de: %s", estado.SalaID, estado.TurnoDe)
	s.servidor.AtualizarEstadoSalaRemoto(estado)

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) handleAtualizarEstado(c *gin.Context) {
	// ... (código a ser movido)
}

func (s *Server) handleNotificarPronto(c *gin.Context) {
	// ... (código a ser movido)
}

// HANDLERS DE MATCHMAKING GLOBAL
func (s *Server) handleSolicitarOponente(c *gin.Context) {
	var req struct {
		SolicitanteID   string `json:"solicitante_id"`
		SolicitanteNome string `json:"solicitante_nome"`
		ServidorOrigem  string `json:"servidor_origem"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dados inválidos"})
		return
	}

	// Tenta encontrar um oponente na fila local
	oponente := s.servidor.RemoverPrimeiroDaFila()

	if oponente != nil {
		// Oponente encontrado!
		log.Printf("[MATCHMAKING_RX] Oponente '%s' encontrado localmente para solicitante '%s' de %s", oponente.Nome, req.SolicitanteNome, req.ServidorOrigem)

		// Cria um objeto Cliente para o solicitante remoto
		solicitante := &tipos.Cliente{
			ID:   req.SolicitanteID,
			Nome: req.SolicitanteNome,
		}

		// Cria a sala. Servidor local será o Host.
		salaID := s.servidor.CriarSalaRemotaComSombra(solicitante, oponente, req.ServidorOrigem)
		// Responde ao servidor de origem com sucesso
		c.JSON(http.StatusOK, gin.H{
			"partida_encontrada": true,
			"sala_id":            salaID,                      // <-- CORREÇÃO: Envia o ID da sala
			"servidor_host":      s.servidor.GetMeuEndereco(), // <-- CORREÇÃO: Informa quem é o Host
			"oponente_nome":      oponente.Nome,               // Retorna o nome do jogador local para o solicitante
			"oponente_id":        oponente.ID,
		})
		return
	}

	// Não encontrou oponente
	log.Printf("[MATCHMAKING_RX] Nenhum oponente na fila para '%s'", req.SolicitanteNome)
	c.JSON(http.StatusOK, gin.H{"partida_encontrada": false})
}

func (s *Server) handleConfirmarPartida(c *gin.Context) {
	// ... (código a ser movido)
}

// HANDLERS DOS NOVOS ENDPOINTS PADRÃO
func (s *Server) handleGameStart(c *gin.Context) {
	// ... (código a ser movido)
}

func (s *Server) handleGameEvent(c *gin.Context) {
	var req tipos.GameEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload inválido"})
		return
	}

	// Valida assinatura
	event := tipos.GameEvent{
		EventSeq:  req.EventSeq,
		MatchID:   req.MatchID,
		EventType: req.EventType,
		PlayerID:  req.PlayerID,
		Signature: req.Signature,
	}
	if !seguranca.VerifyEventSignature(&event) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Assinatura inválida"})
		return
	}

	// Busca a sala usando método da interface
	salas := s.servidor.GetSalas()
	sala, ok := salas[req.MatchID]

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sala não encontrada"})
		return
	}

	// Processa o evento como Host
	estado := s.servidor.ProcessarEventoComoHost(sala, &req)
	if estado == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Evento rejeitado"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "evento_processado"})
}

func (s *Server) handleGameReplicate(c *gin.Context) {
	// ... (código a ser movido)
}

// handleEncaminharChat recebe uma mensagem de chat do Host e a retransmite para o cliente local (usado pelo Shadow)
func (s *Server) handleEncaminharChat(c *gin.Context) {
	var req struct {
		SalaID      string `json:"sala_id"`
		NomeJogador string `json:"nome_jogador"`
		Texto       string `json:"texto"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload inválido"})
		return
	}
	s.servidor.PublicarChatRemoto(req.SalaID, req.NomeJogador, req.Texto)
	c.JSON(http.StatusOK, gin.H{"status": "chat_relayed"})
}
