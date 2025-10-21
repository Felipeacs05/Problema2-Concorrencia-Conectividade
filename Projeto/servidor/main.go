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
}

func (s *Servidor) CriarSalaRemota(solicitante, oponente *tipos.Cliente) {
	s.criarSala(solicitante, oponente)
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
	// Extrai o ID da sala do tópico
	topico := msg.Topic()
	// topico formato: "partidas/{salaID}/comandos"
	partes := strings.Split(topico, "/")
	if len(partes) < 2 {
		log.Printf("Tópico inválido: %s", topico)
		return
	}
	salaID := partes[1]

	log.Printf("[COMANDO_DEBUG] Comando recebido no tópico: %s", topico)
	log.Printf("[COMANDO_DEBUG] Payload: %s", string(msg.Payload()))

	var mensagem protocolo.Mensagem
	if err := json.Unmarshal(msg.Payload(), &mensagem); err != nil {
		log.Printf("Erro ao decodificar comando: %v", err)
		return
	}

	log.Printf("[COMANDO_DEBUG] Comando decodificado: %s", mensagem.Comando)

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

	case "CHAT":
		var dadosCliente protocolo.DadosEnviarChat
		if err := json.Unmarshal(mensagem.Dados, &dadosCliente); err != nil {
			log.Printf("[CHAT_ERRO] Erro ao decodificar dados do chat: %v", err)
			return
		}

		clienteID := dadosCliente.ClienteID
		s.mutexClientes.RLock()
		cliente := s.Clientes[clienteID]
		s.mutexClientes.RUnlock()
		if cliente == nil {
			return // Early exit se o cliente não for encontrado
		}
		cliente.Mutex.Lock()
		nomeJogador := cliente.Nome
		cliente.Mutex.Unlock()

		if nomeJogador == "" {
			log.Printf("[CHAT_ERRO] Nome do jogador para cliente %s não encontrado.", clienteID)
			return
		}

		s.broadcastChat(sala, dadosCliente.Texto, nomeJogador)

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
		s.criarSala(oponente, cliente)
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

	// Aguarda um pouco antes de iniciar busca global para dar chance de matchmaking local
	go func() {
		time.Sleep(2 * time.Second) // Aguarda 2 segundos

		// Verifica se ainda está na fila local antes de iniciar busca global
		s.mutexFila.Lock()
		aindaNaFila := false
		for _, c := range s.FilaDeEspera {
			if c.ID == cliente.ID {
				aindaNaFila = true
				break
			}
		}
		s.mutexFila.Unlock()

		if aindaNaFila {
			log.Printf("[MATCHMAKING] Iniciando busca global persistente para %s (%s)", cliente.Nome, cliente.ID)
			s.gerenciarBuscaGlobalPersistente(cliente)
		}
	}()
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
		oponenteRemoto := &tipos.Cliente{
			ID:   res.OponenteID,
			Nome: res.OponenteNome,
		}

		// Cria a sala. O servidor remoto será o Host.
		s.criarSala(cliente, oponenteRemoto) // `cliente` é o jogador local

		return true // Sucesso!
	}

	log.Printf("[MATCHMAKING-TX] Servidor %s não encontrou oponente.", addr)
	return false
}

