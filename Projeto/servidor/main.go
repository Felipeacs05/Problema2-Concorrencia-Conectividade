package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"jogodistribuido/protocolo"
	"jogodistribuido/servidor/api"
	"jogodistribuido/servidor/cluster"
	"jogodistribuido/servidor/game"
	mqttManager "jogodistribuido/servidor/mqtt"
	"jogodistribuido/servidor/seguranca"
	"jogodistribuido/servidor/store"
	"jogodistribuido/servidor/tipos"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

//Servidor ta ok :))
// ==================== CONFIGURAÇÃO E CONSTANTES ====================

const (
	ELEICAO_TIMEOUT     = 30 * time.Second // Aumentado para 30 segundos
	HEARTBEAT_INTERVALO = 5 * time.Second  // Aumentado para 5 segundos
	PACOTE_SIZE         = 5
	JWT_SECRET          = "jogo_distribuido_secret_key_2025" // Chave secreta compartilhada entre servidores
	JWT_EXPIRATION      = 24 * time.Hour                     // Tokens expiram em 24 horas
)

// ==================== TIPOS ====================

type Carta = protocolo.Carta

// Servidor é a estrutura principal que gerencia o servidor distribuído
type Servidor struct {
	ServerID        string
	MeuEndereco     string
	MeuEnderecoHTTP string
	BrokerMQTT      string
	MQTTClient      mqtt.Client
	ClusterManager  cluster.ClusterManagerInterface
	Store           store.StoreInterface
	GameManager     game.GameManagerInterface
	MQTTManager     mqttManager.MQTTManagerInterface

	// Gerenciamento de Partidas
	Clientes        map[string]*tipos.Cliente // clienteID -> Cliente
	mutexClientes   sync.RWMutex
	Salas           map[string]*tipos.Sala // salaID -> Sala
	mutexSalas      sync.RWMutex
	FilaDeEspera    []*tipos.Cliente
	mutexFila       sync.Mutex
	ComandosPartida map[string]chan protocolo.Comando
	mutexComandos   sync.Mutex
}

// ==================== INICIALIZAÇÃO ====================

var (
	meuEndereco        = flag.String("addr", "servidor1:8080", "Endereço deste servidor")
	brokerMQTT         = flag.String("broker", "tcp://broker1:1883", "Endereço do broker MQTT")
	servidoresIniciais = flag.String("peers", "", "Lista de peers separados por vírgula")
)

func main() {
	flag.Parse()
	servidor := novoServidor(*meuEndereco, *brokerMQTT)
	servidor.Run()
}

func (s *Servidor) Run() {
	// Inicializa gerador aleatório
	rand.Seed(time.Now().UnixNano())

	log.Printf("Iniciando servidor em %s | Broker MQTT: %s", s.MeuEndereco, s.BrokerMQTT)

	if err := s.conectarMQTT(); err != nil {
		log.Fatalf("Erro fatal ao conectar ao MQTT: %v", err)
	}

	// Inicia processos concorrentes
	// O ClusterManager é iniciado primeiro para que a descoberta comece imediatamente
	s.ClusterManager.Run()
	go s.tentarMatchmakingGlobalPeriodicamente() // Inicia a busca proativa

	// A API Server agora recebe o servidor e o cluster manager
	apiServer := api.NewServer(s.MeuEndereco, s, s.ClusterManager)
	go apiServer.Run()

	log.Println("Servidor pronto e operacional")
	select {} // Mantém o programa rodando
}

// Interface methods for managers
func (s *Servidor) GetClientes() map[string]*tipos.Cliente {
	return s.Clientes
}

func (s *Servidor) GetSalas() map[string]*tipos.Sala {
	return s.Salas
}

func (s *Servidor) GetFilaDeEspera() []*tipos.Cliente {
	return s.FilaDeEspera
}

func (s *Servidor) GetComandosPartida() map[string]chan protocolo.Comando {
	return s.ComandosPartida
}

func (s *Servidor) GetMeuEndereco() string {
	return s.MeuEndereco
}

func (s *Servidor) GetMeuEnderecoHTTP() string {
	return s.MeuEnderecoHTTP
}

func (s *Servidor) GetBrokerMQTT() string {
	return s.BrokerMQTT
}

func (s *Servidor) GetMQTTClient() mqtt.Client {
	return s.MQTTClient
}

func (s *Servidor) GetClusterManager() cluster.ClusterManagerInterface {
	return s.ClusterManager
}

func (s *Servidor) GetStore() store.StoreInterface {
	return s.Store
}

func (s *Servidor) GetGameManager() game.GameManagerInterface {
	return s.GameManager
}

func (s *Servidor) GetMQTTManager() mqttManager.MQTTManagerInterface {
	return s.MQTTManager
}

func (s *Servidor) PublicarParaCliente(clienteID string, msg protocolo.Mensagem) {
	s.publicarParaCliente(clienteID, msg)
}

func (s *Servidor) PublicarEventoPartida(salaID string, msg protocolo.Mensagem) {
	s.publicarEventoPartida(salaID, msg)
}

func (s *Servidor) NotificarCompraSucesso(clienteID string, cartas []tipos.Carta) {
	_, total := s.Store.GetStatusEstoque()
	msg := protocolo.Mensagem{
		Comando: "PACOTE_RESULTADO",
		Dados: seguranca.MustJSON(protocolo.ComprarPacoteResp{
			Cartas:          cartas,
			EstoqueRestante: total,
		}),
	}
	s.publicarParaCliente(clienteID, msg)
}

func (s *Servidor) AtualizarEstadoSalaRemoto(estado tipos.EstadoPartida) {
	s.mutexSalas.Lock()
	sala, ok := s.Salas[estado.SalaID]
	s.mutexSalas.Unlock()

	if !ok {
		log.Printf("[SYNC_SOMBRA_ERRO] Sala %s não encontrada para atualização remota", estado.SalaID)
		return
	}

	sala.Mutex.Lock()
	sala.Estado = estado.Estado
	sala.TurnoDe = estado.TurnoDe
	sala.Mutex.Unlock()

	log.Printf("[SYNC_SOMBRA_OK] Sala %s atualizada. Novo estado: %s, Turno de: %s", sala.ID, sala.Estado, sala.TurnoDe)
	log.Printf("[SYNC_SOMBRA_DEBUG] Jogadores na sala: %v", func() []string {
		ids := make([]string, len(sala.Jogadores))
		for i, j := range sala.Jogadores {
			ids[i] = fmt.Sprintf("%s(%s)", j.Nome, j.ID)
		}
		return ids
	}())
}

func (s *Servidor) CriarSalaRemota(solicitante, oponente *tipos.Cliente) {
	// Esta função agora precisa do endereço da sombra.
	// Ela é chamada por handleSolicitarOponente, que TEM o endereço.
	// Precisamos atualizar a interface em api.go
	log.Printf("[ERRO] CriarSalaRemota chamada sem endereço de sombra. Isso não deveria acontecer.")
	// A correção real é atualizar a interface, veja o próximo passo.
}

func (s *Servidor) CriarSalaRemotaComSombra(solicitante, oponente *tipos.Cliente, sombraAddr string) string {
	// A ordem dos jogadores aqui é crucial.
	// Em handleSolicitarOponente:
	//  'oponente' é o jogador local (j1)
	//  'solicitante' é o jogador remoto (j2)
	// Portanto, chamamos criarSala com (oponente, solicitante, sombraAddr)
	return s.criarSala(oponente, solicitante, sombraAddr)
}

func (s *Servidor) RemoverPrimeiroDaFila() *tipos.Cliente {
	s.mutexFila.Lock()
	defer s.mutexFila.Unlock()
	if len(s.FilaDeEspera) == 0 {
		return nil
	}
	oponente := s.FilaDeEspera[0]
	s.FilaDeEspera = s.FilaDeEspera[1:]
	return oponente
}

func (s *Servidor) ProcessarComandoRemoto(salaID string, mensagem protocolo.Mensagem) error {
	s.mutexSalas.RLock()
	_, ok := s.Salas[salaID]
	s.mutexSalas.RUnlock()

	if !ok {
		return fmt.Errorf("sala %s não encontrada no servidor host", salaID)
	}

	// Extrai o clienteID do payload da mensagem genérica
	var dadosComClienteID struct {
		ClienteID string `json:"cliente_id"`
	}
	if err := json.Unmarshal(mensagem.Dados, &dadosComClienteID); err != nil {
		return fmt.Errorf("não foi possível extrair cliente_id do comando remoto: %v", err)
	}

	// Constrói o comando no formato esperado pelo canal
	comando := protocolo.Comando{
		ClienteID: dadosComClienteID.ClienteID,
		Tipo:      mensagem.Comando,
		Payload:   mensagem.Dados,
	}

	// Envia o comando para a goroutine da partida
	s.ComandosPartida[salaID] <- comando
	return nil
}

func (s *Servidor) GetStatusEstoque() (map[string]int, int) {
	return s.Store.GetStatusEstoque()
}

