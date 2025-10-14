package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"jogodistribuido/protocolo"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ==================== CONFIGURAÇÃO E CONSTANTES ====================

const (
	ELEICAO_TIMEOUT     = 10 * time.Second
	HEARTBEAT_INTERVALO = 3 * time.Second
	PACOTE_SIZE         = 5
)

// ==================== TIPOS ====================

type Carta = protocolo.Carta

// InfoServidor representa informações sobre um servidor no cluster
type InfoServidor struct {
	Endereco   string    `json:"endereco"`
	UltimoPing time.Time `json:"ultimo_ping"`
	Ativo      bool      `json:"ativo"`
}

// Cliente representa um jogador conectado via MQTT
type Cliente struct {
	ID         string
	Nome       string
	Inventario []Carta
	Sala       *Sala
	mutex      sync.Mutex
}

// Sala representa uma partida entre dois jogadores (possivelmente de servidores diferentes)
type Sala struct {
	ID             string
	Jogadores      []*Cliente
	Estado         string // "AGUARDANDO_COMPRA" | "JOGANDO" | "FINALIZADO"
	CartasNaMesa   map[string]Carta
	PontosRodada   map[string]int
	PontosPartida  map[string]int
	NumeroRodada   int
	Prontos        map[string]bool
	ServidorHost   string // Servidor responsável pela lógica da partida
	ServidorSombra string // Servidor backup
	mutex          sync.Mutex
}

// EstadoPartida representa o estado completo de uma partida (para replicação)
type EstadoPartida struct {
	SalaID        string           `json:"sala_id"`
	Estado        string           `json:"estado"`
	CartasNaMesa  map[string]Carta `json:"cartas_na_mesa"`
	PontosRodada  map[string]int   `json:"pontos_rodada"`
	PontosPartida map[string]int   `json:"pontos_partida"`
	NumeroRodada  int              `json:"numero_rodada"`
	Prontos       map[string]bool  `json:"prontos"`
}

// Servidor é a estrutura principal que gerencia o servidor distribuído
type Servidor struct {
	MeuEndereco     string
	MeuEnderecoHTTP string
	BrokerMQTT      string
	MQTTClient      mqtt.Client

	// Gerenciamento de Cluster
	Servidores      map[string]*InfoServidor
	mutexServidores sync.RWMutex

	// Eleição de Líder (Guardião do Estoque)
	SouLider        bool
	LiderAtual      string
	TermoAtual      int64
	UltimoHeartbeat time.Time
	mutexLider      sync.RWMutex

	// Estoque de Cartas (apenas o líder gerencia diretamente)
	Estoque      map[string][]Carta // raridade -> cartas
	mutexEstoque sync.Mutex

	// Clientes e Salas
	Clientes      map[string]*Cliente // clienteID -> Cliente
	mutexClientes sync.RWMutex
	Salas         map[string]*Sala // salaID -> Sala
	mutexSalas    sync.RWMutex
	FilaDeEspera  []*Cliente
	mutexFila     sync.Mutex
}

// ==================== INICIALIZAÇÃO ====================

var (
	meuEndereco        = flag.String("addr", "servidor1:8080", "Endereço deste servidor")
	brokerMQTT         = flag.String("broker", "tcp://broker1:1883", "Endereço do broker MQTT")
	servidoresIniciais = flag.String("peers", "", "Lista de peers separados por vírgula")
)

func main() {
	flag.Parse()

	log.Printf("Iniciando servidor em %s | Broker MQTT: %s", *meuEndereco, *brokerMQTT)

	servidor := novoServidor(*meuEndereco, *brokerMQTT)

	// Inicia processos concorrentes
	go servidor.iniciarAPI()
	go servidor.descobrirServidores() // Agora vai funcionar
	go servidor.processoEleicao()
	go servidor.enviarHeartbeats()

	log.Println("Servidor pronto e operacional")
	select {} // Mantém o programa rodando
}

func novoServidor(endereco, broker string) *Servidor {
	s := &Servidor{
		MeuEndereco:     endereco,
		BrokerMQTT:      broker,
		Servidores:      make(map[string]*InfoServidor),
		TermoAtual:      0,
		Estoque:         make(map[string][]Carta),
		Clientes:        make(map[string]*Cliente),
		Salas:           make(map[string]*Sala),
		FilaDeEspera:    make(chan *Cliente, 100), // Fila com buffer
		VotosRecebidos:  make(chan bool),
		PararEleicao:    make(chan bool),
		ComandosPartida: make(map[string]chan protocolo.Comando),
	}

	s.inicializarEstoque()

	if err := s.conectarMQTT(); err != nil {
		log.Fatalf("Erro fatal ao conectar ao MQTT: %v", err)
	}

	return s
}

// ==================== MQTT ====================

func (s *Servidor) conectarMQTT() error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(s.BrokerMQTT)
	opts.SetClientID("servidor_" + s.MeuEndereco)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)

	s.MQTTClient = mqtt.NewClient(opts)

	if token := s.MQTTClient.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}

	log.Println("Conectado ao broker MQTT")

	// Subscreve a tópicos importantes
	s.subscreverTopicos()
	return nil
}

func (s *Servidor) handleClienteLogin(client mqtt.Client, msg mqtt.Message) {
	var dados protocolo.DadosLogin
	if err := json.Unmarshal(msg.Payload(), &dados); err != nil {
		log.Printf("Erro ao decodificar login: %v", err)
		return
	}

	clienteID := uuid.New().String()
	novoCliente := &Cliente{
		ID:         clienteID,
		Nome:       dados.Nome,
		Inventario: make([]Carta, 0),
	}

	s.mutexClientes.Lock()
	s.Clientes[clienteID] = novoCliente
	s.mutexClientes.Unlock()

	log.Printf("Cliente %s (%s) conectado", dados.Nome, clienteID)

	// Envia confirmação
	resposta := protocolo.Mensagem{
		Comando: "LOGIN_OK",
		Dados:   mustJSON(map[string]string{"cliente_id": clienteID, "servidor": s.MeuEndereco}),
	}
	s.publicarParaCliente(clienteID, resposta)
}

func (s *Servidor) handleClienteEntrarFila(client mqtt.Client, msg mqtt.Message) {
	var dados map[string]string
	if err := json.Unmarshal(msg.Payload(), &dados); err != nil {
		log.Printf("Erro ao decodificar entrada na fila: %v", err)
		return
	}

	clienteID := dados["cliente_id"]

	s.mutexClientes.RLock()
	cliente, existe := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()

	if !existe {
		log.Printf("Cliente %s não encontrado", clienteID)
		return
	}

	s.entrarFila(cliente)
}

