package api

import (
	"encoding/json"
	"fmt"
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
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Cabeçalho de autorização ausente ou mal formatado"})
			c.Abort()
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		log.Printf("[AUTH_MIDDLEWARE] Recebido token para validação.")
		serverID, err := seguranca.ValidateJWT(tokenString) // Usa a função do pacote de segurança
		if err != nil {
			log.Printf("[AUTH_MIDDLEWARE] Erro na validação do JWT: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token inválido: " + err.Error()})
			c.Abort()
			return
		}

		log.Printf("[AUTH_MIDDLEWARE] Token validado com sucesso para server_id: %s", serverID)
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

	// Processa troca de cartas DIRETAMENTE no handler HTTP (já veio do Shadow)
	if req.Comando.Comando == "TROCAR_CARTAS" {
		var trocaReq protocolo.TrocarCartasReq
		if err := json.Unmarshal(req.Comando.Dados, &trocaReq); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Dados de troca inválidos"})
			return
		}

		// Acessa a sala diretamente da interface
		salas := s.servidor.GetSalas()
		if salas == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Salas não disponível"})
			return
		}
		sala := salas[req.SalaID]
		if sala == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Sala não encontrada"})
			return
		}

		// Processa a troca imediatamente
		s.servidor.ProcessarTrocaDireta(sala, &trocaReq)
		c.JSON(http.StatusOK, gin.H{"status": "troca processada"})
		return
	}

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
	var req struct {
		ClienteID string             `json:"cliente_id"`
		Mensagem  protocolo.Mensagem `json:"mensagem"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Requisição inválida"})
		return
	}

	log.Printf("[NOTIFICACAO-REMOTA_RX] Notificando jogador %s localmente", req.ClienteID)

	// CORREÇÃO: Se for mensagem de ATUALIZACAO_JOGO, ajusta contagem de cartas usando método do servidor
	if req.Mensagem.Comando == "ATUALIZACAO_JOGO" {
		s.servidor.AjustarContagemCartasLocal(req.ClienteID, &req.Mensagem)
	}

	// Publica a mensagem no MQTT local para o cliente
	s.servidor.PublicarParaCliente(req.ClienteID, req.Mensagem)

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
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

// Aplica a troca localmente para um cliente deste servidor: remove a carta desejada dele e adiciona a carta oferecida
func (s *Server) handleAplicarTrocaLocal(c *gin.Context) {
	var req struct {
		ClienteID       string      `json:"cliente_id"`
		CartaDesejadaID string      `json:"carta_desejada_id"`
		CartaOferecida  tipos.Carta `json:"carta_oferecida"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload inválido"})
		return
	}

	aplicado, cartaRemovida, inventario := s.servidor.AplicarTrocaLocal(req.ClienteID, req.CartaDesejadaID, req.CartaOferecida)
	if !aplicado {
		c.JSON(http.StatusBadRequest, gin.H{"status": "nao_aplicado"})
		return
	}
	log.Printf("[APLICAR_TROCA_LOCAL] Retornando inventário com %d cartas", len(inventario))

	// Notifica o cliente local com o inventário atualizado
	resp := protocolo.TrocarCartasResp{
		Sucesso:              true,
		Mensagem:             fmt.Sprintf("Troca realizada! Você deu '%s' e recebeu '%s'.", cartaRemovida.Nome, req.CartaOferecida.Nome),
		InventarioAtualizado: inventario,
	}
	s.servidor.PublicarParaCliente(req.ClienteID, protocolo.Mensagem{Comando: "TROCA_CONCLUIDA", Dados: seguranca.MustJSON(resp)})

	c.JSON(http.StatusOK, gin.H{"status": "ok", "inventario": inventario})
}

// handleBuscarCarta busca uma carta específica no inventário de um cliente
func (s *Server) handleBuscarCarta(c *gin.Context) {
	var req struct {
		ClienteID string `json:"cliente_id"`
		CartaID   string `json:"carta_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload inválido"})
		return
	}

	log.Printf("[BUSCAR_CARTA_RX] Buscando carta %s para cliente %s", req.CartaID, req.ClienteID)

	// Busca a carta usando a interface do servidor
	cartaEncontrada := s.servidor.BuscarCartaEmCliente(req.ClienteID, req.CartaID)

	if cartaEncontrada.ID == "" {
		log.Printf("[BUSCAR_CARTA] Carta %s não encontrada", req.CartaID)
		c.JSON(http.StatusOK, gin.H{"encontrada": false})
		return
	}

	log.Printf("[BUSCAR_CARTA] Carta %s encontrada: %s", req.CartaID, cartaEncontrada.Nome)
	c.JSON(http.StatusOK, gin.H{"encontrada": true, "carta": cartaEncontrada})
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