func novoServidor(endereco, broker string) *Servidor {
	serverID := os.Getenv("SERVER_ID")
	if serverID == "" {
		log.Fatal("A variável de ambiente SERVER_ID não foi definida!")
	}

	servidor := &Servidor{
		ServerID:        serverID,
		MeuEndereco:     endereco,
		MeuEnderecoHTTP: "http://" + endereco,
		BrokerMQTT:      broker,
		Store:           store.NewStore(),
		Clientes:        make(map[string]*tipos.Cliente),
		Salas:           make(map[string]*tipos.Sala),
		FilaDeEspera:    make([]*tipos.Cliente, 0),
		ComandosPartida: make(map[string]chan protocolo.Comando),
	}

	// Initialize managers
	servidor.ClusterManager = cluster.NewManager(servidor)
	// TODO: Initialize game and MQTT managers when interfaces are simplified
	// servidor.GameManager = game.NewManager(servidor)
	// servidor.MQTTManager = mqttManager.NewManager(servidor)

	return servidor
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

// subscreverTopicos centraliza as subscrições MQTT.
func (s *Servidor) subscreverTopicos() {
	// Inscrição para responder a pedidos de informação dos clientes
	infoTopic := fmt.Sprintf("servidores/%s/info_req/+", s.ServerID)
	if token := s.MQTTClient.Subscribe(infoTopic, 1, s.handleInfoRequest); token.Wait() && token.Error() != nil {
		log.Printf("Erro ao subscrever ao tópico de info: %v", token.Error())
	}

	s.MQTTClient.Subscribe("clientes/+/login", 0, s.handleClienteLogin)
	s.MQTTClient.Subscribe("clientes/+/entrar_fila", 0, s.handleClienteEntrarFila)
	s.MQTTClient.Subscribe("partidas/+/comandos", 0, s.handleComandoPartida)
	log.Println("Subscreveu aos tópicos MQTT essenciais")
}

// handleInfoRequest processa um pedido de informação de um cliente e responde.
func (s *Servidor) handleInfoRequest(client mqtt.Client, msg mqtt.Message) {
	// O tópico tem o formato: servidores/{serverID}/info_req/{clientID}
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 4 {
		log.Printf("Tópico de info request inválido recebido: %s", msg.Topic())
		return
	}
	clientID := parts[3]

	log.Printf("[INFO] Recebido pedido de informação do cliente %s", clientID)

	// Prepara a mensagem de resposta
	responsePayload, _ := json.Marshal(map[string]string{
		"server_id": s.ServerID,
	})

	responseMsg := protocolo.Mensagem{
		Comando: "INFO_SERVIDOR_RESP",
		Dados:   responsePayload,
	}

	// Publica a resposta no tópico de eventos privados do cliente
	s.publicarParaCliente(clientID, responseMsg)
}

func (s *Servidor) handleClienteLogin(client mqtt.Client, msg mqtt.Message) {
	s.mutexClientes.Lock()         // Bloqueia logo no início
	defer s.mutexClientes.Unlock() // Garante que desbloqueia ao sair

	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 3 {
		log.Printf("[LOGIN_ERRO:%s] Tópico de login inválido: %s", s.ServerID, msg.Topic())
		return
	}
	tempClientID := parts[1]

	var mensagem protocolo.Mensagem
	log.Printf("[LOGIN_DEBUG:%s] Payload recebido: %s", s.ServerID, string(msg.Payload()))
	if err := json.Unmarshal(msg.Payload(), &mensagem); err != nil {
		log.Printf("[LOGIN_ERRO:%s] Erro ao decodificar mensagem: %v", s.ServerID, err)
		return
	}

	var dados protocolo.DadosLogin
	if err := json.Unmarshal(mensagem.Dados, &dados); err != nil {
		log.Printf("[LOGIN_ERRO:%s] Erro ao decodificar dados de login: %v", s.ServerID, err)
		return
	}
	log.Printf("[LOGIN_DEBUG:%s] Dados decodificados - Nome: '%s' (len=%d)", s.ServerID, dados.Nome, len(dados.Nome))
	if dados.Nome == "" {
		log.Printf("[LOGIN_ERRO:%s] Nome do jogador vazio recebido no login.", s.ServerID)
		erroMsg := protocolo.Mensagem{Comando: "ERRO", Dados: seguranca.MustJSON(protocolo.DadosErro{Mensagem: "Nome de usuário não pode ser vazio."})}
		s.publicarParaCliente(tempClientID, erroMsg)
		return
	}

	log.Printf("[LOGIN_DEBUG:%s] Nome válido, criando cliente...", s.ServerID)
	clienteID := uuid.New().String() // ID permanente
	novoCliente := &tipos.Cliente{
		ID:         clienteID,
		Nome:       dados.Nome,
		Inventario: make([]protocolo.Carta, 0),
	}

	log.Printf("[LOGIN_DEBUG:%s] Adicionando cliente ao mapa...", s.ServerID)
	s.Clientes[clienteID] = novoCliente
	log.Printf("[LOGIN_DEBUG:%s] Cliente adicionado ao mapa.", s.ServerID)

	log.Printf("[LOGIN:%s] Cliente %s (ID temp: %s, ID perm: %s) registrado e pronto.", s.ServerID, dados.Nome, tempClientID, clienteID)

	// Envia confirmação de volta para o TÓPICO TEMPORÁRIO
	log.Printf("[LOGIN_DEBUG:%s] Enviando resposta LOGIN_OK...", s.ServerID)
	resposta := protocolo.Mensagem{
		Comando: "LOGIN_OK",
		Dados:   seguranca.MustJSON(map[string]string{"cliente_id": clienteID, "servidor": s.MeuEndereco}),
	}
	s.publicarParaCliente(tempClientID, resposta)
	log.Printf("[LOGIN_DEBUG:%s] Resposta LOGIN_OK enviada.", s.ServerID)
}

func (s *Servidor) handleClienteEntrarFila(client mqtt.Client, msg mqtt.Message) {
	var dados map[string]string
	if err := json.Unmarshal(msg.Payload(), &dados); err != nil {
		log.Printf("[ENTRAR_FILA_ERRO:%s] Erro ao decodificar JSON: %v", s.ServerID, err)
		return
	}
	clienteID := dados["cliente_id"] // ID PERMANENTE enviado pelo cliente

	s.mutexClientes.RLock() // Lock de leitura para verificar
	cliente, existe := s.Clientes[clienteID]
	nomeCliente := ""
	if existe {
		cliente.Mutex.Lock() // Lock no cliente específico para ler o nome
		nomeCliente = cliente.Nome
		cliente.Mutex.Unlock()
	}
	s.mutexClientes.RUnlock()

	// Verifica se o cliente existe E se o nome não está vazio
	if !existe || nomeCliente == "" {
		log.Printf("[ENTRAR_FILA_ERRO:%s] Cliente %s não encontrado ou nome ainda vazio (login pode não ter sido concluído).", s.ServerID, clienteID)
		// Notificar o cliente seria ideal aqui
		s.publicarParaCliente(clienteID, protocolo.Mensagem{Comando: "ERRO", Dados: seguranca.MustJSON(protocolo.DadosErro{Mensagem: "Erro ao entrar na fila. Tente novamente."})})
		return
	}

	// Se chegou aqui, o cliente existe e tem nome (login concluído)
	log.Printf("[ENTRAR_FILA:%s] Cliente %s (%s) encontrado. Adicionando à fila.", s.ServerID, nomeCliente, clienteID)
	s.entrarFila(cliente) // Chama a função que adiciona à fila e inicia a busca
}

func (s *Servidor) handleComandoPartida(client mqtt.Client, msg mqtt.Message) {
	// CORREÇÃO: Adicionar logs detalhados para debugging
	timestamp := time.Now().Format("15:04:05.000")
	log.Printf("[%s][COMANDO_DEBUG] === INÍCIO PROCESSAMENTO COMANDO ===", timestamp)

	// Extrai o ID da sala do tópico
	topico := msg.Topic()
	// topico formato: "partidas/{salaID}/comandos"
	partes := strings.Split(topico, "/")
	if len(partes) < 2 {
		log.Printf("[%s][COMANDO_ERRO] Tópico inválido: %s", timestamp, topico)
		return
	}
	salaID := partes[1]

	log.Printf("[%s][COMANDO_DEBUG] Comando recebido no tópico: %s", timestamp, topico)
	log.Printf("[%s][COMANDO_DEBUG] Payload: %s", timestamp, string(msg.Payload()))

	var mensagem protocolo.Mensagem
	if err := json.Unmarshal(msg.Payload(), &mensagem); err != nil {
		log.Printf("[%s][COMANDO_ERRO] Erro ao decodificar comando: %v", timestamp, err)
		return
	}

	log.Printf("[%s][COMANDO_DEBUG] Comando decodificado: %s", timestamp, mensagem.Comando)

	s.mutexSalas.RLock()
	sala, existe := s.Salas[salaID]
	s.mutexSalas.RUnlock()

	if !existe {
		log.Printf("Sala %s não encontrada", salaID)
		return
	}

	// Verifica se este servidor é o Host ou Sombra
	sala.Mutex.Lock()
	servidorHost := sala.ServidorHost
	servidorSombra := sala.ServidorSombra
	sala.Mutex.Unlock()

	// Processa comando baseado no tipo
	switch mensagem.Comando {
	case "COMPRAR_PACOTE":
		var dados map[string]string
		json.Unmarshal(mensagem.Dados, &dados)
		clienteID := dados["cliente_id"]

		// CORREÇÃO: Aplicar lógica Host/Shadow
		sala.Mutex.Lock()
		servidorHost := sala.ServidorHost
		servidorSombra := sala.ServidorSombra
		sala.Mutex.Unlock()

		// Sempre processa compra localmente
		s.processarCompraPacote(clienteID, sala)

		// AGORA, notificamos o Host se formos o Shadow
		if servidorHost == s.MeuEndereco {
			// Eu sou o Host. processarCompraPacote já chamou verificarEIniciarPartidaSeProntos.
			log.Printf("[HOST] Compra processada localmente para %s. Verificando prontos.", clienteID)
		} else if servidorSombra == s.MeuEndereco {
			// Eu sou o Shadow. Além de processar a compra,
			// devo notificar o Host que este jogador está PRONTO.
			log.Printf("[SHADOW] Compra processada localmente para %s. Notificando Host %s que estou pronto.", clienteID, servidorHost)
			go s.encaminharEventoParaHost(sala, clienteID, "PLAYER_READY", nil)
		}

	case "JOGAR_CARTA":
		var dados map[string]interface{}
		json.Unmarshal(mensagem.Dados, &dados)
		clienteID := dados["cliente_id"].(string)
		cartaID := dados["carta_id"].(string)

		// Se este servidor é o Host, processa diretamente
		if servidorHost == s.MeuEndereco {
			// CORREÇÃO: Construir o objeto GameEventRequest
			eventoReq := &tipos.GameEventRequest{
				MatchID:   sala.ID,
				EventSeq:  sala.EventSeq + 1, // O Host validará
				EventType: "CARD_PLAYED",
				PlayerID:  clienteID,
				Data: map[string]interface{}{
					"carta_id": cartaID,
				},
			}
			s.processarEventoComoHost(sala, eventoReq) // <-- CHAMADA CORRIGIDA

		} else if servidorSombra == s.MeuEndereco {
			// Se é a Sombra, encaminha para o Host via API REST
			s.encaminharJogadaParaHost(sala, clienteID, cartaID)
		}

	// Em func (s *Servidor) handleComandoPartida
	case "CHAT":
		var dadosCliente protocolo.DadosEnviarChat
		if err := json.Unmarshal(mensagem.Dados, &dadosCliente); err != nil {
			log.Printf("[CHAT_ERRO] Erro ao decodificar dados do chat: %v", err)
			return
		}

		// CORREÇÃO: Aplicar lógica Host/Shadow
		sala.Mutex.Lock()
		servidorHost := sala.ServidorHost
		servidorSombra := sala.ServidorSombra
		sala.Mutex.Unlock()

		s.mutexClientes.RLock()
		cliente := s.Clientes[dadosCliente.ClienteID]
		s.mutexClientes.RUnlock()
		if cliente == nil {
			return
		}
		cliente.Mutex.Lock()
		nomeJogador := cliente.Nome
		cliente.Mutex.Unlock()

		if nomeJogador == "" {
			log.Printf("[CHAT_ERRO] Nome do jogador para cliente %s não encontrado.", dadosCliente.ClienteID)
			return
		}

		if servidorHost == s.MeuEndereco {
			// Eu sou o Host, eu faço o broadcast
			log.Printf("[HOST-CHAT] Recebido chat de %s. Fazendo broadcast.", nomeJogador)
			s.broadcastChat(sala, dadosCliente.Texto, nomeJogador)
		} else if servidorSombra == s.MeuEndereco {
			// Eu sou o Shadow, encaminho para o Host
			log.Printf("[SHADOW-CHAT] Recebido chat de %s. Encaminhando para Host %s.", nomeJogador, servidorHost)
			dadosEvento := map[string]interface{}{
				"texto": dadosCliente.Texto,
			}
			go s.encaminharEventoParaHost(sala, dadosCliente.ClienteID, "CHAT", dadosEvento)
		}

	case "TROCAR_CARTAS":
		var req protocolo.TrocarCartasReq
		if err := json.Unmarshal(mensagem.Dados, &req); err != nil {
			log.Printf("[TROCA_ERRO] Erro ao decodificar requisição de troca: %v", err)
			return
		}
		s.processarTrocaCartas(sala, &req)
	}
}

func (s *Servidor) publicarParaCliente(clienteID string, msg protocolo.Mensagem) {
	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("clientes/%s/eventos", clienteID)
	log.Printf("[PUBLICAR_CLIENTE] Enviando para %s no tópico %s: %s", clienteID, topico, string(payload))
	s.MQTTClient.Publish(topico, 0, false, payload)
}

func (s *Servidor) publicarEventoPartida(salaID string, msg protocolo.Mensagem) {
	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("partidas/%s/eventos", salaID)
	s.MQTTClient.Publish(topico, 0, false, payload)
}

// ==================== MATCHMAKING E LÓGICA DE JOGO ====================

func (s *Servidor) tentarMatchmakingGlobalPeriodicamente() {
	// Aguarda um pouco no início para a rede de servidores se estabilizar
	time.Sleep(10 * time.Second)

	ticker := time.NewTicker(5 * time.Second) // Tenta a cada 5 segundos
	defer ticker.Stop()

	for range ticker.C {
		s.mutexFila.Lock()
		if len(s.FilaDeEspera) == 0 {
			s.mutexFila.Unlock()
			continue // Pula se a fila estiver vazia
		}
		// Pega o primeiro jogador sem removê-lo ainda
		jogadorLocal := s.FilaDeEspera[0]
		s.mutexFila.Unlock()

		log.Printf("[MATCHMAKING-GLOBAL] Buscando oponente para %s (%s)...", jogadorLocal.Nome, jogadorLocal.ID)

		// Pega a lista de servidores ativos, excluindo a si mesmo
		servidoresAtivos := s.ClusterManager.GetServidoresAtivos(s.MeuEndereco)

		// Itera sobre os outros servidores para encontrar um oponente
		for _, addr := range servidoresAtivos {
			if s.realizarSolicitacaoMatchmaking(addr, jogadorLocal) {
				// Sucesso! A partida foi formada. O jogador já foi removido da fila pela lógica de sucesso.
				// A `realizarSolicitacaoMatchmaking` agora precisa remover o jogador da fila em caso de sucesso.
				break // Para de procurar por este jogador
			}
		}
	}
}

func (s *Servidor) entrarFila(cliente *tipos.Cliente) {
	s.mutexFila.Lock()
	// Tenta encontrar oponente na fila local primeiro
	if len(s.FilaDeEspera) > 0 {
		oponente := s.FilaDeEspera[0]
		s.FilaDeEspera = s.FilaDeEspera[1:]
		s.mutexFila.Unlock()

		// Cria sala localmente
		s.criarSala(oponente, cliente, "")
		return
	}

	// Se não encontrou, adiciona à fila local
	s.FilaDeEspera = append(s.FilaDeEspera, cliente)
	s.mutexFila.Unlock()

	log.Printf("Cliente %s (%s) entrou na fila de espera.", cliente.Nome, cliente.ID)
	s.publicarParaCliente(cliente.ID, protocolo.Mensagem{
		Comando: "AGUARDANDO_OPONENTE",
		Dados:   seguranca.MustJSON(map[string]string{"mensagem": "Procurando oponente em todos os servidores..."}),
	})

	// O matchmaking global já é persistente, não precisamos mais do 'go func' aqui
	// O go s.tentarMatchmakingGlobalPeriodicamente() no 'Run' cuidará disso
}

// gerenciarBuscaGlobalPersistente tenta encontrar um oponente global periodicamente.
func (s *Servidor) gerenciarBuscaGlobalPersistente(cliente *tipos.Cliente) {
	ticker := time.NewTicker(5 * time.Second) // Tenta a cada 5 segundos
	defer ticker.Stop()

	// Cria um contexto para poder cancelar esta goroutine se o cliente sair/for matchado
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Garante que o cancelamento seja chamado ao sair

	// Armazena a função de cancelamento para uso posterior (ex: quando o cliente for matchado localmente)
	cliente.Mutex.Lock()
	// Assumindo que você adicione um campo `cancelBuscaGlobal context.CancelFunc` à struct Cliente
	// cliente.cancelBuscaGlobal = cancel // (Descomente se adicionar o campo)
	cliente.Mutex.Unlock()

	for {
		select {
		case <-ticker.C:
			// 1. Verifica se o cliente AINDA está na fila deste servidor
			s.mutexFila.Lock()
			aindaNaFila := false
			for _, c := range s.FilaDeEspera {
				if c.ID == cliente.ID {
					aindaNaFila = true
					break
				}
			}
			s.mutexFila.Unlock()

			if !aindaNaFila {
				log.Printf("[MATCHMAKING-PERSIST] Cliente %s (%s) não está mais na fila local. Parando busca global.", cliente.Nome, cliente.ID)
				return // Cliente já foi matchado (localmente ou por outra tentativa), sai da rotina
			}

			// 2. Tenta encontrar um oponente global AGORA (sem remover da fila!)
			log.Printf("[MATCHMAKING-PERSIST] Tentando busca global para %s (%s)", cliente.Nome, cliente.ID)
			encontrou := s.tentarMatchmakingGlobalAgora(cliente)

			if encontrou {
				log.Printf("[MATCHMAKING-PERSIST] Busca global para %s (%s) encontrou partida! A busca será finalizada.", cliente.Nome, cliente.ID)
				// A remoção da fila agora é feita dentro de `realizarSolicitacaoMatchmaking` no momento da confirmação.
				// Apenas precisamos parar a busca.
				return // Encontrou, sai da rotina
			}
			// Se não encontrou, o loop continua na próxima iteração do ticker.

		case <-ctx.Done():
			log.Printf("[MATCHMAKING-PERSIST] Busca global para %s (%s) cancelada.", cliente.Nome, cliente.ID)
			return // Sai da rotina se o contexto for cancelado
		}
	}
}

// tentarMatchmakingGlobalAgora faz UMA tentativa de encontrar um oponente global.
func (s *Servidor) tentarMatchmakingGlobalAgora(cliente *tipos.Cliente) bool {
	// Busca oponente em outros servidores ATIVOS
	servidores := s.ClusterManager.GetServidores()
	servidoresAtivos := make([]string, 0, len(servidores))
	for addr, info := range servidores {
		if addr != s.MeuEndereco && info.Ativo {
			servidoresAtivos = append(servidoresAtivos, addr)
		}
	}

	if len(servidoresAtivos) == 0 {
		// log.Printf("[MATCHMAKING-AGORA] Nenhum outro servidor ativo para buscar oponente para %s.", cliente.Nome)
		return false // Não há ninguém para perguntar
	}

	// Embaralha a ordem para não sobrecarregar sempre o mesmo servidor
	rand.Shuffle(len(servidoresAtivos), func(i, j int) {
		servidoresAtivos[i], servidoresAtivos[j] = servidoresAtivos[j], servidoresAtivos[i]
	})

	for _, addr := range servidoresAtivos {
		if s.realizarSolicitacaoMatchmaking(addr, cliente) {
			return true // Encontrou partida! A função interna já tratou de tudo.
		}
	}

	// Se chegou aqui, não encontrou oponente em NENHUM servidor nesta tentativa.
	return false
}

// realizarSolicitacaoMatchmaking envia uma requisição de oponente para um servidor específico.
func (s *Servidor) realizarSolicitacaoMatchmaking(addr string, cliente *tipos.Cliente) bool {
	log.Printf("[MATCHMAKING-TX] Enviando solicitação para %s", addr)
	reqBody, _ := json.Marshal(map[string]string{
		"solicitante_id":   cliente.ID,
		"solicitante_nome": cliente.Nome,
		"servidor_origem":  s.MeuEndereco,
	})

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/matchmaking/solicitar_oponente", addr), bytes.NewBuffer(reqBody))
	if err != nil {
		log.Printf("[MATCHMAKING-TX] Erro ao criar requisição para %s: %v", addr, err)
		return false
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+seguranca.GenerateJWT(s.ServerID))

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[MATCHMAKING-TX] Erro ao contatar servidor %s: %v", addr, err)
		return false
	}
	defer resp.Body.Close()

	var res struct {
		PartidaEncontrada bool   `json:"partida_encontrada"`
		SalaID            string `json:"sala_id"`
		OponenteNome      string `json:"oponente_nome"`
		OponenteID        string `json:"oponente_id"`
		ServidorHost      string `json:"servidor_host"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		log.Printf("[MATCHMAKING-TX] Erro ao decodificar resposta de %s: %v", addr, err)
		return false
	}

	if res.PartidaEncontrada {
		log.Printf("[MATCHMAKING-TX] Partida encontrada no servidor %s! Oponente: %s", addr, res.OponenteNome)

		// A requisição foi bem-sucedida, então removemos nosso cliente da fila local.
		s.RemoverPrimeiroDaFila()

		// Cria um objeto Cliente para o oponente remoto

		s.criarSalaComoSombra(cliente, res.SalaID, res.OponenteID, res.OponenteNome, res.ServidorHost)
		return true // Sucesso!
	}

	log.Printf("[MATCHMAKING-TX] Servidor %s não encontrou oponente.", addr)
	return false
}

// Em: servidor/main.go
// ADICIONAR esta nova função
// Em: servidor/main.go
// SUBSTITUA a função 'criarSalaComoSombra' (Etapa 7) por esta:

func (s *Servidor) criarSalaComoSombra(jogadorLocal *tipos.Cliente, salaID string, oponenteID string, oponenteNome string, hostAddr string) {
	// Cria objeto para oponente remoto (o Host)
	oponenteRemoto := &tipos.Cliente{
		ID:   oponenteID,
		Nome: oponenteNome,
	}

	// Busca info completa do jogador local (que ESTÁ no mapa)
	s.mutexClientes.RLock()
	clienteLocalCompleto, ok := s.Clientes[jogadorLocal.ID]
	s.mutexClientes.RUnlock()
	if !ok || clienteLocalCompleto == nil {
		log.Printf("[CRIAR_SALA_SOMBRA_ERRO] Cliente local %s não encontrado no mapa principal.", jogadorLocal.ID)
		return
	}

	clienteLocalCompleto.Mutex.Lock()
	nomeJ1 := clienteLocalCompleto.Nome
	clienteLocalCompleto.Mutex.Unlock()

	nomeJ2 := oponenteRemoto.Nome // Nome do oponente remoto (Host)

	log.Printf("[CRIAR_SALA_SOMBRA:%s] Criando sala %s (Host: %s) para %s vs %s", s.ServerID, salaID, hostAddr, nomeJ1, nomeJ2)

	novaSala := &tipos.Sala{
		ID:             salaID,
		Jogadores:      []*tipos.Cliente{clienteLocalCompleto, oponenteRemoto}, // Usa o local (completo) e o remoto (DTO)
		Estado:         "AGUARDANDO_COMPRA",
		CartasNaMesa:   make(map[string]Carta),
		PontosRodada:   make(map[string]int),
		PontosPartida:  make(map[string]int),
		NumeroRodada:   1,
		Prontos:        make(map[string]bool),
		ServidorHost:   hostAddr,      // Aponta para o Host
		ServidorSombra: s.MeuEndereco, // Eu sou a Sombra
	}

	s.mutexSalas.Lock()
	s.Salas[salaID] = novaSala
	s.mutexSalas.Unlock()

	// Associa a sala ao jogador LOCAL
	clienteLocalCompleto.Mutex.Lock()
	clienteLocalCompleto.Sala = novaSala
	clienteLocalCompleto.Mutex.Unlock()

	// Associa a sala ao jogador REMOTO (DTO)
	oponenteRemoto.Mutex.Lock()
	oponenteRemoto.Sala = novaSala
	oponenteRemoto.Mutex.Unlock()

	log.Printf("Sala %s criada como SOMBRA. Host: %s", salaID, hostAddr)

	// Notifica jogador local (o Sombra notifica seu jogador)
	msg := protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   seguranca.MustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: oponenteID, OponenteNome: oponenteNome}),
	}
	s.publicarParaCliente(jogadorLocal.ID, msg)
}

// Em: servidor/main.go
// SUBSTITUA a função 'criarSala' (Etapa 1) por esta:

func (s *Servidor) criarSala(j1 *tipos.Cliente, j2 *tipos.Cliente, sombraAddr string) string {
	salaID := uuid.New().String()

	// --- LÓGICA DE BUSCA CORRIGIDA ---
	// j1 é o jogador local (oponente) que ESTÁ no mapa.
	// j2 é o jogador remoto (solicitante) que NÃO ESTÁ no mapa.

	s.mutexClientes.RLock()
	// Busca o struct completo do jogador local
	cliente1Completo, ok1 := s.Clientes[j1.ID]
	s.mutexClientes.RUnlock()

	if !ok1 {
		log.Printf("[CRIAR_SALA_ERRO:%s] Falha ao buscar informações completas do JOGADOR LOCAL %s (%s) no mapa principal.", s.ServerID, j1.Nome, j1.ID)
		return "" // Aborta
	}

	// Usa o struct DTO (Data Transfer Object) do jogador remoto diretamente.
	// Este struct foi criado no 'api/handlers.go'
	cliente2Completo := j2
	// --- FIM DA LÓGICA DE BUSCA ---

	// Leitura segura dos nomes
	cliente1Completo.Mutex.Lock()
	nomeJ1 := cliente1Completo.Nome
	cliente1Completo.Mutex.Unlock()

	// O cliente remoto (j2) é só um DTO, usamos o nome que veio na requisição
	nomeJ2 := cliente2Completo.Nome

	idJ1 := cliente1Completo.ID
	idJ2 := cliente2Completo.ID

	log.Printf("[CRIAR_SALA:%s] Tentando criar sala %s para %s (%s) vs %s (%s)", s.ServerID, salaID, nomeJ1, idJ1, nomeJ2, idJ2)
	if nomeJ1 == "" || nomeJ2 == "" {
		log.Printf("[CRIAR_SALA_ERRO:%s] Nomes vazios detectados! Abortando.", s.ServerID)
		return ""
	}

	novaSala := &tipos.Sala{
		ID:             salaID,
		Jogadores:      []*tipos.Cliente{cliente1Completo, cliente2Completo}, // Usa o local (completo) e o remoto (DTO)
		Estado:         "AGUARDANDO_COMPRA",
		CartasNaMesa:   make(map[string]Carta),
		PontosRodada:   make(map[string]int),
		PontosPartida:  make(map[string]int),
		NumeroRodada:   1,
		Prontos:        make(map[string]bool),
		ServidorHost:   s.MeuEndereco,
		ServidorSombra: sombraAddr, // Salva o endereço da Sombra
	}

	s.mutexSalas.Lock()
	s.Salas[salaID] = novaSala
	s.mutexSalas.Unlock()

	// Associa a sala ao jogador LOCAL
	cliente1Completo.Mutex.Lock()
	cliente1Completo.Sala = novaSala
	cliente1Completo.Mutex.Unlock()

	// Associa a sala ao jogador REMOTO (que está na struct 'novaSala.Jogadores')
	// O struct 'cliente2Completo' (j2) foi criado no handler e tem seu próprio mutex.
	cliente2Completo.Mutex.Lock()
	cliente2Completo.Sala = novaSala
	cliente2Completo.Mutex.Unlock()

	log.Printf("Sala %s criada localmente (HOST): %s vs %s. Sombra: %s", salaID, nomeJ1, nomeJ2, sombraAddr)

	// Notifica jogadores (o sistema MQTT/API fará o roteamento)
	msg1 := protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   seguranca.MustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: idJ2, OponenteNome: nomeJ2}),
	}

	s.publicarParaCliente(j1.ID, msg1) // Notifica o jogador local
	log.Printf("[CRIAR_SALA:NOTIFICACAO] Enviando PARTIDA_ENCONTRADA para JOGADOR LOCAL %s (ID: %s)", nomeJ1, idJ1)

	// CORREÇÃO: Se for uma partida local (sem sombra), notifica o j2 também.
	if sombraAddr == "" {
		msg2 := protocolo.Mensagem{
			Comando: "PARTIDA_ENCONTRADA",
			Dados:   seguranca.MustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: idJ1, OponenteNome: nomeJ1}),
		}
		s.publicarParaCliente(j2.ID, msg2)
		log.Printf("[CRIAR_SALA:NOTIFICACAO] Enviando PARTIDA_ENCONTRADA para JOGADOR LOCAL %s (ID: %s)", nomeJ2, idJ2)
	}

	// A notificação para j2 (remoto) será feita pelo servidor Sombra quando ele criar sua sala.
	log.Printf("[CRIAR_SALA:NOTIFICACAO] Enviando PARTIDA_ENCONTRADA para JOGADOR REMOTO %s (ID: %s)", nomeJ2, idJ2)

	// O handler da API (Etapa 5) usará este ID para responder ao Sombra.
	return salaID
}

// CORREÇÃO: Funções para comunicação cross-server
func (s *Servidor) CriarSalaCrossServer(matchID string, players []tipos.Player, hostServer string) string {
	log.Printf("[CRIAR_SALA_CROSS] Criando sala %s como Host", matchID)

	// Cria sala como Host
	novaSala := &tipos.Sala{
		ID:             matchID,
		Jogadores:      make([]*tipos.Cliente, 0, len(players)),
		Estado:         "AGUARDANDO_COMPRA",
		CartasNaMesa:   make(map[string]Carta),
		PontosRodada:   make(map[string]int),
		PontosPartida:  make(map[string]int),
		NumeroRodada:   1,
		Prontos:        make(map[string]bool),
		ServidorHost:   s.MeuEndereco,
		ServidorSombra: "", // Será definido quando Shadow se conectar
	}

	// Adiciona jogadores à sala
	for _, player := range players {
		cliente := &tipos.Cliente{
			ID:   player.ID,
			Nome: player.Nome,
		}
		novaSala.Jogadores = append(novaSala.Jogadores, cliente)
	}

	s.mutexSalas.Lock()
	s.Salas[matchID] = novaSala
	s.mutexSalas.Unlock()

	log.Printf("[CRIAR_SALA_CROSS] Sala %s criada como Host", matchID)
	return matchID
}

func (s *Servidor) ProcessarEventoComoHost(matchID string, eventSeq int64, eventType, playerID string, data map[string]interface{}) bool {
	s.mutexSalas.Lock()
	sala, ok := s.Salas[matchID]
	s.mutexSalas.Unlock()

	if !ok {
		log.Printf("[PROCESSAR_EVENTO] Sala %s não encontrada", matchID)
		return false
	}

	sala.Mutex.Lock()
	defer sala.Mutex.Unlock()

	// Valida eventSeq
	if eventSeq <= sala.EventSeq {
		log.Printf("[PROCESSAR_EVENTO] EventSeq %d é menor ou igual ao atual %d", eventSeq, sala.EventSeq)
		return false
	}

	// Atualiza eventSeq
	sala.EventSeq = eventSeq

	// Processa evento baseado no tipo
	switch eventType {
	case "CARD_PLAYED":
		// CORREÇÃO: Cria evento e processa
		evento := &tipos.GameEventRequest{
			MatchID:   matchID,
			EventSeq:  eventSeq,
			EventType: eventType,
			PlayerID:  playerID,
			Data:      data,
		}
		s.processarEventoComoHost(sala, evento)
	case "PACKAGE_PURCHASED":
		// Processa compra de pacote
		log.Printf("[PROCESSAR_EVENTO] Processando compra de pacote para %s", playerID)
	}

	return true
}

func (s *Servidor) ReplicarEstadoComoShadow(matchID string, eventSeq int64, state tipos.EstadoPartida) bool {
	s.mutexSalas.Lock()
	sala, ok := s.Salas[matchID]
	s.mutexSalas.Unlock()

	if !ok {
		// Cria sala como Shadow se não existir
		log.Printf("[REPLICAR_ESTADO] Criando sala %s como Shadow", matchID)
		sala = &tipos.Sala{
			ID:        matchID,
			Jogadores: make([]*tipos.Cliente, 0),
		}

		s.mutexSalas.Lock()
		s.Salas[matchID] = sala
		s.mutexSalas.Unlock()
	}

	sala.Mutex.Lock()
	defer sala.Mutex.Unlock()

	// Valida eventSeq
	if eventSeq <= sala.EventSeq {
		log.Printf("[REPLICAR_ESTADO] EventSeq %d é menor ou igual ao atual %d", eventSeq, sala.EventSeq)
		return false
	}

	// Atualiza estado
	sala.Estado = state.Estado
	sala.CartasNaMesa = state.CartasNaMesa
	sala.PontosRodada = state.PontosRodada
	sala.PontosPartida = state.PontosPartida
	sala.NumeroRodada = state.NumeroRodada
	sala.Prontos = state.Prontos
	sala.EventSeq = eventSeq

	log.Printf("[REPLICAR_ESTADO] Estado da sala %s sincronizado (eventSeq: %d)", matchID, eventSeq)
	return true
}

// CORREÇÃO: Funções auxiliares para a API (GetMeuEndereco já existe)

// encaminharEventoParaHost envia um evento genérico do Shadow para o Host via API REST
func (s *Servidor) encaminharEventoParaHost(sala *tipos.Sala, clienteID, eventType string, data map[string]interface{}) {
	sala.Mutex.Lock()
	host := sala.ServidorHost
	// Incremento otimista. O Host validará e definirá o eventSeq final.
	eventSeq := sala.EventSeq + 1
	sala.Mutex.Unlock()

	log.Printf("[SHADOW] Encaminhando evento %s de %s para o Host %s (eventSeq: %d)", eventType, clienteID, host, eventSeq)

	// Usa o endpoint padrão /game/event
	req := tipos.GameEventRequest{
		MatchID:   sala.ID,
		EventSeq:  eventSeq,
		EventType: eventType,
		PlayerID:  clienteID,
		Data:      data,
		Token:     seguranca.GenerateJWT(s.ServerID),
	}

	// Gera assinatura
	event := tipos.GameEvent{
		EventSeq:  req.EventSeq,
		MatchID:   req.MatchID,
		EventType: req.EventType,
		PlayerID:  req.PlayerID,
	}
	seguranca.SignEvent(&event)
	req.Signature = event.Signature

	jsonData, _ := json.Marshal(req)
	url := fmt.Sprintf("http://%s/game/event", host)

	httpClient := &http.Client{Timeout: 5 * time.Second}
	httpReq, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[FAILOVER] Host %s inacessível ao enviar evento %s: %v.", host, eventType, err)
		// A lógica de failover (promoção) deve ser tratada aqui
		// s.promoverSombraAHost(sala)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[SHADOW] Host retornou status %d ao processar evento %s", resp.StatusCode, eventType)
		return
	}

	log.Printf("[SHADOW] Evento %s processado pelo Host com sucesso", eventType)
}

func (s *Servidor) processarCompraPacote(clienteID string, sala *tipos.Sala) {
	// Se não for o líder, faz requisição para o líder
	souLider := s.ClusterManager.SouLider()
	lider := s.ClusterManager.GetLider()

	log.Printf("[COMPRAR_DEBUG] Processando compra para cliente %s, souLider: %v", clienteID, souLider)

	cartas := make([]Carta, 0) // Inicializa como slice vazio, não nil

	if souLider {
		cartas = s.Store.FormarPacote(PACOTE_SIZE)
		log.Printf("[COMPRAR_DEBUG] Líder retirou %d cartas do estoque", len(cartas))
	} else {
		// Faz requisição HTTP para o líder
		dados := map[string]interface{}{
			"cliente_id": clienteID,
		}
		jsonData, _ := json.Marshal(dados)
		url := fmt.Sprintf("http://%s/estoque/comprar_pacote", lider)

		// Cria requisição HTTP com autenticação JWT
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			log.Printf("Erro ao criar requisição para o líder: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+seguranca.GenerateJWT(s.ServerID))

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Erro ao requisitar pacote do líder: %v", err)
			return
		}
		defer resp.Body.Close()

		var resultado struct {
			Pacote []Carta `json:"pacote"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&resultado); err != nil {
			log.Printf("Erro ao decodificar resposta do líder: %v", err)
			return
		}

		cartas = resultado.Pacote
	}

	// Adiciona cartas ao inventário do cliente
	s.mutexClientes.RLock()
	cliente := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()

	cliente.Mutex.Lock()
	cliente.Inventario = append(cliente.Inventario, cartas...)
	cliente.Mutex.Unlock()

	// Notifica cliente
	_, total := s.Store.GetStatusEstoque()
	msg := protocolo.Mensagem{
		Comando: "PACOTE_RESULTADO",
		Dados: seguranca.MustJSON(protocolo.ComprarPacoteResp{
			Cartas:          cartas,
			EstoqueRestante: total,
		}),
	}
	// Correção: Notifica o cliente localmente via MQTT ou remotamente via API
	if s.getClienteLocal(clienteID) != nil {
		s.publicarParaCliente(clienteID, msg)
	} else {
		sala.Mutex.Lock()
		sombraAddr := sala.ServidorSombra
		sala.Mutex.Unlock()
		if sombraAddr != "" {
			go s.notificarJogadorRemoto(sombraAddr, clienteID, msg)
		}
	}

	// Marca como pronto na sala
	sala.Mutex.Lock()
	sala.Prontos[cliente.Nome] = true
	sala.Mutex.Unlock()

	// Delega a verificação de início de partida
	go s.verificarEIniciarPartidaSeProntos(sala)
}