func (s *Servidor) handleComandoPartida(client mqtt.Client, msg mqtt.Message) {
	// Extrai o ID da sala do tópico
	topico := msg.Topic()
	// topico formato: "partidas/{salaID}/comandos"
	partes := strings.Split(topico, "/")
	if len(partes) < 2 {
		log.Printf("Tópico inválido: %s", topico)
		return
	}
	salaID := partes[1]

	var mensagem protocolo.Mensagem
	if err := json.Unmarshal(msg.Payload(), &mensagem); err != nil {
		log.Printf("Erro ao decodificar comando: %v", err)
		return
	}

	s.mutexSalas.RLock()
	sala, existe := s.Salas[salaID]
	s.mutexSalas.RUnlock()

	if !existe {
		log.Printf("Sala %s não encontrada", salaID)
		return
	}

	// Verifica se este servidor é o Host ou Sombra
	sala.mutex.Lock()
	servidorHost := sala.ServidorHost
	servidorSombra := sala.ServidorSombra
	sala.mutex.Unlock()

	// Processa comando baseado no tipo
	switch mensagem.Comando {
	case "COMPRAR_PACOTE":
		var dados map[string]string
		json.Unmarshal(mensagem.Dados, &dados)
		clienteID := dados["cliente_id"]

		// Sempre processa compra localmente
		s.processarCompraPacote(clienteID, sala)

	case "JOGAR_CARTA":
		var dados map[string]interface{}
		json.Unmarshal(mensagem.Dados, &dados)
		clienteID := dados["cliente_id"].(string)
		cartaID := dados["carta_id"].(string)

		// Se este servidor é o Host, processa diretamente
		if servidorHost == s.MeuEndereco {
			s.processarJogadaComoHost(sala, clienteID, cartaID)
		} else if servidorSombra == s.MeuEndereco {
			// Se é a Sombra, encaminha para o Host via API REST
			s.encaminharJogadaParaHost(sala, clienteID, cartaID)
		}

	case "ENVIAR_CHAT":
		var dados map[string]string
		json.Unmarshal(mensagem.Dados, &dados)
		texto := dados["texto"]
		clienteID := dados["cliente_id"]

		// Encontra o nome do jogador
		var nomeRemetente string
		s.mutexClientes.RLock()
		if cliente, existe := s.Clientes[clienteID]; existe {
			nomeRemetente = cliente.Nome
		}
		s.mutexClientes.RUnlock()

		s.broadcastChat(sala, texto, nomeRemetente)

	case "TROCAR_CARTAS_OFERTA":
		var req protocolo.TrocarCartasReq
		if err := json.Unmarshal(mensagem.Dados, &req); err == nil {
			s.processarTrocaCartas(sala, &req)
		}
	}
}

func (s *Servidor) publicarParaCliente(clienteID string, msg protocolo.Mensagem) {
	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("clientes/%s/eventos", clienteID)
	s.MQTTClient.Publish(topico, 0, false, payload)
}

func (s *Servidor) publicarEventoPartida(salaID string, msg protocolo.Mensagem) {
	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("partidas/%s/eventos", salaID)
	s.MQTTClient.Publish(topico, 0, false, payload)
}

// ==================== API REST ====================

func (s *Servidor) iniciarAPI() {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// Rotas de descoberta e heartbeat
	router.POST("/register", s.handleRegister)
	router.POST("/heartbeat", s.handleHeartbeat)
	router.GET("/servers", s.handleGetServers)

	// Rotas de eleição de líder
	router.POST("/eleicao/solicitar_voto", s.handleSolicitarVoto)
	router.POST("/eleicao/declarar_lider", s.handleDeclararLider)

	// Rotas de gerenciamento de estoque (apenas líder)
	router.POST("/estoque/comprar_pacote", s.handleComprarPacote)
	router.GET("/estoque/status", s.handleStatusEstoque)

	// Rotas de sincronização de partidas
	router.POST("/partida/encaminhar_comando", s.handleEncaminharComando)
	router.POST("/partida/sincronizar_estado", s.handleSincronizarEstado)
	router.POST("/partida/notificar_jogador", s.handleNotificarJogador)

	// Rotas de matchmaking global
	router.POST("/matchmaking/solicitar_oponente", s.handleSolicitarOponente)
	router.POST("/matchmaking/confirmar_partida", s.handleConfirmarPartida)

	log.Printf("API REST iniciada em %s", s.MeuEndereco)
	if err := router.Run(s.MeuEndereco); err != nil {
		log.Fatalf("Erro ao iniciar API: %v", err)
	}
}

// Handlers de descoberta
func (s *Servidor) handleRegister(c *gin.Context) {
	var novoServidor InfoServidor
	if err := c.ShouldBindJSON(&novoServidor); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.mutexServidores.Lock()
	novoServidor.UltimoPing = time.Now()
	novoServidor.Ativo = true
	s.Servidores[novoServidor.Endereco] = &novoServidor
	s.mutexServidores.Unlock()

	log.Printf("Servidor registrado: %s", novoServidor.Endereco)

	// Retorna lista de todos os servidores conhecidos
	s.mutexServidores.RLock()
	defer s.mutexServidores.RUnlock()
	c.JSON(http.StatusOK, s.Servidores)
}

func (s *Servidor) handleHeartbeat(c *gin.Context) {
	var dados map[string]interface{}
	if err := c.ShouldBindJSON(&dados); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	endereco := dados["endereco"].(string)

	s.mutexServidores.Lock()
	if servidor, existe := s.Servidores[endereco]; existe {
		servidor.UltimoPing = time.Now()
		servidor.Ativo = true
	} else {
		s.Servidores[endereco] = &InfoServidor{
			Endereco:   endereco,
			UltimoPing: time.Now(),
			Ativo:      true,
		}
	}

	// Atualiza informação do líder se recebida
	if lider, ok := dados["lider"].(string); ok {
		s.mutexLider.Lock()
		if lider != "" {
			s.LiderAtual = lider
			s.SouLider = (lider == s.MeuEndereco)
			s.UltimoHeartbeat = time.Now()
		}
		s.mutexLider.Unlock()
	}

	s.mutexServidores.Unlock()

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Servidor) handleGetServers(c *gin.Context) {
	s.mutexServidores.RLock()
	defer s.mutexServidores.RUnlock()
	c.JSON(http.StatusOK, s.Servidores)
}

// Handlers de eleição
func (s *Servidor) handleSolicitarVoto(c *gin.Context) {
	var dados map[string]interface{}
	if err := c.ShouldBindJSON(&dados); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	candidato := dados["candidato"].(string)
	termo := int64(dados["termo"].(float64))

	s.mutexLider.Lock()
	defer s.mutexLider.Unlock()

	// Vota no candidato se o termo for maior
	if termo > s.TermoAtual {
		s.TermoAtual = termo
		log.Printf("Votando em %s para termo %d", candidato, termo)
		c.JSON(http.StatusOK, gin.H{"voto_concedido": true, "termo": s.TermoAtual})
	} else {
		c.JSON(http.StatusOK, gin.H{"voto_concedido": false, "termo": s.TermoAtual})
	}
}