func (s *Servidor) criarSala(j1, j2 *tipos.Cliente) {
	salaID := uuid.New().String()

	// --- CORREÇÃO: Buscar nomes de forma segura ---
	s.mutexClientes.RLock()
	cliente1Completo, ok1 := s.Clientes[j1.ID]
	cliente2Completo, ok2 := s.Clientes[j2.ID]
	s.mutexClientes.RUnlock()

	if !ok1 || !ok2 {
		log.Printf("[CRIAR_SALA_ERRO:%s] Falha ao buscar informações completas dos clientes %s ou %s no mapa principal.", s.ServerID, j1.ID, j2.ID)
		return // Aborta a criação da sala
	}

	// Usa os nomes dos clientes buscados do mapa principal
	// Leitura segura dos nomes dentro do lock do cliente específico
	cliente1Completo.Mutex.Lock()
	nomeJ1 := cliente1Completo.Nome
	cliente1Completo.Mutex.Unlock()
	cliente2Completo.Mutex.Lock()
	nomeJ2 := cliente2Completo.Nome
	cliente2Completo.Mutex.Unlock()

	idJ1 := cliente1Completo.ID
	idJ2 := cliente2Completo.ID

	log.Printf("[CRIAR_SALA:%s] Tentando criar sala %s para %s (%s) vs %s (%s)", s.ServerID, salaID, nomeJ1, idJ1, nomeJ2, idJ2)
	if nomeJ1 == "" || nomeJ2 == "" {
		log.Printf("[CRIAR_SALA_ERRO:%s] Nomes vazios detectados mesmo após busca! Cliente1: '%s', Cliente2: '%s'. Abortando.", s.ServerID, nomeJ1, nomeJ2)
		return // Aborta se os nomes ainda estiverem vazios
	}
	// --- FIM DA CORREÇÃO ---

	// O resto da função continua...
	novaSala := &tipos.Sala{
		ID:             salaID,
		Jogadores:      []*tipos.Cliente{cliente1Completo, cliente2Completo}, // Usa os ponteiros completos
		Estado:         "AGUARDANDO_COMPRA",
		CartasNaMesa:   make(map[string]Carta),
		PontosRodada:   make(map[string]int),
		PontosPartida:  make(map[string]int),
		NumeroRodada:   1,
		Prontos:        make(map[string]bool),
		ServidorHost:   s.MeuEndereco,
		ServidorSombra: "",
	}
	// Adiciona a sala ao mapa
	s.mutexSalas.Lock()
	s.Salas[salaID] = novaSala
	s.mutexSalas.Unlock()

	// Associa os clientes à sala
	cliente1Completo.Mutex.Lock()
	cliente1Completo.Sala = novaSala
	cliente1Completo.Mutex.Unlock()

	cliente2Completo.Mutex.Lock()
	cliente2Completo.Sala = novaSala
	cliente2Completo.Mutex.Unlock()

	log.Printf("Sala %s criada localmente: %s vs %s", salaID, nomeJ1, nomeJ2)

	// Notifica jogadores - GARANTE QUE USA OS NOMES CORRETOS
	msg1 := protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   seguranca.MustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: idJ2, OponenteNome: nomeJ2}),
	}
	msg2 := protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   seguranca.MustJSON(protocolo.DadosPartidaEncontrada{SalaID: salaID, OponenteID: idJ1, OponenteNome: nomeJ1}),
	}

	log.Printf("[CRIAR_SALA:NOTIFICACAO] Enviando PARTIDA_ENCONTRADA para %s (ID: %s)", nomeJ1, idJ1)
	s.publicarParaCliente(j1.ID, msg1)
	log.Printf("[CRIAR_SALA:NOTIFICACAO] Enviando PARTIDA_ENCONTRADA para %s (ID: %s)", nomeJ2, idJ2)
	s.publicarParaCliente(j2.ID, msg2)
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

	msg := protocolo.Mensagem{
		Comando: "PARTIDA_INICIADA",
		Dados:   seguranca.MustJSON(map[string]string{"mensagem": "Partida iniciada! É a vez de " + jogadorInicial.Nome}),
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
		s.processarJogadaComoHost(sala, clienteID, cartaID) // Processa a jogada como o novo Host
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

// processarJogadaComoHost processa uma jogada quando este servidor é o Host
func (s *Servidor) processarJogadaComoHost(sala *tipos.Sala, clienteID, cartaID string) *tipos.EstadoPartida {
	s.mutexSalas.RLock()
	sala, ok := s.Salas[sala.ID]
	s.mutexSalas.RUnlock()
	if !ok {
		log.Printf("[JOGO_ERRO] Sala %s não encontrada para processar jogada.", sala.ID)
		return nil
	}

	log.Printf("[JOGO_DEBUG] Host processando jogada. Sala: %s, Cliente: %s, Carta: %s", sala.ID, clienteID, cartaID)

	sala.Mutex.Lock()
	defer sala.Mutex.Unlock()

	if sala.Estado != "JOGANDO" {
		log.Printf("[JOGO_ERRO] Partida %s não está em andamento.", sala.ID)
		return nil
	}

	if clienteID != sala.TurnoDe {
		log.Printf("[JOGO_AVISO] Jogada fora de turno. Cliente: %s, Turno de: %s", clienteID, sala.TurnoDe)
		s.notificarErroPartida(clienteID, "Não é sua vez de jogar.", sala.ID)
		return nil
	}

	// Incrementa eventSeq
	sala.EventSeq++
	currentEventSeq := sala.EventSeq

	// Encontra o cliente
	var jogador *tipos.Cliente
	var nomeJogador string
	for _, c := range sala.Jogadores {
		if c.ID == clienteID {
			jogador = c
			nomeJogador = c.Nome
			break
		}
	}

	if jogador == nil {
		log.Printf("[HOST] Cliente %s não encontrado na sala", clienteID)
		return nil
	}

	// Verifica se já jogou nesta rodada
	if _, jaJogou := sala.CartasNaMesa[nomeJogador]; jaJogou {
		log.Printf("[HOST] Jogador %s já jogou nesta rodada", nomeJogador)
		return nil
	}

	// Encontra a carta no inventário
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

	// Remove a carta do inventário
	jogador.Inventario = append(jogador.Inventario[:cartaIndex], jogador.Inventario[cartaIndex+1:]...)
	jogador.Mutex.Unlock()

	// Adiciona carta à mesa
	sala.CartasNaMesa[nomeJogador] = carta

	// Registra evento no log
	event := tipos.GameEvent{
		EventSeq:  currentEventSeq,
		MatchID:   sala.ID,
		Timestamp: time.Now(),
		EventType: "CARD_PLAYED",
		PlayerID:  clienteID,
		Data: map[string]interface{}{
			"carta_id":    cartaID,
			"carta_nome":  carta.Nome,
			"carta_valor": carta.Valor,
		},
	}
	seguranca.SignEvent(&event)
	sala.EventLog = append(sala.EventLog, event)

	log.Printf("[HOST] Jogador %s jogou carta %s (Poder: %d) - eventSeq: %d", nomeJogador, carta.Nome, carta.Valor, currentEventSeq)

	// Verifica se ambos jogaram
	if len(sala.CartasNaMesa) == len(sala.Jogadores) {
		// Resolve a jogada
		s.resolverJogada(sala)
	} else {
		// Notifica que está aguardando o oponente
		s.notificarAguardandoOponente(sala)
	}

	// Cria estado da partida para sincronização
	estado := &tipos.EstadoPartida{
		SalaID:        sala.ID,
		Estado:        sala.Estado,
		CartasNaMesa:  sala.CartasNaMesa,
		PontosRodada:  sala.PontosRodada,
		PontosPartida: sala.PontosPartida,
		NumeroRodada:  sala.NumeroRodada,
		Prontos:       sala.Prontos,
		EventSeq:      sala.EventSeq,
		EventLog:      sala.EventLog,
	}

	// Sincroniza com a Sombra usando o novo endpoint
	if sala.ServidorSombra != "" && sala.ServidorSombra != s.MeuEndereco {
		go s.replicarEstadoParaShadow(sala.ServidorSombra, estado)
	}

	// --- ADICIONE O CÓDIGO ABAIXO ---
	// Após processar a jogada, pega a última mensagem enviada aos clientes locais
	// e a retransmite para o servidor Sombra, se houver.

	// (Esta parte assume que as funções notificar... publicam a mensagem correta.
	// Precisamos capturar essa mensagem ou reconstruí-la aqui)

	// Reconstrói a mensagem de atualização para enviar à Sombra
	contagemCartas := make(map[string]int)
	ultimaJogada := make(map[string]Carta) // Precisamos das cartas jogadas nesta rodada
	vencedorDaJogada := ""                 // Precisamos do resultado da última jogada

	// Rebusca informações atualizadas (simplificado, idealmente viria do resolverJogada)
	for _, j := range sala.Jogadores {
		j.Mutex.Lock()
		contagemCartas[j.Nome] = len(j.Inventario)
		j.Mutex.Unlock()
		if c, ok := sala.CartasNaMesa[j.Nome]; ok { // Pega cartas da mesa se ainda não foram limpas
			ultimaJogada[j.Nome] = c
		}
	}
	// Lógica para determinar vencedorDaJogada se aplicável (precisa ser extraída de resolverJogada)

	msgUpdate := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Jogada de %s processada.", nomeJogador), // Mensagem genérica
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    ultimaJogada,
			VencedorJogada:  vencedorDaJogada, // Pode estar vazio se só um jogou
			PontosRodada:    sala.PontosRodada,
			PontosPartida:   sala.PontosPartida,
		}),
	}

	// Envia para a Sombra retransmitir
	if sala.ServidorSombra != "" && sala.ServidorSombra != s.MeuEndereco {
		var jogadorRemotoID string
		// Encontra o ID do jogador que NÃO é o que acabou de jogar (o destinatário remoto)
		for _, jogador := range sala.Jogadores {
			if jogador.ID != clienteID { // Encontra o ID do outro jogador na sala
				jogadorRemotoID = jogador.ID
				break
			}
		}
		if jogadorRemotoID != "" {
			go s.notificarJogadorRemoto(sala.ServidorSombra, jogadorRemotoID, msgUpdate)
		}
	}
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
	if len(sala.Jogadores) != 2 {
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
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: "Aguardando oponente jogar...",
			NumeroRodada:    sala.NumeroRodada,
			UltimaJogada:    make(map[string]Carta), // Esconde cartas até ambos jogarem
			SalaID:          sala.ID,                // Inclui o SalaID para a Sombra
		}),
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