func (s *Servidor) iniciarPartida(sala *tipos.Sala) {
	sala.Mutex.Lock()
	sala.Estado = "JOGANDO"
	// Sorteia quem começa
	jogadorInicial := sala.Jogadores[rand.Intn(len(sala.Jogadores))]
	sala.TurnoDe = jogadorInicial.ID
	sala.Mutex.Unlock()

	log.Printf("[JOGO_DEBUG] Partida %s iniciada. Jogador inicial: %s (%s)", sala.ID, jogadorInicial.Nome, jogadorInicial.ID)
	log.Printf("[JOGO_DEBUG] TurnoDe definido como: %s", sala.TurnoDe)

	// Envia um estado inicial completo em vez de apenas uma mensagem de texto
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Partida iniciada! É a vez de %s.", jogadorInicial.Nome),
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  map[string]int{sala.Jogadores[0].Nome: len(sala.Jogadores[0].Inventario), sala.Jogadores[1].Nome: len(sala.Jogadores[1].Inventario)},
			TurnoDe:         sala.TurnoDe,
		}),
	}
	s.publicarEventoPartida(sala.ID, msg)
}

// verificarEIniciarPartidaSeProntos verifica se todos compraram e inicia a partida,
// notificando a Sombra se necessário.
func (s *Servidor) verificarEIniciarPartidaSeProntos(sala *tipos.Sala) {
	sala.Mutex.Lock()
	prontos := len(sala.Prontos)
	total := len(sala.Jogadores)
	estadoAtual := sala.Estado
	sombraAddr := sala.ServidorSombra
	salaID := sala.ID
	sala.Mutex.Unlock()

	// Só inicia se estiver aguardando e todos estiverem prontos
	if estadoAtual == "AGUARDANDO_COMPRA" && prontos == total && total == 2 {
		log.Printf("[INICIAR_PARTIDA:%s] Todos os jogadores da sala %s estão prontos. Iniciando.", s.ServerID, salaID)
		s.iniciarPartida(sala) // Chama a função que muda o estado e notifica localmente

		// Se houver uma sombra, notifica via API que a partida iniciou
		if sombraAddr != "" {
			// Recupera o estado atualizado da sala (principalmente o TurnoDe)
			sala.Mutex.Lock()
			estadoAtualizado := s.criarEstadoDaSala(sala)
			sala.Mutex.Unlock()

			// Envia o estado completo para a sombra
			go func() {
				log.Printf("[SYNC_SOMBRA] Notificando sombra %s sobre início da partida %s. Turno de: %s", sombraAddr, salaID, estadoAtualizado.TurnoDe)
				reqBody, _ := json.Marshal(estadoAtualizado)
				httpClient := &http.Client{Timeout: 10 * time.Second}
				_, err := httpClient.Post(fmt.Sprintf("http://%s/partida/iniciar_remoto", sombraAddr), "application/json", bytes.NewBuffer(reqBody))
				if err != nil {
					log.Printf("[SYNC_SOMBRA_ERRO] Falha ao notificar sombra %s: %v", sombraAddr, err)
				}
			}()
		}
	}
}