func (s *Servidor) handleDeclararLider(c *gin.Context) {
	var dados map[string]interface{}
	if err := c.ShouldBindJSON(&dados); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	novoLider := dados["lider"].(string)
	termo := int64(dados["termo"].(float64))

	s.mutexLider.Lock()
	if termo >= s.TermoAtual {
		s.TermoAtual = termo
		s.LiderAtual = novoLider
		s.SouLider = (novoLider == s.MeuEndereco)
		s.UltimoHeartbeat = time.Now()
		log.Printf("Novo líder reconhecido: %s (termo %d)", novoLider, termo)
	}
	s.mutexLider.Unlock()

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Handlers de estoque
func (s *Servidor) handleComprarPacote(c *gin.Context) {
	if !s.souLider() {
		s.encaminharParaLider(c)
		return
	}

	var req struct {
		ClienteID string `json:"cliente_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Requisição inválida"})
		return
	}

	log.Printf("[ESTOQUE] Recebido pedido de compra do cliente %s", req.ClienteID)

	s.mutexEstoque.Lock()
	defer s.mutexEstoque.Unlock()

	pacote, err := s.formarPacote()
	if err != nil {
		log.Printf("[ESTOQUE] FALHA na compra para %s: %v", req.ClienteID, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	// Aqui, o pacote seria enviado ao cliente via MQTT
	// e o inventário do cliente seria atualizado.
	// Por simplicidade, vamos apenas logar.

	s.notificarCompraSucesso(req.ClienteID, pacote)

	log.Printf("[ESTOQUE] SUCESSO na compra para %s. Novo estoque: C=%d, U=%d, R=%d, L=%d",
		req.ClienteID, len(s.Estoque["C"]), len(s.Estoque["U"]), len(s.Estoque["R"]), len(s.Estoque["L"]))

	c.JSON(http.StatusOK, gin.H{"pacote": pacote})
}

func (s *Servidor) handleStatusEstoque(c *gin.Context) {
	if !s.souLider() {
		s.encaminharParaLider(c)
		return
	}

	s.mutexEstoque.Lock()
	defer s.mutexEstoque.Unlock()

	status := make(map[string]int)
	for raridade, cartas := range s.Estoque {
		status[raridade] = len(cartas)
	}

	c.JSON(http.StatusOK, gin.H{
		"estoque": status,
		"total":   s.contarEstoque(),
		"lider":   s.SouLider,
	})
}

// Handlers de partida
func (s *Servidor) handleEncaminharComando(c *gin.Context) {
	var dados map[string]interface{}
	if err := c.ShouldBindJSON(&dados); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	salaID := dados["sala_id"].(string)
	comando := dados["comando"].(string)

	s.mutexSalas.RLock()
	sala, existe := s.Salas[salaID]
	s.mutexSalas.RUnlock()

	if !existe {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sala não encontrada"})
		return
	}

	// Verifica se este servidor é o host
	sala.mutex.Lock()
	eHost := sala.ServidorHost == s.MeuEndereco
	sala.mutex.Unlock()

	if !eHost {
		c.JSON(http.StatusForbidden, gin.H{"error": "Este servidor não é o host da partida"})
		return
	}

	// Processa o comando baseado no tipo
	switch comando {
	case "JOGAR_CARTA":
		clienteID := dados["cliente_id"].(string)
		cartaID := dados["carta_id"].(string)

		// Processa a jogada como Host
		estado := s.processarJogadaComoHost(sala, clienteID, cartaID)

		c.JSON(http.StatusOK, gin.H{
			"status": "processado",
			"estado": estado,
		})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Comando não reconhecido"})
	}
}

func (s *Servidor) handleSincronizarEstado(c *gin.Context) {
	var estado EstadoPartida
	if err := c.ShouldBindJSON(&estado); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.mutexSalas.Lock()
	defer s.mutexSalas.Unlock()

	// Atualiza ou cria a sala com o estado recebido
	if sala, existe := s.Salas[estado.SalaID]; existe {
		sala.Estado = estado.Estado
		sala.CartasNaMesa = estado.CartasNaMesa
		sala.PontosRodada = estado.PontosRodada
		sala.PontosPartida = estado.PontosPartida
		sala.NumeroRodada = estado.NumeroRodada
		sala.Prontos = estado.Prontos
		log.Printf("Estado da sala %s sincronizado", estado.SalaID)
	}

	c.JSON(http.StatusOK, gin.H{"status": "sincronizado"})
}

func (s *Servidor) handleNotificarJogador(c *gin.Context) {
	var dados map[string]interface{}
	if err := c.ShouldBindJSON(&dados); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	clienteID := dados["cliente_id"].(string)
	mensagemJSON := dados["mensagem"].(map[string]interface{})

	var msg protocolo.Mensagem
	msgBytes, _ := json.Marshal(mensagemJSON)
	json.Unmarshal(msgBytes, &msg)

	// Publica a mensagem para o cliente via MQTT
	s.publicarParaCliente(clienteID, msg)

	c.JSON(http.StatusOK, gin.H{"status": "notificado"})
}

// ==================== HANDLERS DE MATCHMAKING GLOBAL ====================

// handleSolicitarOponente é chamado por outro servidor procurando um oponente.
func (s *Servidor) handleSolicitarOponente(c *gin.Context) {
	var req struct {
		SolicitanteID   string `json:"solicitante_id"`
		SolicitanteNome string `json:"solicitante_nome"`
		ServidorOrigem  string `json:"servidor_origem"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("[MATCHMAKING-RX] Recebida solicitação de oponente de %s@%s", req.SolicitanteNome, req.ServidorOrigem)

	s.mutexFila.Lock()
	if len(s.FilaDeEspera) > 0 {
		// Oponente encontrado!
		oponenteLocal := s.FilaDeEspera[0]
		s.FilaDeEspera = s.FilaDeEspera[1:] // Remove da fila
		s.mutexFila.Unlock()

		log.Printf("[MATCHMAKING-RX] Oponente local %s encontrado para %s@%s", oponenteLocal.Nome, req.SolicitanteNome, req.ServidorOrigem)

		// Este servidor (que encontrou o oponente) será o Host.
		salaID := uuid.New().String()
		novaSala := &Sala{
			ID:             salaID,
			Jogadores:      []*Cliente{oponenteLocal}, // Adiciona jogador local
			Estado:         "AGUARDANDO_JOGADORES",
			CartasNaMesa:   make(map[string]Carta),
			PontosRodada:   make(map[string]int),
			PontosPartida:  make(map[string]int),
			NumeroRodada:   1,
			Prontos:        make(map[string]bool),
			ServidorHost:   s.MeuEndereco,      // Eu sou o Host
			ServidorSombra: req.ServidorOrigem, // O outro servidor é a Sombra
		}

		s.mutexSalas.Lock()
		s.Salas[salaID] = novaSala
		s.mutexSalas.Unlock()

		oponenteLocal.mutex.Lock()
		oponenteLocal.Sala = novaSala
		oponenteLocal.mutex.Unlock()

		// Responde ao servidor solicitante com os detalhes da partida
		c.JSON(http.StatusOK, gin.H{
			"partida_encontrada": true,
			"sala_id":            salaID,
			"oponente_nome":      oponenteLocal.Nome,
			"servidor_host":      s.MeuEndereco,
		})

		// Notifica o jogador local
		s.publicarParaCliente(oponenteLocal.ID, protocolo.Mensagem{
			Comando: "PARTIDA_ENCONTRADA",
			Dados: mustJSON(protocolo.DadosPartidaEncontrada{
				SalaID:       salaID,
				OponenteID:   oponenteLocal.ID, // Adiciona o ID do oponente
				OponenteNome: oponenteLocal.Nome,
			}),
		})

	} else {
		s.mutexFila.Unlock()
		log.Printf("[MATCHMAKING-RX] Nenhum oponente local na fila para %s@%s", req.SolicitanteNome, req.ServidorOrigem)
		c.JSON(http.StatusOK, gin.H{"partida_encontrada": false})
	}
}

// handleConfirmarPartida é chamado pelo servidor que iniciou o matchmaking para confirmar a participação.
func (s *Servidor) handleConfirmarPartida(c *gin.Context) {
	var req struct {
		SalaID          string `json:"sala_id"`
		SolicitanteID   string `json:"solicitante_id"`
		SolicitanteNome string `json:"solicitante_nome"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("[MATCHMAKING-CONFIRM] Recebida confirmação de %s para a sala %s", req.SolicitanteNome, req.SalaID)

	s.mutexSalas.Lock()
	sala, existe := s.Salas[req.SalaID]
	s.mutexSalas.Unlock()

	if !existe {
		c.JSON(http.StatusNotFound, gin.H{"error": "sala não encontrada"})
		return
	}

	// Cria uma representação local para o jogador remoto
	jogadorRemoto := &Cliente{
		ID:   req.SolicitanteID,
		Nome: req.SolicitanteNome,
		Sala: sala,
	}

	s.mutexClientes.Lock()
	s.Clientes[jogadorRemoto.ID] = jogadorRemoto
	s.mutexClientes.Unlock()

	sala.mutex.Lock()
	sala.Jogadores = append(sala.Jogadores, jogadorRemoto)
	sala.Estado = "AGUARDANDO_COMPRA"
	sala.mutex.Unlock()

	log.Printf("[MATCHMAKING-CONFIRM] Jogador remoto %s adicionado à sala %s. A partida pode começar.", req.SolicitanteNome, req.SalaID)

	c.JSON(http.StatusOK, gin.H{"status": "confirmado"})
}

// ==================== LÓGICA DE ELEIÇÃO DE LÍDER ====================

func (s *Servidor) processoEleicao() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mutexLider.RLock()
		tempoSemLider := time.Since(s.UltimoHeartbeat)
		souLider := s.SouLider
		s.mutexLider.RUnlock()

		// Se não há líder há muito tempo, inicia eleição
		if tempoSemLider > ELEICAO_TIMEOUT && !souLider {
			log.Println("Iniciando processo de eleição")
			s.iniciarEleicao()
		}
	}
}

func (s *Servidor) iniciarEleicao() {
	s.mutexLider.Lock()
	s.TermoAtual++
	termoAtual := s.TermoAtual
	s.mutexLider.Unlock()

	log.Printf("Candidatando-se ao termo %d", termoAtual)

	// Solicita votos de todos os servidores
	votos := 1 // Vota em si mesmo
	totalServidores := 1

	s.mutexServidores.RLock()
	servidores := make([]string, 0, len(s.Servidores))
	for addr := range s.Servidores {
		if addr != s.MeuEndereco {
			servidores = append(servidores, addr)
		}
	}
	totalServidores += len(servidores)
	s.mutexServidores.RUnlock()

	// Envia solicitações de voto em paralelo
	var wg sync.WaitGroup
	var mutexVotos sync.Mutex

	for _, servidor := range servidores {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()

			dados := map[string]interface{}{
				"candidato": s.MeuEndereco,
				"termo":     termoAtual,
			}

			jsonData, _ := json.Marshal(dados)
			url := fmt.Sprintf("http://%s/eleicao/solicitar_voto", addr)

			resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var resultado map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&resultado); err != nil {
				return
			}

			if votoConcedido, ok := resultado["voto_concedido"].(bool); ok && votoConcedido {
				mutexVotos.Lock()
				votos++
				mutexVotos.Unlock()
			}
		}(servidor)
	}

	wg.Wait()

	// Verifica se ganhou a eleição (maioria)
	if votos > totalServidores/2 {
		log.Printf("Eleição ganha! Votos: %d/%d", votos, totalServidores)
		s.tornarLider(termoAtual)
	} else {
		log.Printf("Eleição perdida. Votos: %d/%d", votos, totalServidores)
	}
}

func (s *Servidor) tornarLider(termo int64) {
	s.mutexLider.Lock()
	s.SouLider = true
	s.LiderAtual = s.MeuEndereco
	s.TermoAtual = termo
	s.UltimoHeartbeat = time.Now()
	s.mutexLider.Unlock()

	log.Printf("Agora sou o líder! Termo: %d", termo)

	// Anuncia liderança para todos os servidores
	s.mutexServidores.RLock()
	servidores := make([]string, 0, len(s.Servidores))
	for addr := range s.Servidores {
		if addr != s.MeuEndereco {
			servidores = append(servidores, addr)
		}
	}
	s.mutexServidores.RUnlock()

	for _, servidor := range servidores {
		go func(addr string) {
			dados := map[string]interface{}{
				"lider": s.MeuEndereco,
				"termo": termo,
			}

			jsonData, _ := json.Marshal(dados)
			url := fmt.Sprintf("http://%s/eleicao/declarar_lider", addr)
			http.Post(url, "application/json", bytes.NewBuffer(jsonData))
		}(servidor)
	}
}

func (s *Servidor) enviarHeartbeats() {
	ticker := time.NewTicker(HEARTBEAT_INTERVALO)
	defer ticker.Stop()

	for range ticker.C {
		s.mutexLider.RLock()
		souLider := s.SouLider
		lider := s.LiderAtual
		s.mutexLider.RUnlock()

		s.mutexServidores.RLock()
		servidores := make([]string, 0, len(s.Servidores))
		for addr := range s.Servidores {
			if addr != s.MeuEndereco {
				servidores = append(servidores, addr)
			}
		}
		s.mutexServidores.RUnlock()

		// Envia heartbeat para todos os servidores
		for _, servidor := range servidores {
			go func(addr string) {
				dados := map[string]interface{}{
					"endereco": s.MeuEndereco,
					"lider":    lider,
				}

				if souLider {
					dados["lider"] = s.MeuEndereco
				}

				jsonData, _ := json.Marshal(dados)
				url := fmt.Sprintf("http://%s/heartbeat", addr)
				http.Post(url, "application/json", bytes.NewBuffer(jsonData))
			}(servidor)
		}
	}
}

// ==================== DESCOBERTA DE SERVIDORES ====================

func (s *Servidor) descobrirServidores() {
	// Adiciona a si mesmo à lista
	s.mutexServidores.Lock()
	s.Servidores[s.MeuEndereco] = &InfoServidor{
		Endereco:   s.MeuEndereco,
		UltimoPing: time.Now(),
		Ativo:      true,
	}
	s.mutexServidores.Unlock()

	// Lê a variável de ambiente PEERS
	peersStr := os.Getenv("PEERS")
	if peersStr == "" {
		log.Println("[Cluster] Nenhuma variável PEERS encontrada. Operando em modo standalone.")
		return
	}

	peers := strings.Split(peersStr, ",")
	log.Printf("[Cluster] Peers para descoberta: %v", peers)

	for _, peerAddr := range peers {
		if peerAddr != s.MeuEndereco {
			// Tenta se registrar com cada peer em uma goroutine
			go s.registrarComPeer(peerAddr)
		}
	}

	// Verifica periodicamente servidores inativos
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mutexServidores.Lock()
		for addr, servidor := range s.Servidores {
			if addr != s.MeuEndereco && time.Since(servidor.UltimoPing) > 15*time.Second {
				servidor.Ativo = false
				log.Printf("Servidor %s marcado como inativo", addr)
			}
		}
		s.mutexServidores.Unlock()
	}
}

// registrarComPeer tenta se registrar com um peer até ter sucesso.
func (s *Servidor) registrarComPeer(peerAddr string) {
	ticker := time.NewTicker(5 * time.Second) // Tenta a cada 5 segundos
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			log.Printf("[Cluster] Tentando registrar com o peer %s...", peerAddr)
			if s.enviarRegistro(peerAddr) {
				log.Printf("[Cluster] Registro com %s bem-sucedido!", peerAddr)
				return // Sucesso, para de tentar
			}
			log.Printf("[Cluster] Falha ao registrar com %s, tentando novamente...", peerAddr)
		}
	}
}

// enviarRegistro envia uma requisição POST para o endpoint /register de um peer.
func (s *Servidor) enviarRegistro(peerAddr string) bool {
	url := fmt.Sprintf("http://%s/register", peerAddr)
	meuInfo := InfoServidor{
		Endereco:   s.MeuEndereco,
		UltimoPing: time.Now(),
		Ativo:      true,
	}
	jsonData, err := json.Marshal(meuInfo)
	if err != nil {
		return false
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// Atualiza a lista de servidores com a resposta do peer
		var servidoresRecebidos map[string]*InfoServidor
		if err := json.NewDecoder(resp.Body).Decode(&servidoresRecebidos); err == nil {
			s.mutexServidores.Lock()
			for addr, info := range servidoresRecebidos {
				if _, existe := s.Servidores[addr]; !existe {
					s.Servidores[addr] = info
					log.Printf("[Cluster] Peer %s descoberto através de %s", addr, peerAddr)
				}
			}
			s.mutexServidores.Unlock()
		}
		return true
	}
	return false
}

// ==================== GERENCIAMENTO DE ESTOQUE ====================

func (s *Servidor) inicializarEstoque() {
	s.mutexEstoque.Lock()
	defer s.mutexEstoque.Unlock()

	s.Estoque = map[string][]Carta{
		"C": make([]Carta, 0),
		"U": make([]Carta, 0),
		"R": make([]Carta, 0),
		"L": make([]Carta, 0),
	}

	// Gera estoque inicial de cartas
	tiposCartas := []string{
		"Dragão", "Guerreiro", "Mago", "Anjo", "Demônio", "Fênix", "Titan", "Sereia",
		"Lobo", "Águia", "Leão", "Tigre", "Cavaleiro", "Arqueiro", "Bárbaro", "Paladino",
	}
	naipes := []string{"♠", "♥", "♦", "♣"}

	for _, nome := range tiposCartas {
		// Comuns: 100 por tipo
		for i := 0; i < 100; i++ {
			s.Estoque["C"] = append(s.Estoque["C"], Carta{
				ID:       uuid.New().String(),
				Nome:     nome,
				Naipe:    naipes[rand.Intn(len(naipes))],
				Valor:    1 + rand.Intn(50),
				Raridade: "C",
			})
		}

		// Incomuns: 50 por tipo
		for i := 0; i < 50; i++ {
			s.Estoque["U"] = append(s.Estoque["U"], Carta{
				ID:       uuid.New().String(),
				Nome:     nome,
				Naipe:    naipes[rand.Intn(len(naipes))],
				Valor:    51 + rand.Intn(30),
				Raridade: "U",
			})
		}

		// Raras: 20 por tipo
		for i := 0; i < 20; i++ {
			s.Estoque["R"] = append(s.Estoque["R"], Carta{
				ID:       uuid.New().String(),
				Nome:     nome,
				Naipe:    naipes[rand.Intn(len(naipes))],
				Valor:    81 + rand.Intn(20),
				Raridade: "R",
			})
		}

		// Lendárias: 5 por tipo
		for i := 0; i < 5; i++ {
			s.Estoque["L"] = append(s.Estoque["L"], Carta{
				ID:       uuid.New().String(),
				Nome:     nome,
				Naipe:    naipes[rand.Intn(len(naipes))],
				Valor:    101 + rand.Intn(20),
				Raridade: "L",
			})
		}
	}

	log.Printf("Estoque inicializado: C=%d, U=%d, R=%d, L=%d",
		len(s.Estoque["C"]), len(s.Estoque["U"]), len(s.Estoque["R"]), len(s.Estoque["L"]))
}

func (s *Servidor) retirarCartasDoEstoque(quantidade int) []Carta {
	s.mutexEstoque.Lock()
	defer s.mutexEstoque.Unlock()

	cartas := make([]Carta, 0, quantidade)

	for i := 0; i < quantidade; i++ {
		raridade := sampleRaridade()

		// Tenta encontrar carta da raridade desejada com downgrade
		ordem := []string{"L", "R", "U", "C"}
		var start int
		switch raridade {
		case "L":
			start = 0
		case "R":
			start = 1
		case "U":
			start = 2
		default:
			start = 3
		}

		var carta Carta
		encontrou := false
		for j := start; j < len(ordem); j++ {
			r := ordem[j]
			if len(s.Estoque[r]) > 0 {
				// Remove a última carta
				idx := len(s.Estoque[r]) - 1
				carta = s.Estoque[r][idx]
				s.Estoque[r] = s.Estoque[r][:idx]
				encontrou = true
				break
			}
		}

		if !encontrou {
			// Gera carta comum básica se estoque acabou
			carta = s.gerarCartaComum()
		}

		cartas = append(cartas, carta)
	}

	return cartas
}

func (s *Servidor) gerarCartaComum() Carta {
	nomes := []string{"Guerreiro", "Arqueiro", "Mago", "Cavaleiro", "Ladrão"}
	naipes := []string{"♠", "♥", "♦", "♣"}

	return Carta{
		ID:       uuid.New().String(),
		Nome:     nomes[rand.Intn(len(nomes))],
		Naipe:    naipes[rand.Intn(len(naipes))],
		Valor:    1 + rand.Intn(50),
		Raridade: "C",
	}
}

func (s *Servidor) contarEstoque() int {
	total := 0
	for _, cartas := range s.Estoque {
		total += len(cartas)
	}
	return total
}

func sampleRaridade() string {
	x := rand.Intn(100)
	if x < 70 {
		return "C"
	}
	if x < 90 {
		return "U"
	}
	if x < 99 {
		return "R"
	}
	return "L"
}

// ==================== MATCHMAKING E LÓGICA DE JOGO ====================

func (s *Servidor) entrarFila(cliente *Cliente) {
	s.mutexFila.Lock()
	// Tenta encontrar oponente na fila local primeiro
	if len(s.FilaDeEspera) > 0 {
		oponente := s.FilaDeEspera[0]
		s.FilaDeEspera = s.FilaDeEspera[1:]
		s.mutexFila.Unlock()

		// Cria sala localmente
		s.criarSala(oponente, cliente)
		return
	}

	// Se não encontrou, adiciona à fila local e tenta matchmaking global
	s.FilaDeEspera = append(s.FilaDeEspera, cliente)
	s.mutexFila.Unlock()

	log.Printf("Cliente %s (%s) entrou na fila de espera.", cliente.Nome, cliente.ID)
	s.publicarParaCliente(cliente.ID, protocolo.Mensagem{
		Comando: "AGUARDANDO_OPONENTE",
		Dados:   mustJSON(map[string]string{"mensagem": "Procurando oponente em todos os servidores..."}),
	})

	// Dispara a busca por oponente em outros servidores em uma goroutine
	go s.tentarMatchmakingGlobal(cliente)
}

// tentarMatchmakingGlobal é a rotina que busca oponentes em outros servidores.
func (s *Servidor) tentarMatchmakingGlobal(cliente *Cliente) {
	// Delay para dar chance ao matchmaking local de outros servidores
	time.Sleep(2 * time.Second)

	// Garante que o cliente não encontrou uma partida enquanto esperava
	s.mutexFila.Lock()
	aindaNaFila := false
	for i, c := range s.FilaDeEspera {
		if c.ID == cliente.ID {
			// Remove temporariamente para não ser encontrado por outro processo
			s.FilaDeEspera = append(s.FilaDeEspera[:i], s.FilaDeEspera[i+1:]...)
			aindaNaFila = true
			break
		}
	}
	s.mutexFila.Unlock()

	if !aindaNaFila {
		log.Printf("[MATCHMAKING-TX] Cliente %s já encontrou partida, cancelando busca global.", cliente.Nome)
		return
	}

	log.Printf("[MATCHMAKING-TX] Iniciando busca global para %s", cliente.Nome)

	// Busca oponente em outros servidores
	s.mutexServidores.RLock()
	servidoresAtivos := make([]string, 0, len(s.Servidores))
	for addr, info := range s.Servidores {
		if addr != s.MeuEndereco && info.Ativo {
			servidoresAtivos = append(servidoresAtivos, addr)
		}
	}
	s.mutexServidores.RUnlock()

	for _, addr := range servidoresAtivos {
		if s.realizarSolicitacaoMatchmaking(addr, cliente) {
			// Encontrou partida, a função interna já tratou de tudo.
			return
		}
	}

	// Se chegou aqui, não encontrou oponente. Coloca o cliente de volta na fila.
	log.Printf("[MATCHMAKING-TX] Nenhum oponente encontrado globalmente para %s. Devolvendo à fila.", cliente.Nome)
	s.mutexFila.Lock()
	s.FilaDeEspera = append(s.FilaDeEspera, cliente)
	s.mutexFila.Unlock()
}

// realizarSolicitacaoMatchmaking envia uma requisição de oponente para um servidor específico.
func (s *Servidor) realizarSolicitacaoMatchmaking(addr string, cliente *Cliente) bool {
	log.Printf("[MATCHMAKING-TX] Enviando solicitação para %s", addr)
	reqBody, _ := json.Marshal(map[string]string{
		"solicitante_id":   cliente.ID,
		"solicitante_nome": cliente.Nome,
		"servidor_origem":  s.MeuEndereco,
	})

	httpClient := &http.Client{Timeout: 3 * time.Second}
	resp, err := httpClient.Post(fmt.Sprintf("http://%s/matchmaking/solicitar_oponente", addr), "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Printf("[MATCHMAKING-TX] Erro ao contatar servidor %s: %v", addr, err)
		return false
	}
	defer resp.Body.Close()

	var res struct {
		PartidaEncontrada bool   `json:"partida_encontrada"`
		SalaID            string `json:"sala_id"`
		OponenteNome      string `json:"oponente_nome"`
		ServidorHost      string `json:"servidor_host"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		log.Printf("[MATCHMAKING-TX] Erro ao decodificar resposta de %s: %v", addr, err)
		return false
	}

	if res.PartidaEncontrada {
		log.Printf("[MATCHMAKING-TX] Partida encontrada no servidor %s! Sala: %s, Oponente: %s", addr, res.SalaID, res.OponenteNome)

		// Cria a sala como Sombra neste servidor.
		novaSala := &Sala{
			ID:             res.SalaID,
			Jogadores:      []*Cliente{cliente},
			Estado:         "AGUARDANDO_COMPRA",
			CartasNaMesa:   make(map[string]Carta),
			PontosRodada:   make(map[string]int),
			PontosPartida:  make(map[string]int),
			NumeroRodada:   1,
			Prontos:        make(map[string]bool),
			ServidorHost:   res.ServidorHost,
			ServidorSombra: s.MeuEndereco,
		}

		s.mutexSalas.Lock()
		s.Salas[res.SalaID] = novaSala
		s.mutexSalas.Unlock()

		cliente.mutex.Lock()
		cliente.Sala = novaSala
		cliente.mutex.Unlock()

		// Notifica o servidor Host para confirmar a participação do nosso jogador.
		confirmBody, _ := json.Marshal(map[string]string{
			"sala_id":          res.SalaID,
			"solicitante_id":   cliente.ID,
			"solicitante_nome": cliente.Nome,
		})
		http.Post(fmt.Sprintf("http://%s/matchmaking/confirmar_partida", res.ServidorHost), "application/json", bytes.NewBuffer(confirmBody))

		// Notifica nosso cliente local.
		s.publicarParaCliente(cliente.ID, protocolo.Mensagem{
			Comando: "PARTIDA_ENCONTRADA",
			Dados:   mustJSON(protocolo.DadosPartidaEncontrada{SalaID: res.SalaID, OponenteID: "oponente_remoto", OponenteNome: res.OponenteNome}), // O ID exato do oponente não é conhecido aqui, mas o cliente precisa de um valor.
		})
		return true
	}

	log.Printf("[MATCHMAKING-TX] Servidor %s não encontrou oponente.", addr)
	return false
}

func (s *Servidor) criarSala(j1, j2 *Cliente) {
	salaID := uuid.New().String()

	novaSala := &Sala{
		ID:             salaID,
		Jogadores:      []*Cliente{j1, j2},
		Estado:         "AGUARDANDO_COMPRA",
		CartasNaMesa:   make(map[string]Carta),
		PontosRodada:   make(map[string]int),
		PontosPartida:  make(map[string]int),
		NumeroRodada:   1,
		Prontos:        make(map[string]bool),
		ServidorHost:   s.MeuEndereco, // Este servidor é o host
		ServidorSombra: "",            // TODO: determinar sombra se jogadores de servidores diferentes
	}

	s.mutexSalas.Lock()
	s.Salas[salaID] = novaSala
	s.mutexSalas.Unlock()

	j1.mutex.Lock()
	j1.Sala = novaSala
	j1.mutex.Unlock()

	j2.mutex.Lock()
	j2.Sala = novaSala
	j2.mutex.Unlock()

	log.Printf("Sala %s criada: %s vs %s", salaID, j1.Nome, j2.Nome)

	// Notifica jogadores
	msg1 := protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   mustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: j2.ID, OponenteNome: j2.Nome}),
	}
	msg2 := protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   mustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: j1.ID, OponenteNome: j1.Nome}),
	}

	s.publicarParaCliente(j1.ID, msg1)
	s.publicarParaCliente(j2.ID, msg2)
}