// notificarResultadoJogada notifica o resultado de uma jogada
func (s *Servidor) notificarResultadoJogada(sala *tipos.Sala, vencedorJogada string) {
	// Cria contagem de cartas
	contagemCartas := make(map[string]int)
	for _, j := range sala.Jogadores {
		j.Mutex.Lock()
		contagemCartas[j.Nome] = len(j.Inventario)
		j.Mutex.Unlock()
	}

	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Vencedor da jogada: %s", vencedorJogada),
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    sala.CartasNaMesa,
			VencedorJogada:  vencedorJogada,
			PontosRodada:    sala.PontosRodada,
			PontosPartida:   sala.PontosPartida,
			SalaID:          sala.ID, // Inclui o SalaID para a Sombra
		}),
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

// broadcastChat envia uma mensagem de chat para todos na sala, local ou remotamente.
func (s *Servidor) broadcastChat(sala *tipos.Sala, texto, remetenteNome string) {
	sala.Mutex.Lock()
	jogadores := make([]*tipos.Cliente, len(sala.Jogadores))
	copy(jogadores, sala.Jogadores)
	sombraAddr := sala.ServidorSombra
	sala.Mutex.Unlock()

	msgChat := protocolo.Mensagem{
		Comando: "CHAT_RECEBIDO",
		Dados: seguranca.MustJSON(protocolo.DadosReceberChat{
			NomeJogador: remetenteNome,
			Texto:       texto,
		}),
	}

	for _, jogador := range jogadores {
		if s.getClienteLocal(jogador.ID) != nil {
			s.publicarParaCliente(jogador.ID, msgChat)
		} else {
			if sombraAddr != "" {
				go s.notificarJogadorRemoto(sombraAddr, jogador.ID, msgChat)
			}
		}
	}
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