// encaminharJogadaParaHost encaminha uma jogada da Sombra para o Host via API REST
func (s *Servidor) encaminharJogadaParaHost(sala *tipos.Sala, clienteID, cartaID string) {
	sala.Mutex.Lock()
	host := sala.ServidorHost
	eventSeq := sala.EventSeq + 1 // Próximo eventSeq
	sala.Mutex.Unlock()

	log.Printf("[SHADOW] Encaminhando jogada de %s para o Host %s (eventSeq: %d)", clienteID, host, eventSeq)

	// Usa o novo endpoint /game/event
	req := tipos.GameEventRequest{
		MatchID:   sala.ID,
		EventSeq:  eventSeq,
		EventType: "CARD_PLAYED",
		PlayerID:  clienteID,
		Data: map[string]interface{}{
			"carta_id": cartaID,
		},
		Token: seguranca.GenerateJWT(s.ServerID),
	}

	// Gera assinatura
	event := tipos.GameEvent{
		EventSeq:  req.EventSeq,
		MatchID:   req.MatchID,
		EventType: req.EventType,
		PlayerID:  req.PlayerID,
	}
	seguranca.SignEvent(&event)
	req.Signature = event.Signature

	jsonData, _ := json.Marshal(req)
	url := fmt.Sprintf("http://%s/game/event", host)

	// Adiciona timeout para detectar falha do Host
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Adiciona header de autenticação
	httpReq, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[FAILOVER] Host %s inacessível: %v. Iniciando promoção da Sombra...", host, err)
		s.promoverSombraAHost(sala)

		// CORREÇÃO: Processa o evento 'req' que já foi criado nesta função (na linha 1182)
		s.processarEventoComoHost(sala, &req)

		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[SHADOW] Host retornou status %d ao processar jogada", resp.StatusCode)
		return
	}

	log.Printf("[SHADOW] Jogada processada pelo Host com sucesso")
}