func (s *Servidor) processarCompraPacote(clienteID string, sala *Sala) {
	// Se não for o líder, faz requisição para o líder
	s.mutexLider.RLock()
	souLider := s.SouLider
	lider := s.LiderAtual
	s.mutexLider.RUnlock()

	var cartas []Carta

	if souLider {
		cartas = s.retirarCartasDoEstoque(PACOTE_SIZE)
	} else {
		// Faz requisição HTTP para o líder
		dados := map[string]interface{}{
			"quantidade": 1,
		}
		jsonData, _ := json.Marshal(dados)
		url := fmt.Sprintf("http://%s/estoque/comprar_pacote", lider)

		resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			log.Printf("Erro ao requisitar pacote do líder: %v", err)
			return
		}
		defer resp.Body.Close()

		var resultado protocolo.ComprarPacoteResp
		if err := json.NewDecoder(resp.Body).Decode(&resultado); err != nil {
			log.Printf("Erro ao decodificar resposta: %v", err)
			return
		}

		cartas = resultado.Cartas
	}

	// Adiciona cartas ao inventário do cliente
	s.mutexClientes.RLock()
	cliente := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()

	cliente.mutex.Lock()
	cliente.Inventario = append(cliente.Inventario, cartas...)
	cliente.mutex.Unlock()

	// Notifica cliente
	msg := protocolo.Mensagem{
		Comando: "PACOTE_RESULTADO",
		Dados:   mustJSON(protocolo.ComprarPacoteResp{Cartas: cartas}),
	}
	s.publicarParaCliente(clienteID, msg)

	// Marca como pronto na sala
	sala.mutex.Lock()
	sala.Prontos[cliente.Nome] = true
	prontos := len(sala.Prontos)
	total := len(sala.Jogadores)
	sala.mutex.Unlock()

	// Se ambos estão prontos, inicia partida
	if prontos == total {
		s.iniciarPartida(sala)
	}
}