// promoverSombraAHost promove a Sombra a Host quando o Host original falha
func (s *Servidor) promoverSombraAHost(sala *tipos.Sala) {
	sala.Mutex.Lock()
	defer sala.Mutex.Unlock()

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
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: "O servidor da partida falhou. A partida continuará em um servidor reserva.",
		}),
	}
	s.publicarEventoPartida(sala.ID, msg)
}

// (No arquivo servidor/main.go)

// SUBSTITUA a função 'processarJogadaComoHost' inteira por esta:
func (s *Servidor) processarEventoComoHost(sala *tipos.Sala, evento *tipos.GameEventRequest) *tipos.EstadoPartida {
	timestamp := time.Now().Format("15:04:05.000")
	log.Printf("[%s][EVENTO_HOST:%s] === INÍCIO PROCESSAMENTO EVENTO: %s ===", timestamp, sala.ID, evento.EventType)
	log.Printf("[%s][EVENTO_HOST:%s] Cliente: %s", timestamp, sala.ID, evento.PlayerID)
	defer log.Printf("[%s][EVENTO_HOST:%s] === FIM PROCESSAMENTO EVENTO: %s ===", timestamp, sala.ID, evento.EventType)

	log.Printf("[%s][EVENTO_HOST:%s] TENTANDO LOCK DA SALA...", timestamp, sala.ID)
	sala.Mutex.Lock()
	log.Printf("[%s][EVENTO_HOST:%s] LOCK DA SALA OBTIDO", timestamp, sala.ID)
	defer func() {
		log.Printf("[%s][EVENTO_HOST:%s] LIBERANDO LOCK DA SALA...", timestamp, sala.ID)
		sala.Mutex.Unlock()
		log.Printf("[%s][EVENTO_HOST:%s] LOCK DA SALA LIBERADO", timestamp, sala.ID)
	}()

	// Validação de Evento (do ARQUITETURA_CROSS_SERVER.md)
	// O Host é a autoridade sobre o eventSeq.
	if evento.EventSeq <= sala.EventSeq {
		log.Printf("[EVENTO_HOST:%s] Evento desatualizado ou duplicado recebido. Seq Recebido: %d, Seq Atual: %d", sala.ID, evento.EventSeq, sala.EventSeq)
		return nil // Ignora o evento (idealmente, o handler da API retornaria 409 Conflict)
	}

	// Validação de turno (APENAS para jogadas de carta)
	if evento.EventType == "CARD_PLAYED" {
		if sala.Estado != "JOGANDO" {
			log.Printf("[JOGO_ERRO:%s] Partida não está em andamento.", sala.ID)
			s.notificarErroPartida(evento.PlayerID, "A partida não está em andamento.", sala.ID)
			return nil
		}
		if evento.PlayerID != sala.TurnoDe {
			log.Printf("[JOGO_AVISO] Jogada fora de turno. Cliente: %s, Turno de: %s", evento.PlayerID, sala.TurnoDe)
			s.notificarErroPartida(evento.PlayerID, "Não é sua vez de jogar.", sala.ID)
			return nil
		}
	}

	// O Host define o eventSeq oficial
	sala.EventSeq++
	currentEventSeq := sala.EventSeq

	// Encontra o cliente
	var jogador *tipos.Cliente
	var nomeJogador string
	for _, c := range sala.Jogadores {
		if c.ID == evento.PlayerID {
			jogador = c
			nomeJogador = c.Nome
			break
		}
	}
	if jogador == nil {
		log.Printf("[EVENTO_HOST:%s] Cliente %s não encontrado na sala", sala.ID, evento.PlayerID)
		return nil
	}

	// Registra o evento no log (agora genérico)
	logEvent := tipos.GameEvent{
		EventSeq:  currentEventSeq, // Usa o eventSeq oficial do Host
		MatchID:   sala.ID,
		Timestamp: time.Now(),
		EventType: evento.EventType,
		PlayerID:  evento.PlayerID,
		Data:      evento.Data,
	}
	seguranca.SignEvent(&logEvent)
	sala.EventLog = append(sala.EventLog, logEvent)

	// --- LÓGICA DE CADA EVENTO ---
	switch evento.EventType {

	case "CARD_PLAYED":
		// CORREÇÃO: Primeiro, faça a asserção de tipo do 'Data' para um map
		dadosDoEvento, ok := evento.Data.(map[string]interface{})
		if !ok {
			log.Printf("[EVENTO_HOST:%s] Evento CARD_PLAYED com 'Data' mal formatado (esperado map[string]interface{}, obteve %T)", sala.ID, evento.Data)
			return nil
		}

		// Agora sim, acesse o map 'dadosDoEvento'
		cartaID, ok := dadosDoEvento["carta_id"].(string)
		if !ok {
			log.Printf("[EVENTO_HOST:%s] Evento CARD_PLAYED sem 'carta_id' no 'Data'", sala.ID)
			return nil
		}

		if _, jaJogou := sala.CartasNaMesa[nomeJogador]; jaJogou {
			log.Printf("[HOST] Jogador %s já jogou nesta rodada", nomeJogador)
			return nil
		}

		jogador.Mutex.Lock()
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
			jogador.Mutex.Unlock()
			log.Printf("[HOST] Carta %s não encontrada no inventário de %s", cartaID, nomeJogador)
			return nil
		}
		jogador.Inventario = append(jogador.Inventario[:cartaIndex], jogador.Inventario[cartaIndex+1:]...)
		jogador.Mutex.Unlock()
		sala.CartasNaMesa[nomeJogador] = carta

		log.Printf("[HOST] Jogador %s jogou carta %s (Poder: %d) - eventSeq: %d", nomeJogador, carta.Nome, carta.Valor, currentEventSeq)

		if len(sala.CartasNaMesa) == len(sala.Jogadores) {
			s.resolverJogada(sala)
		} else {
			for _, j := range sala.Jogadores {
				if j.ID != evento.PlayerID {
					log.Printf("[TURNO:%s] Jogador %s jogou. Próximo a jogar: %s (%s)", sala.ID, evento.PlayerID, j.Nome, j.ID)
					s.mudarTurnoAtomicamente(sala, j.ID)
					break
				}
			}
			s.notificarAguardandoOponente(sala) // Notifica que a jogada foi feita, mas espera o outro
		}

	case "PLAYER_READY":
		sala.Prontos[nomeJogador] = true
		log.Printf("[HOST] Jogador %s (%s) está PRONTO (evento recebido). Prontos: %d/%d", nomeJogador, evento.PlayerID, len(sala.Prontos), len(sala.Jogadores))
		// Usamos goroutine para liberar o lock da sala rapidamente
		go s.verificarEIniciarPartidaSeProntos(sala)

	case "CHAT":
		// CORREÇÃO: Primeiro, faça a asserção de tipo
		dadosDoEvento, ok := evento.Data.(map[string]interface{})
		if !ok {
			log.Printf("[EVENTO_HOST:%s] Evento CHAT com 'Data' mal formatado (esperado map[string]interface{}, obteve %T)", sala.ID, evento.Data)
			return nil
		}

		// Agora, acesse o map 'dadosDoEvento'
		texto, ok := dadosDoEvento["texto"].(string)
		if !ok {
			log.Printf("[EVENTO_HOST:%s] Evento CHAT sem 'texto' no 'Data'", sala.ID)
			return nil
		}
		log.Printf("[HOST-CHAT] Recebido evento de chat de %s. Fazendo broadcast.", nomeJogador)
		// Usamos goroutine para liberar o lock da sala rapidamente
		// (A função broadcastChat deve ser atualizada para publicar no MQTT da partida)
		go s.broadcastChat(sala, texto, nomeJogador)

	} // Fim do switch

	// --- REPLICAÇÃO ---
	estado := &tipos.EstadoPartida{
		SalaID:        sala.ID,
		Estado:        sala.Estado,
		CartasNaMesa:  sala.CartasNaMesa,
		PontosRodada:  sala.PontosRodada,
		PontosPartida: sala.PontosPartida,
		NumeroRodada:  sala.NumeroRodada,
		Prontos:       sala.Prontos,
		EventSeq:      currentEventSeq,
		EventLog:      sala.EventLog,
		TurnoDe:       sala.TurnoDe,
	}

	if sala.ServidorSombra != "" && sala.ServidorSombra != s.MeuEndereco {
		go s.replicarEstadoParaShadow(sala.ServidorSombra, estado)
	}

	// (A lógica de notificação que estava aqui foi movida para dentro do case "CARD_PLAYED"
	// ou é tratada pela replicação/broadcast)

	return estado
}

// replicarEstadoParaShadow replica o estado para o servidor Shadow usando o endpoint /game/replicate
func (s *Servidor) replicarEstadoParaShadow(shadowAddr string, estado *tipos.EstadoPartida) {
	req := tipos.GameReplicateRequest{
		MatchID:  estado.SalaID,
		EventSeq: estado.EventSeq,
		State:    *estado,
		Token:    seguranca.GenerateJWT(s.ServerID),
	}

	// Gera assinatura
	data := fmt.Sprintf("%s:%d", req.MatchID, req.EventSeq)
	req.Signature = seguranca.GenerateHMAC(data, JWT_SECRET)

	jsonData, _ := json.Marshal(req)
	url := fmt.Sprintf("http://%s/game/replicate", shadowAddr)

	httpClient := &http.Client{Timeout: 10 * time.Second}

	httpReq, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[HOST] Erro ao replicar estado para Shadow %s: %v", shadowAddr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("[HOST] Estado replicado com sucesso para Shadow %s (eventSeq: %d)", shadowAddr, estado.EventSeq)
	} else {
		log.Printf("[HOST] Shadow %s retornou status %d ao receber replicação", shadowAddr, resp.StatusCode)
	}
}

// resolverJogada resolve uma jogada quando ambos os jogadores jogaram
func (s *Servidor) resolverJogada(sala *tipos.Sala) {
	// IMPORTANTE: Esta função assume que o `sala.Mutex` JÁ ESTÁ BLOQUEADO pela função que a chamou (ex: processarJogadaComoHost)
	log.Printf("[JOGADA_RESOLVER:%s] Resolvendo jogada...", sala.ID)

	// Garante que a operação seja atômica na sala
	// sala.Mutex.Lock() <--- REMOVIDO PARA EVITAR DEADLOCK
	// defer sala.Mutex.Unlock() <--- REMOVIDO

	if len(sala.CartasNaMesa) != 2 {
		log.Printf("[JOGO_ERRO:%s] Tentativa de resolver jogada com %d cartas na mesa.", sala.ID, len(sala.CartasNaMesa))
		return
	}

	j1 := sala.Jogadores[0]
	j2 := sala.Jogadores[1]

	c1 := sala.CartasNaMesa[j1.Nome]
	c2 := sala.CartasNaMesa[j2.Nome]

	vencedorJogada := "EMPATE"
	var vencedor *tipos.Cliente

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

	// CORREÇÃO: Define o próximo turno baseado no vencedor da jogada
	if vencedor != nil {
		log.Printf("[TURNO:%s] Vencedor da jogada: %s. Próximo turno: %s", sala.ID, vencedorJogada, vencedor.Nome)
		s.mudarTurnoAtomicamente(sala, vencedor.ID)
	} else {
		// Em caso de empate, mantém o mesmo jogador
		log.Printf("[TURNO:%s] Empate na jogada. Mantendo turno atual: %s", sala.ID, sala.TurnoDe)
		// Notificação será feita pela função chamadora após liberar o lock
	}

	// Notifica jogadores
	s.notificarResultadoJogada(sala, vencedorJogada)

	// Verifica se acabaram as cartas
	j1.Mutex.Lock()
	j1Cartas := len(j1.Inventario)
	j1.Mutex.Unlock()

	j2.Mutex.Lock()
	j2Cartas := len(j2.Inventario)
	j2.Mutex.Unlock()

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
func (s *Servidor) notificarAguardandoOponente(sala *tipos.Sala) {
	// IMPORTANTE: Esta função assume que o `sala.Mutex` JÁ ESTÁ BLOQUEADO pela função que a chamou.
	timestamp := time.Now().Format("15:04:05.000")
	log.Printf("[%s][NOTIFICACAO:%s] === INÍCIO NOTIFICAÇÃO AGUARDANDO OPONENTE ===", timestamp, sala.ID)
	log.Printf("[%s][NOTIFICACAO:%s] Publicando atualização de aguardo de jogada.", timestamp, sala.ID)

	// Encontra o nome do jogador que deve jogar
	var proximoJogadorNome string
	for _, j := range sala.Jogadores {
		if j.ID == sala.TurnoDe {
			proximoJogadorNome = j.Nome
			break
		}
	}

	// CORREÇÃO: Criar contagem de cartas atualizada
	contagemCartas := make(map[string]int)
	for _, j := range sala.Jogadores {
		j.Mutex.Lock()
		contagemCartas[j.Nome] = len(j.Inventario)
		j.Mutex.Unlock()
	}

	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Aguardando jogada de %s...", proximoJogadorNome),
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    sala.CartasNaMesa,
			TurnoDe:         sala.TurnoDe,
		}),
	}

	log.Printf("[%s][NOTIFICACAO:%s] Enviando mensagem para tópico partidas/%s/eventos", timestamp, sala.ID, sala.ID)
	log.Printf("[%s][NOTIFICACAO:%s] Conteúdo da mensagem: %+v", timestamp, sala.ID, msg)
	s.publicarEventoPartida(sala.ID, msg)
	log.Printf("[%s][NOTIFICACAO:%s] === FIM NOTIFICAÇÃO AGUARDANDO OPONENTE ===", timestamp, sala.ID)
}