func (s *Servidor) iniciarPartida(sala *Sala) {
	sala.mutex.Lock()
	sala.Estado = "JOGANDO"
	sala.mutex.Unlock()

	log.Printf("Partida %s iniciada", sala.ID)

	msg := protocolo.Mensagem{
		Comando: "PARTIDA_INICIADA",
		Dados:   mustJSON(map[string]string{"mensagem": "Partida iniciada! Jogue suas cartas."}),
	}
	s.publicarEventoPartida(sala.ID, msg)
}

// encaminharJogadaParaHost encaminha uma jogada da Sombra para o Host via API REST
func (s *Servidor) encaminharJogadaParaHost(sala *Sala, clienteID, cartaID string) {
	sala.mutex.Lock()
	host := sala.ServidorHost
	sala.mutex.Unlock()

	log.Printf("Encaminhando jogada de %s para o Host %s", clienteID, host)

	dados := map[string]interface{}{
		"sala_id":    sala.ID,
		"comando":    "JOGAR_CARTA",
		"cliente_id": clienteID,
		"carta_id":   cartaID,
	}

	jsonData, _ := json.Marshal(dados)
	url := fmt.Sprintf("http://%s/partida/encaminhar_comando", host)

	// Adiciona timeout para detectar falha do Host
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[FAILOVER] Host %s inacessível: %v. Iniciando promoção da Sombra...", host, err)
		s.promoverSombraAHost(sala)
		s.processarJogadaComoHost(sala, clienteID, cartaID) // Processa a jogada como o novo Host
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Host retornou status %d ao processar jogada", resp.StatusCode)
		return
	}

	log.Printf("Jogada processada pelo Host com sucesso")
}