// notificarResultadoJogada notifica o resultado de uma jogada
func (s *Servidor) notificarResultadoJogada(sala *tipos.Sala, vencedorJogada string) {
	// IMPORTANTE: Esta função assume que o `sala.Mutex` JÁ ESTÁ BLOQUEADO pela função que a chamou (resolverJogada).
	log.Printf("[NOTIFICACAO:%s] Publicando resultado da jogada. Vencedor: %s", sala.ID, vencedorJogada)

	// Cria contagem de cartas (dentro do lock existente)
	contagemCartas := make(map[string]int)
	for _, j := range sala.Jogadores {
		// Acesso ao inventário de um jogador de outra goroutine requer seu próprio lock
		j.Mutex.Lock()
		contagemCartas[j.Nome] = len(j.Inventario)
		j.Mutex.Unlock()
	}

	// CORREÇÃO: Encontra o nome do próximo jogador para a mensagem
	var proximoJogadorNome string
	for _, j := range sala.Jogadores {
		if j.ID == sala.TurnoDe {
			proximoJogadorNome = j.Nome
			break
		}
	}

	// Cria a mensagem base
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Vencedor da jogada: %s. Próximo a jogar: %s", vencedorJogada, proximoJogadorNome),
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    sala.CartasNaMesa,
			VencedorJogada:  vencedorJogada,
			SalaID:          sala.ID,
			TurnoDe:         sala.TurnoDe,
		}),
	}

	s.publicarEventoPartida(sala.ID, msg)
}

// finalizarPartida finaliza uma partida e determina o vencedor
func (s *Servidor) finalizarPartida(sala *tipos.Sala) {
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
		Dados:   seguranca.MustJSON(protocolo.DadosFimDeJogo{VencedorNome: vencedorFinal, SalaID: sala.ID}),
	}

	sala.Mutex.Lock()
	jogadores := make([]*tipos.Cliente, len(sala.Jogadores))
	copy(jogadores, sala.Jogadores)
	sombraAddr := sala.ServidorSombra
	sala.Mutex.Unlock()

	for _, jogador := range jogadores {
		if s.getClienteLocal(jogador.ID) != nil {
			s.publicarParaCliente(jogador.ID, msg)
		} else {
			if sombraAddr != "" {
				go s.notificarJogadorRemoto(sombraAddr, jogador.ID, msg)
			}
		}
	}
}

// sincronizarEstadoComSombra envia o estado atualizado da partida para a Sombra
func (s *Servidor) sincronizarEstadoComSombra(sombra string, estado *tipos.EstadoPartida) {
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

func (s *Servidor) enviarAtualizacaoParaSombra(sombraAddr string, msg protocolo.Mensagem) {
	log.Printf("[SYNC] Enviando atualização de jogo para a sombra %s", sombraAddr)
	jsonData, _ := json.Marshal(msg)
	url := fmt.Sprintf("http://%s/partida/atualizar_estado", sombraAddr)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Erro ao enviar atualização para a sombra %s: %v", sombraAddr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Sombra %s retornou status %d ao receber atualização.", sombraAddr, resp.StatusCode)
	}
}

func (s *Servidor) broadcastChat(sala *tipos.Sala, texto, remetenteNome string) {
	log.Printf("[HOST-CHAT] Retransmitindo chat de '%s' para sala %s", remetenteNome, sala.ID)

	msgChat := protocolo.Mensagem{
		Comando: "CHAT_RECEBIDO", // O cliente já trata CHAT_RECEBIDO (em handleMensagemServidor)
		Dados: seguranca.MustJSON(protocolo.DadosReceberChat{
			NomeJogador: remetenteNome,
			Texto:       texto,
		}),
	}

	// Publica no tópico de eventos da PARTIDA.
	// Ambos os clientes (no Host e no Shadow) estão inscritos neste tópico
	// (ver cliente/main.go -> case "PARTIDA_ENCONTRADA")
	s.publicarEventoPartida(sala.ID, msgChat)
}

// ==================== LÓGICA DE TROCA DE CARTAS ====================

func (s *Servidor) processarTrocaCartas(sala *tipos.Sala, req *protocolo.TrocarCartasReq) {
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
	jogadorOferta.Mutex.Lock()
	jogadorDesejado.Mutex.Lock()
	jogadorOferta.Inventario[idxOferta], jogadorDesejado.Inventario[idxDesejado] = jogadorDesejado.Inventario[idxDesejado], jogadorOferta.Inventario[idxOferta]
	jogadorDesejado.Mutex.Unlock()
	jogadorOferta.Mutex.Unlock()

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

func (s *Servidor) getClienteDaSala(sala *tipos.Sala, clienteID string) *tipos.Cliente {
	sala.Mutex.Lock()
	defer sala.Mutex.Unlock()
	for _, jogador := range sala.Jogadores {
		if jogador.ID == clienteID {
			return jogador
		}
	}
	return nil
}

func (s *Servidor) findCartaNoInventario(cliente *tipos.Cliente, cartaID string) (Carta, int) {
	cliente.Mutex.Lock()
	defer cliente.Mutex.Unlock()
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
		Dados:   seguranca.MustJSON(protocolo.DadosErro{Mensagem: mensagem}),
	})
}

func (s *Servidor) notificarSucessoTroca(clienteID, cartaPerdida, cartaGanha string) {
	msg := fmt.Sprintf("Troca realizada! Você deu '%s' e recebeu '%s'.", cartaPerdida, cartaGanha)
	resp := protocolo.TrocarCartasResp{Sucesso: true, Mensagem: msg}
	s.publicarParaCliente(clienteID, protocolo.Mensagem{Comando: "TROCA_CONCLUIDA", Dados: seguranca.MustJSON(resp)})
}

func (s *Servidor) notificarJogadorRemoto(servidor string, clienteID string, msg protocolo.Mensagem) {
	log.Printf("[NOTIFICACAO-REMOTA] Notificando cliente %s no servidor %s", clienteID, servidor)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"cliente_id": clienteID,
		"mensagem":   msg,
	})
	httpClient := &http.Client{Timeout: 10 * time.Second}
	httpClient.Post(fmt.Sprintf("http://%s/partida/notificar_jogador", servidor), "application/json", bytes.NewBuffer(reqBody))
}

func (s *Servidor) getClienteLocal(clienteID string) *tipos.Cliente {
	s.mutexClientes.RLock()
	defer s.mutexClientes.RUnlock()
	// Verifica se o cliente está conectado a este servidor
	cliente, ok := s.Clientes[clienteID]
	if ok {
		return cliente
	}
	return nil
}

func (s *Servidor) criarEstadoDaSala(sala *tipos.Sala) *tipos.EstadoPartida {
	sala.Mutex.Lock()
	defer sala.Mutex.Unlock()

	// Simplificado para apenas o necessário
	return &tipos.EstadoPartida{
		SalaID:  sala.ID,
		Estado:  sala.Estado,
		TurnoDe: sala.TurnoDe,
	}
}

func (s *Servidor) notificarErroPartida(clienteID, mensagem, salaID string) {
	dados := protocolo.DadosErro{Mensagem: mensagem}
	msg := protocolo.Mensagem{
		Comando: "ERRO_JOGADA",
		Dados:   seguranca.MustJSON(dados),
	}
	// O erro é publicado no tópico de eventos da partida para que ambos os jogadores possam vê-lo, se necessário,
	// ou apenas para o cliente específico. Optarei por notificar apenas o cliente que cometeu o erro.
	s.publicarParaCliente(clienteID, msg)
}

// ==================== UTILITÁRIOS ====================

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// CORREÇÃO: Função auxiliar para mudança atômica de turno
func (s *Servidor) mudarTurnoAtomicamente(sala *tipos.Sala, novoJogadorID string) {
	// Encontra o nome do novo jogador
	var novoJogadorNome string
	for _, j := range sala.Jogadores {
		if j.ID == novoJogadorID {
			novoJogadorNome = j.Nome
			break
		}
	}

	// Atualiza o turno
	sala.TurnoDe = novoJogadorID
	log.Printf("[TURNO_ATOMICO:%s] Turno alterado para: %s (%s)", sala.ID, novoJogadorNome, novoJogadorID)

	// CORREÇÃO: Notificação deve ser feita FORA do lock da sala para evitar deadlock
	// A notificação será feita pela função chamadora após liberar o lock
}

// A estrutura Servidor agora implementa implicitamente a api.ServidorInterface

func (s *Servidor) RegistrarServidor(info *tipos.InfoServidor) {
	// s.mutexServidores.Lock()
	// s.Servidores[info.Endereco] = info
	// s.mutexServidores.Unlock()
}

func (s *Servidor) GetServidores() map[string]*tipos.InfoServidor {
	// s.mutexServidores.RLock()
	// defer s.mutexServidores.RUnlock()
	// return s.Servidores
	return nil // Placeholder
}

func (s *Servidor) ProcessarHeartbeat(endereco string, dados map[string]interface{}) {
	// Lógica movida para cluster.Manager
}

func (s *Servidor) ProcessarVoto(candidato string, termo int64) (bool, int64) {
	// Lógica movida para cluster.Manager
	return false, 0
}

func (s *Servidor) DeclararLider(novoLider string, termo int64) {
	// Lógica movida para cluster.Manager
}

func (s *Servidor) SouLider() bool {
	// Lógica movida para cluster.Manager
	return false
}

func (s *Servidor) EncaminharParaLider(c *gin.Context) {
	liderAddr := s.ClusterManager.GetLider()

	if liderAddr == "" || liderAddr == s.MeuEndereco { // Adicionada verificação para não encaminhar para si mesmo
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Líder não disponível no momento"})
		return
	}

	url := fmt.Sprintf("http://%s%s", liderAddr, c.Request.URL.Path)
	proxyReq, err := http.NewRequest(c.Request.Method, url, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Falha ao criar proxy da requisição"})
		return
	}

	proxyReq.Header = c.Request.Header
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Falha ao encaminhar requisição para o líder"})
		return
	}
	defer resp.Body.Close()

	c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}

func (s *Servidor) FormarPacote() ([]tipos.Carta, error) {
	return s.Store.FormarPacote(PACOTE_SIZE), nil
}