// promoverSombraAHost promove a Sombra a Host quando o Host original falha
func (s *Servidor) promoverSombraAHost(sala *Sala) {
	sala.mutex.Lock()
	defer sala.mutex.Unlock()

	if sala.ServidorHost == s.MeuEndereco {
		return // Já sou o host, não fazer nada
	}

	antigoHost := sala.ServidorHost
	sala.ServidorHost = s.MeuEndereco
	sala.ServidorSombra = "" // Eu sou o novo Host

	log.Printf("[FAILOVER] Sombra promovida a Host para a sala %s. Antigo Host: %s", sala.ID, antigoHost)

	// Notifica jogadores da promoção
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: mustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: "O servidor da partida falhou. A partida continuará em um servidor reserva.",
		}),
	}
	s.publicarEventoPartida(sala.ID, msg)
}

// processarJogadaComoHost processa uma jogada quando este servidor é o Host
func (s *Servidor) processarJogadaComoHost(sala *Sala, clienteID, cartaID string) *EstadoPartida {
	sala.mutex.Lock()
	defer sala.mutex.Unlock()

	// Encontra o cliente
	var jogador *Cliente
	var nomeJogador string
	for _, c := range sala.Jogadores {
		if c.ID == clienteID {
			jogador = c
			nomeJogador = c.Nome
			break
		}
	}

	if jogador == nil {
		log.Printf("Cliente %s não encontrado na sala", clienteID)
		return nil
	}

	// Verifica se já jogou nesta rodada
	if _, jaJogou := sala.CartasNaMesa[nomeJogador]; jaJogou {
		log.Printf("Jogador %s já jogou nesta rodada", nomeJogador)
		return nil
	}

	// Encontra a carta no inventário
	jogador.mutex.Lock()
	var carta Carta
	cartaIndex := -1
	for i, c := range jogador.Inventario {
		if c.ID == cartaID {
			carta = c
			cartaIndex = i
			break
		}
	}

	if cartaIndex == -1 {
		jogador.mutex.Unlock()
		log.Printf("Carta %s não encontrada no inventário de %s", cartaID, nomeJogador)
		return nil
	}

	// Remove a carta do inventário
	jogador.Inventario = append(jogador.Inventario[:cartaIndex], jogador.Inventario[cartaIndex+1:]...)
	jogador.mutex.Unlock()

	// Adiciona carta à mesa
	sala.CartasNaMesa[nomeJogador] = carta

	log.Printf("Jogador %s jogou carta %s (Poder: %d)", nomeJogador, carta.Nome, carta.Valor)

	// Verifica se ambos jogaram
	if len(sala.CartasNaMesa) == len(sala.Jogadores) {
		// Resolve a jogada
		s.resolverJogada(sala)
	} else {
		// Notifica que está aguardando o oponente
		s.notificarAguardandoOponente(sala)
	}

	// Cria estado da partida para sincronização
	estado := &EstadoPartida{
		SalaID:        sala.ID,
		Estado:        sala.Estado,
		CartasNaMesa:  sala.CartasNaMesa,
		PontosRodada:  sala.PontosRodada,
		PontosPartida: sala.PontosPartida,
		NumeroRodada:  sala.NumeroRodada,
		Prontos:       sala.Prontos,
	}

	// Sincroniza com a Sombra
	if sala.ServidorSombra != "" && sala.ServidorSombra != s.MeuEndereco {
		go s.sincronizarEstadoComSombra(sala.ServidorSombra, estado)
	}

	return estado
}

// resolverJogada resolve uma jogada quando ambos os jogadores jogaram
func (s *Servidor) resolverJogada(sala *Sala) {
	if len(sala.Jogadores) != 2 {
		return
	}

	j1 := sala.Jogadores[0]
	j2 := sala.Jogadores[1]

	c1 := sala.CartasNaMesa[j1.Nome]
	c2 := sala.CartasNaMesa[j2.Nome]

	vencedorJogada := "EMPATE"
	var vencedor *Cliente

	resultado := compararCartas(c1, c2)
	if resultado > 0 {
		vencedorJogada = j1.Nome
		vencedor = j1
	} else if resultado < 0 {
		vencedorJogada = j2.Nome
		vencedor = j2
	}

	if vencedor != nil {
		sala.PontosRodada[vencedor.Nome]++
	}

	log.Printf("Resultado da jogada: %s venceu", vencedorJogada)

	// Limpa a mesa
	sala.CartasNaMesa = make(map[string]Carta)

	// Notifica jogadores
	s.notificarResultadoJogada(sala, vencedorJogada)

	// Verifica se acabaram as cartas
	j1.mutex.Lock()
	j1Cartas := len(j1.Inventario)
	j1.mutex.Unlock()

	j2.mutex.Lock()
	j2Cartas := len(j2.Inventario)
	j2.mutex.Unlock()

	if j1Cartas == 0 || j2Cartas == 0 {
		// Fim da partida
		s.finalizarPartida(sala)
	}
}

// compararCartas compara duas cartas e retorna o resultado
func compararCartas(c1, c2 Carta) int {
	if c1.Valor != c2.Valor {
		return c1.Valor - c2.Valor
	}

	// Desempate por naipe
	naipes := map[string]int{"♠": 4, "♥": 3, "♦": 2, "♣": 1}
	return naipes[c1.Naipe] - naipes[c2.Naipe]
}

// notificarAguardandoOponente notifica que está aguardando o oponente jogar
func (s *Servidor) notificarAguardandoOponente(sala *Sala) {
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: mustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: "Aguardando oponente jogar...",
			NumeroRodada:    sala.NumeroRodada,
			UltimaJogada:    make(map[string]Carta), // Esconde cartas até ambos jogarem
		}),
	}

	s.publicarEventoPartida(sala.ID, msg)
}

// notificarResultadoJogada notifica o resultado de uma jogada
func (s *Servidor) notificarResultadoJogada(sala *Sala, vencedorJogada string) {
	// Cria contagem de cartas
	contagemCartas := make(map[string]int)
	for _, j := range sala.Jogadores {
		j.mutex.Lock()
		contagemCartas[j.Nome] = len(j.Inventario)
		j.mutex.Unlock()
	}

	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: mustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Vencedor da jogada: %s", vencedorJogada),
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    sala.CartasNaMesa,
			VencedorJogada:  vencedorJogada,
			PontosRodada:    sala.PontosRodada,
			PontosPartida:   sala.PontosPartida,
		}),
	}

	s.publicarEventoPartida(sala.ID, msg)
}

// finalizarPartida finaliza uma partida e determina o vencedor
func (s *Servidor) finalizarPartida(sala *Sala) {
	sala.Estado = "FINALIZADO"

	vencedorFinal := "EMPATE"
	maxPontos := -1

	for nome, pontos := range sala.PontosRodada {
		if pontos > maxPontos {
			maxPontos = pontos
			vencedorFinal = nome
		} else if pontos == maxPontos {
			vencedorFinal = "EMPATE"
		}
	}

	log.Printf("Partida %s finalizada. Vencedor: %s", sala.ID, vencedorFinal)

	msg := protocolo.Mensagem{
		Comando: "FIM_DE_JOGO",
		Dados:   mustJSON(protocolo.DadosFimDeJogo{VencedorNome: vencedorFinal}),
	}

	s.publicarEventoPartida(sala.ID, msg)
}

// sincronizarEstadoComSombra envia o estado atualizado da partida para a Sombra
func (s *Servidor) sincronizarEstadoComSombra(sombra string, estado *EstadoPartida) {
	jsonData, _ := json.Marshal(estado)
	url := fmt.Sprintf("http://%s/partida/sincronizar_estado", sombra)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Erro ao sincronizar estado com Sombra %s: %v", sombra, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("Estado sincronizado com sucesso com Sombra %s", sombra)
	}
}

// broadcastChat envia uma mensagem de chat para todos na sala
func (s *Servidor) broadcastChat(sala *Sala, texto, remetente string) {
	msg := protocolo.Mensagem{
		Comando: "RECEBER_CHAT",
		Dados: mustJSON(protocolo.DadosReceberChat{
			NomeJogador: remetente,
			Texto:       texto,
		}),
	}

	s.publicarEventoPartida(sala.ID, msg)
}

// ==================== LÓGICA DE TROCA DE CARTAS ====================

func (s *Servidor) processarTrocaCartas(sala *Sala, req *protocolo.TrocarCartasReq) {
	log.Printf("[TROCA] Proposta recebida na sala %s", sala.ID)

	// Apenas o Host coordena a troca
	if sala.ServidorHost != s.MeuEndereco {
		// Lógica para encaminhar ao Host seria aqui, se necessário.
		return
	}

	jogadorOferta := s.getClienteDaSala(sala, req.IDJogadorOferta)
	jogadorDesejado := s.getClienteDaSala(sala, req.IDJogadorDesejado)

	if jogadorOferta == nil || jogadorDesejado == nil {
		s.notificarErro(req.IDJogadorOferta, "Um dos jogadores da troca não foi encontrado.")
		return
	}

	cartaOferta, idxOferta := s.findCartaNoInventario(jogadorOferta, req.IDCartaOferecida)
	cartaDesejada, idxDesejado := s.findCartaNoInventario(jogadorDesejado, req.IDCartaDesejada)

	if idxOferta == -1 {
		s.notificarErro(req.IDJogadorOferta, fmt.Sprintf("Você não tem a carta %s.", cartaOferta.Nome))
		return
	}
	if idxDesejado == -1 {
		s.notificarErro(req.IDJogadorOferta, fmt.Sprintf("%s não tem a carta %s.", jogadorDesejado.Nome, cartaDesejada.Nome))
		return
	}

	// Executa a troca
	jogadorOferta.mutex.Lock()
	jogadorDesejado.mutex.Lock()
	jogadorOferta.Inventario[idxOferta], jogadorDesejado.Inventario[idxDesejado] = jogadorDesejado.Inventario[idxDesejado], jogadorOferta.Inventario[idxOferta]
	jogadorDesejado.mutex.Unlock()
	jogadorOferta.mutex.Unlock()

	log.Printf("[TROCA] Troca realizada com sucesso entre %s e %s.", jogadorOferta.Nome, jogadorDesejado.Nome)

	// Notifica ambos os jogadores
	s.notificarSucessoTroca(jogadorOferta.ID, cartaOferta.Nome, cartaDesejada.Nome)
	s.notificarSucessoTroca(jogadorDesejado.ID, cartaDesejada.Nome, cartaOferta.Nome)

	// Sincroniza estado com a Sombra
	if sala.ServidorSombra != "" {
		estado := s.criarEstadoDaSala(sala)
		go s.sincronizarEstadoComSombra(sala.ServidorSombra, estado)
	}
}

func (s *Servidor) getClienteDaSala(sala *Sala, clienteID string) *Cliente {
	sala.mutex.Lock()
	defer sala.mutex.Unlock()
	for _, jogador := range sala.Jogadores {
		if jogador.ID == clienteID {
			return jogador
		}
	}
	return nil
}

func (s *Servidor) findCartaNoInventario(cliente *Cliente, cartaID string) (Carta, int) {
	cliente.mutex.Lock()
	defer cliente.mutex.Unlock()
	for i, c := range cliente.Inventario {
		if c.ID == cartaID {
			return c, i
		}
	}
	return Carta{}, -1
}

func (s *Servidor) notificarErro(clienteID string, mensagem string) {
	s.publicarParaCliente(clienteID, protocolo.Mensagem{
		Comando: "ERRO",
		Dados:   mustJSON(protocolo.DadosErro{Mensagem: mensagem}),
	})
}

func (s *Servidor) notificarSucessoTroca(clienteID, cartaPerdida, cartaGanha string) {
	msg := fmt.Sprintf("Troca realizada! Você deu '%s' e recebeu '%s'.", cartaPerdida, cartaGanha)
	resp := protocolo.TrocarCartasResp{Sucesso: true, Mensagem: msg}
	s.publicarParaCliente(clienteID, protocolo.Mensagem{Comando: "TROCA_CONCLUIDA", Dados: mustJSON(resp)})
}

func (s *Servidor) notificarJogadorRemoto(servidor string, clienteID string, msg protocolo.Mensagem) {
	log.Printf("[NOTIFICACAO-REMOTA] Notificando cliente %s no servidor %s", clienteID, servidor)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"cliente_id": clienteID,
		"mensagem":   msg,
	})
	httpClient := &http.Client{Timeout: 3 * time.Second}
	httpClient.Post(fmt.Sprintf("http://%s/partida/notificar_jogador", servidor), "application/json", bytes.NewBuffer(reqBody))
}

func (s *Servidor) getClienteLocal(clienteID string) *Cliente {
	s.mutexClientes.RLock()
	defer s.mutexClientes.RUnlock()
	// Esta função é um placeholder, a lógica real está em encontrar o cliente na sala.
	// Uma implementação melhor verificaria se o cliente está conectado a este broker.
	_, ok := s.Clientes[clienteID]
	if ok {
		return s.Clientes[clienteID]
	}
	return nil
}

func (s *Servidor) criarEstadoDaSala(sala *Sala) *EstadoPartida {
	sala.mutex.Lock()
	defer sala.mutex.Unlock()

	// Simplificado para apenas o necessário
	return &EstadoPartida{
		SalaID: sala.ID,
		Estado: sala.Estado,
	}
}

// ==================== UTILITÁRIOS ====================

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
