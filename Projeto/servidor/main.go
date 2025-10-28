package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

// AjustarContagemCartasLocal ajusta a contagem de cartas em mensagens ATUALIZACAO_JOGO
// usando os inventários LOCAIS dos jogadores (útil para Shadow server)
func (s *Servidor) AjustarContagemCartasLocal(clienteID string, msg *protocolo.Mensagem) {
	if msg.Comando != "ATUALIZACAO_JOGO" {
		return
	}

	// Busca a sala do cliente
	s.mutexSalas.Lock()
	var sala *tipos.Sala
	for _, salaAchada := range s.Salas {
		for _, j := range salaAchada.Jogadores {
			if j.ID == clienteID {
				sala = salaAchada
				break
			}
		}
		if sala != nil {
			break
		}
	}
	s.mutexSalas.Unlock()

	if sala == nil {
		return
	}

	// Decodifica a mensagem
	var dados protocolo.DadosAtualizacaoJogo
	json.Unmarshal(msg.Dados, &dados)

	// Inicializa contagem se necessário
	if dados.ContagemCartas == nil {
		dados.ContagemCartas = make(map[string]int)
	}

	// Atualiza contagem de cartas com dados LOCAIS
	s.mutexClientes.RLock()
	for _, j := range sala.Jogadores {
		if cliente, ok := s.Clientes[j.ID]; ok {
			// Este jogador está local - atualiza contagem
			cliente.Mutex.Lock()
			dados.ContagemCartas[j.Nome] = len(cliente.Inventario)
			cliente.Mutex.Unlock()
		}
	}
	s.mutexClientes.RUnlock()

	// Re-codifica a mensagem
	msg.Dados = json.RawMessage(seguranca.MustJSON(dados))
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

	// Atualiza estado da sala
	sala.Estado = estado.Estado
	sala.TurnoDe = estado.TurnoDe

	// ATUALIZA TAMBÉM OS INVENTÁRIOS DOS JOGADORES REAIS
	// Importante: sincronizar as mudanças do estado para os jogadores reais do Shadow
	if len(estado.Jogadores) > 0 {
		log.Printf("[SYNC_SOMBRA] Recebido estado com %d jogadores para atualizar", len(estado.Jogadores))
		for _, jogadorEstado := range estado.Jogadores {
			log.Printf("[SYNC_SOMBRA] Procurando jogador %s para atualizar", jogadorEstado.ID)
			// Atualiza inventário do jogador real se existir localmente
			s.mutexClientes.RLock()
			jogadorReal := s.Clientes[jogadorEstado.ID]
			s.mutexClientes.RUnlock()

			if jogadorReal != nil {
				jogadorReal.Mutex.Lock()
				// Sincroniza inventário do jogador real com o estado recebido
				oldCount := len(jogadorReal.Inventario)
				jogadorReal.Inventario = jogadorEstado.Inventario
				jogadorReal.Mutex.Unlock()
				log.Printf("[SYNC_SOMBRA] Inventário real atualizado para jogador local %s (%d -> %d cartas)", jogadorReal.Nome, oldCount, len(jogadorEstado.Inventario))

				// Se o inventário mudou, notifica o cliente com o inventário atualizado
				if oldCount != len(jogadorEstado.Inventario) {
					log.Printf("[SYNC_SOMBRA] Inventário mudou! Notificando cliente %s com inventário atualizado", jogadorReal.Nome)
					// Notifica com inventário atualizado para o jogador ver as mudanças
					dados := protocolo.TrocarCartasResp{
						Sucesso:              true,
						Mensagem:             "Troca processada! Seu inventário foi atualizado.",
						InventarioAtualizado: jogadorEstado.Inventario,
					}
					s.publicarParaCliente(jogadorReal.ID, protocolo.Mensagem{
						Comando: "TROCA_CONCLUIDA",
						Dados:   seguranca.MustJSON(dados),
					})
				}
			} else {
				log.Printf("[SYNC_SOMBRA] Jogador %s não encontrado no mapa de clientes", jogadorEstado.ID)
			}
		}
	} else {
		log.Printf("[SYNC_SOMBRA] Nenhum jogador no estado para atualizar")
	}

	// Atualiza cópias na sala
	for i := range sala.Jogadores {
		for _, jogadorEstado := range estado.Jogadores {
			if sala.Jogadores[i].ID == jogadorEstado.ID {
				sala.Jogadores[i].Inventario = jogadorEstado.Inventario
				log.Printf("[SYNC_SOMBRA] Cópia na sala atualizada para jogador %s (%d cartas)", jogadorEstado.ID, len(jogadorEstado.Inventario))
				break
			}
		}
	}

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
				EventSeq:  0, // O Host definirá o eventSeq correto
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

		if servidorHost == s.MeuEndereco {
			// Eu sou o Host, eu faço o broadcast
			log.Printf("[HOST-CHAT] Recebido chat de %s. Fazendo broadcast.", cliente.Nome)
			s.retransmitirChat(sala, cliente, dadosCliente.Texto)
		} else if servidorSombra == s.MeuEndereco {
			// Eu sou o Shadow, encaminho para o Host
			log.Printf("[SHADOW-CHAT] Recebido chat de %s. Encaminhando para Host %s.", cliente.Nome, servidorHost)
			dadosEvento := map[string]interface{}{
				"texto": dadosCliente.Texto,
			}
			go s.encaminharEventoParaHost(sala, dadosCliente.ClienteID, "CHAT", dadosEvento)
		}

	case "TROCAR_CARTAS", "TROCAR_CARTAS_OFERTA":
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
	// Assumindo que você adicione um campo `cancelBuscaGlobal context.CancelFunc` à struct Cliente
	// cliente.cancelBuscaGlobal = cancel // (Descomente se adicionar o campo)

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

	httpClient := &http.Client{Timeout: 15 * time.Second}
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

func (s *Servidor) ProcessarEventoComoHost(sala *tipos.Sala, evento *tipos.GameEventRequest) *tipos.EstadoPartida {
	return s.processarEventoComoHost(sala, evento)
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
	eventSeq := int64(0)
	sala.Mutex.Unlock()

	log.Printf("[SHADOW] Encaminhando evento %s de %s para o Host %s (eventSeq será definido pelo Host)", eventType, clienteID, host)

	req := tipos.GameEventRequest{
		MatchID:   sala.ID,
		EventSeq:  eventSeq,
		EventType: eventType,
		PlayerID:  clienteID,
		Data:      data,
	}

	// Criar evento e assinar
	event := &tipos.GameEvent{
		EventSeq:  req.EventSeq,
		MatchID:   req.MatchID,
		EventType: req.EventType,
		PlayerID:  req.PlayerID,
	}
	seguranca.SignEvent(event)
	req.Signature = event.Signature

	jsonData, _ := json.Marshal(req)
	url := fmt.Sprintf("http://%s/game/event", host)

	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := s.enviarRequestComToken("POST", url, jsonData)
		if err != nil {
			log.Printf("[SHADOW] Erro ao processar evento %s pelo Host (tentativa %d/%d): %v", eventType, attempt, maxRetries, err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			log.Printf("[SHADOW] Evento %s processado pelo Host com sucesso (tentativa %d/%d)", eventType, attempt, maxRetries)
			return
		}

		log.Printf("[SHADOW] Host retornou status %d ao processar evento %s (tentativa %d/%d)", resp.StatusCode, eventType, attempt, maxRetries)
		if attempt < maxRetries {
			log.Printf("[RETRY] Aguardando %ds antes da próxima tentativa...", attempt)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
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

		client := &http.Client{Timeout: 15 * time.Second}
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
	// Notifica o cliente localmente via MQTT
	s.publicarParaCliente(clienteID, msg)

	// Se for uma partida cross-server, também notifica o servidor remoto
	sala.Mutex.Lock()
	sombraAddr := sala.ServidorSombra
	hostAddr := sala.ServidorHost
	sala.Mutex.Unlock()

	// Se este servidor é o Host e há uma Sombra, notifica a Sombra
	if hostAddr == s.MeuEndereco && sombraAddr != "" {
		go s.notificarJogadorRemoto(sombraAddr, clienteID, msg)
		// Forçar sincronização de estado após compra
		go s.forcarSincronizacaoEstado(sala.ID)
	}

	// CORREÇÃO DEADLOCK: Lógica diferenciada por tipo de partida
	sala.Mutex.Lock()
	sala.Prontos[cliente.Nome] = true

	// Captura informações para decisão
	isHost := sala.ServidorHost == s.MeuEndereco
	isShadow := sala.ServidorSombra == s.MeuEndereco
	sombraAddr = sala.ServidorSombra
	hostAddr = sala.ServidorHost
	sala.Mutex.Unlock()

	// CORREÇÃO CRÍTICA: Evita race condition/deadlock
	// Em partidas cross-server, apenas o Shadow notifica o Host
	if isShadow && hostAddr != "" {
		// Shadow: Envia PLAYER_READY para o Host após compra
		log.Printf("[SHADOW] Jogador %s pronto. Notificando Host %s.", cliente.Nome, hostAddr)
		go s.encaminharEventoParaHost(sala, cliente.ID, "PLAYER_READY", nil)
	} else if isHost && sombraAddr == "" {
		// Host de partida LOCAL: Verifica imediatamente
		log.Printf("[HOST-LOCAL] Jogador %s pronto. Verificando início (partida local).", cliente.Nome)
		go s.verificarEIniciarPartidaSeProntos(sala)
	} else if isHost && sombraAddr != "" {
		// Host de partida CROSS-SERVER: Aguarda PLAYER_READY do Shadow
		// MAS verifica se ambos já estão prontos (caso Felipe tenha enviado PLAYER_READY primeiro)
		log.Printf("[HOST-CROSS] Jogador %s pronto. Verificando se ambos prontos (Shadow: %s).", cliente.Nome, sombraAddr)
		go s.verificarEIniciarPartidaSeProntos(sala)
	}
}

func (s *Servidor) iniciarPartida(sala *tipos.Sala) {
	sala.Mutex.Lock()
	partidaIniciada := s.iniciarPartidaInterna(sala)
	sala.Mutex.Unlock()

	if partidaIniciada {
		s.notificarInicioPartida(sala)
	}
}

// iniciarPartidaInterna faz o trabalho de iniciar a partida, assumindo que o lock já está ativo
// Retorna true se a partida foi realmente iniciada, false se já estava jogando
func (s *Servidor) iniciarPartidaInterna(sala *tipos.Sala) bool {
	// CORREÇÃO: Verifica se a partida já foi iniciada para evitar iniciar duas vezes
	if sala.Estado == "JOGANDO" {
		log.Printf("[INICIAR_PARTIDA:%s] Partida %s já foi iniciada. Ignorando chamada duplicada.", s.ServerID, sala.ID)
		return false
	}

	log.Printf("[INICIAR_PARTIDA_DEBUG:%s] Estado atual: %s, mudando para JOGANDO", s.ServerID, sala.Estado)
	sala.Estado = "JOGANDO"

	// CORREÇÃO: Registrar o TurnoDe ANTES de copiar jogadores, para evitar race condition
	jogadorInicial := sala.Jogadores[rand.Intn(len(sala.Jogadores))]
	turnoDeID := jogadorInicial.ID

	sala.TurnoDe = turnoDeID

	return true
}

// notificarInicioPartida realiza as notificações depois que a partida foi iniciada
func (s *Servidor) notificarInicioPartida(sala *tipos.Sala) {
	// Copia dados sob lock
	sala.Mutex.Lock()
	jogadorInicialNome := ""
	sombraAddr := sala.ServidorSombra
	hostAddr := sala.ServidorHost
	turnoDeID := sala.TurnoDe
	jogadoresCopy := make([]*tipos.Cliente, len(sala.Jogadores))
	copy(jogadoresCopy, sala.Jogadores)
	sala.Mutex.Unlock()

	// Coleta contagem de cartas FORA do lock da sala para evitar contenção
	// CORREÇÃO CRÍTICA: Buscar jogadores do mapa global para obter inventários atualizados
	contagemCartas := make(map[string]int)
	for _, j := range jogadoresCopy {
		// Encontra o nome do jogador inicial para a mensagem
		if j.ID == turnoDeID {
			jogadorInicialNome = j.Nome
		}

		// Busca o jogador no mapa global para obter inventário atualizado
		s.mutexClientes.RLock()
		jogadorAtualizado := s.Clientes[j.ID]
		s.mutexClientes.RUnlock()

		if jogadorAtualizado != nil {
			// Jogador está neste servidor - usa dados atuais
			jogadorAtualizado.Mutex.Lock()
			contagemCartas[j.Nome] = len(jogadorAtualizado.Inventario)
			jogadorAtualizado.Mutex.Unlock()
		}
		// Se jogador não está neste servidor (remoto), não inclui na contagem
		// O Shadow irá preencher a contagem para seus jogadores locais
	}

	log.Printf("[JOGO_DEBUG] Partida %s iniciada. Jogador inicial: %s (%s)", sala.ID, jogadorInicialNome, turnoDeID)
	log.Printf("[JOGO_DEBUG] TurnoDe definido como: %s", turnoDeID)

	// Envia um estado inicial completo em vez de apenas uma mensagem de texto
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Partida iniciada! É a vez de %s.", jogadorInicialNome),
			NumeroRodada:    sala.NumeroRodada,
			ContagemCartas:  contagemCartas,
			TurnoDe:         turnoDeID,
		}),
	}

	// CORREÇÃO CRUCIAL: Em partidas cross-server, publica MQTT local E notifica jogadores remotos via HTTP
	if hostAddr == s.MeuEndereco && sombraAddr != "" {
		// Este servidor é o Host, partida é cross-server
		log.Printf("[INICIAR_PARTIDA_CROSS] Publicando no MQTT local e notificando Shadow via HTTP")
		// Publica no MQTT local (vai chegar apenas aos clientes conectados a este servidor)
		s.publicarEventoPartida(sala.ID, msg)
		// Notifica jogadores remotos (do Shadow) via HTTP
		go s.notificarJogadoresRemotosDaPartida(sala.ID, sombraAddr, jogadoresCopy, msg)
	} else {
		// Partida local - apenas publica no MQTT
		s.publicarEventoPartida(sala.ID, msg)
	}
}

// notificarJogadoresRemotosDaPartida envia evento de partida para jogadores em outro servidor
func (s *Servidor) notificarJogadoresRemotosDaPartida(salaID, servidorRemoto string, todosJogadores []*tipos.Cliente, msg protocolo.Mensagem) {
	log.Printf("[NOTIFICAR_REMOTOS] Notificando jogadores remotos na partida %s via servidor %s", salaID, servidorRemoto)

	// Envia para cada jogador que não está local
	for _, jogador := range todosJogadores {
		if s.getClienteLocal(jogador.ID) == nil {
			// Este jogador está no servidor remoto
			log.Printf("[NOTIFICAR_REMOTOS] Enviando evento para jogador remoto %s (%s) via %s", jogador.Nome, jogador.ID, servidorRemoto)
			s.notificarJogadorRemoto(servidorRemoto, jogador.ID, msg)
		}
	}
}

// verificarEIniciarPartidaSeProntos verifica se todos compraram e inicia a partida,
// notificando a Sombra se necessário.
func (s *Servidor) verificarEIniciarPartidaSeProntos(sala *tipos.Sala) {
	sala.Mutex.Lock()
	prontos := len(sala.Prontos)
	total := len(sala.Jogadores)
	estadoAtual := sala.Estado
	salaID := sala.ID

	// DEBUG: Lista quais jogadores estão prontos
	var jogadoresProntos []string
	for nome := range sala.Prontos {
		jogadoresProntos = append(jogadoresProntos, nome)
	}
	var todosJogadores []string
	for _, j := range sala.Jogadores {
		todosJogadores = append(todosJogadores, j.Nome)
	}

	log.Printf("[VERIFICAR_INICIO:%s] Sala %s - Prontos: %d/%d (%v). Estado: %s. Todos jogadores: %v",
		s.ServerID, salaID, prontos, total, jogadoresProntos, estadoAtual, todosJogadores)
	sala.Mutex.Unlock()

	// CORREÇÃO CRÍTICA: Verificar e iniciar com o LOCK ATIVO para evitar race condition
	// Só inicia se estiver aguardando e todos estiverem prontos
	sala.Mutex.Lock()
	partidaIniciada := false
	if estadoAtual == "AGUARDANDO_COMPRA" && prontos == total && total == 2 {
		log.Printf("[INICIAR_PARTIDA:%s] Todos os jogadores da sala %s estão prontos. Iniciando.", s.ServerID, salaID)
		// iniciarPartidaInterna espera que o lock já esteja ativo
		partidaIniciada = s.iniciarPartidaInterna(sala)
	} else {
		log.Printf("[VERIFICAR_INICIO_DEBUG:%s] Não iniciando partida. Estado: %s, Prontos: %d, Total: %d",
			s.ServerID, estadoAtual, prontos, total)
	}
	sala.Mutex.Unlock()

	// Se a partida foi iniciada, faz as notificações
	if partidaIniciada {
		s.notificarInicioPartida(sala)
	}
}

// encaminharJogadaParaHost encaminha uma jogada da Sombra para o Host via API REST
func (s *Servidor) encaminharJogadaParaHost(sala *tipos.Sala, clienteID, cartaID string) {
	sala.Mutex.Lock()
	host := sala.ServidorHost
	// CORREÇÃO: Não incrementar o eventSeq aqui. O Host é a autoridade sobre o eventSeq.
	eventSeq := int64(0) // Será definido pelo Host
	sala.Mutex.Unlock()

	log.Printf("[SHADOW] Encaminhando jogada de %s para o Host %s (eventSeq será definido pelo Host)", clienteID, host)

	// CORREÇÃO: Buscar e validar a carta no inventário local do Shadow
	// IMPORTANTE: NÃO remove a carta ainda - apenas obtém os dados
	s.mutexClientes.RLock()
	cliente := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()

	if cliente == nil {
		log.Printf("[SHADOW] Cliente %s não encontrado localmente", clienteID)
		return
	}

	cliente.Mutex.Lock()
	var carta Carta
	cartaIndex := -1
	for i, c := range cliente.Inventario {
		if c.ID == cartaID {
			carta = c
			cartaIndex = i
			break
		}
	}

	if cartaIndex == -1 {
		cliente.Mutex.Unlock()
		log.Printf("[SHADOW] Carta %s não encontrada no inventário de %s", cartaID, clienteID)
		return
	}
	// Remove a carta do inventário local ANTES de enviar ao Host
	cliente.Inventario = append(cliente.Inventario[:cartaIndex], cliente.Inventario[cartaIndex+1:]...)
	cliente.Mutex.Unlock()

	// Usa o novo endpoint /game/event
	req := tipos.GameEventRequest{
		MatchID:   sala.ID,
		EventSeq:  eventSeq,
		EventType: "CARD_PLAYED",
		PlayerID:  clienteID,
		Data: map[string]interface{}{
			"carta_id":       cartaID,
			"carta_nome":     carta.Nome,
			"carta_naipe":    carta.Naipe,
			"carta_valor":    carta.Valor,
			"carta_raridade": carta.Raridade,
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
		Timeout: 15 * time.Second,
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

	// CORREÇÃO: Remove a carta do inventário APENAS após confirmação do Host
	cliente.Mutex.Lock()
	// Remove a carta que foi jogada
	cartaIndex = -1
	for i, c := range cliente.Inventario {
		if c.ID == cartaID {
			cartaIndex = i
			break
		}
	}
	if cartaIndex != -1 {
		cliente.Inventario = append(cliente.Inventario[:cartaIndex], cliente.Inventario[cartaIndex+1:]...)
		log.Printf("[SHADOW] Carta %s removida do inventário de %s", cartaID, clienteID)
	}
	cliente.Mutex.Unlock()
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

	// Variável para armazenar o vencedor da jogada (se houver)
	var vencedorJogada string

	log.Printf("[%s][EVENTO_HOST:%s] TENTANDO LOCK DA SALA...", timestamp, sala.ID)

	// Lock com timeout para evitar travamento
	lockAcquired := make(chan bool, 1)
	go func() {
		sala.Mutex.Lock()
		lockAcquired <- true
	}()

	select {
	case <-lockAcquired:
		log.Printf("[%s][EVENTO_HOST:%s] LOCK DA SALA OBTIDO", timestamp, sala.ID)
	case <-time.After(2 * time.Second):
		log.Printf("[%s][EVENTO_HOST:%s] TIMEOUT NO LOCK DA SALA - IGNORANDO EVENTO", timestamp, sala.ID)
		// Liberar o lock da goroutine
		go func() { <-lockAcquired; sala.Mutex.Unlock() }()
		return nil
	}

	defer func() {
		log.Printf("[%s][EVENTO_HOST:%s] LIBERANDO LOCK DA SALA...", timestamp, sala.ID)
		sala.Mutex.Unlock()
		log.Printf("[%s][EVENTO_HOST:%s] LOCK DA SALA LIBERADO", timestamp, sala.ID)

		// CORREÇÃO CRÍTICA: Verificação de início da partida APÓS liberar o lock
		// Isso evita deadlock em partidas cross-server
		if evento.EventType == "PLAYER_READY" {
			log.Printf("[HOST] PLAYER_READY processado. Verificando se pode iniciar partida (lock já liberado)...")
			go s.verificarEIniciarPartidaSeProntos(sala)
		}

		// CORREÇÃO: Notificar resultado da jogada APÓS liberar o lock
		// Isso evita que a goroutine inicie com o lock ativo e causa timeout
		// Só notifica se a partida ainda não foi finalizada (se chegou aqui, não finalizou)
		if vencedorJogada != "" && (evento.EventType == "CARD_PLAYED" || evento.EventType == "JOGAR_CARTA") && sala.Estado != "FINALIZADO" {
			log.Printf("[HOST] Notificando resultado da jogada após lock liberado. Vencedor: %s", vencedorJogada)
			go s.notificarResultadoJogada(sala, vencedorJogada)
		}
	}()

	// Validação de Evento (do ARQUITETURA_CROSS_SERVER.md)
	// O Host é a autoridade sobre o eventSeq.
	// CORREÇÃO: Aceitar eventos com eventSeq = 0 (enviados pelo Shadow sem eventSeq definido)
	if evento.EventSeq > 0 && evento.EventSeq <= sala.EventSeq {
		log.Printf("[EVENTO_HOST:%s] Evento desatualizado ou duplicado recebido. Seq Recebido: %d, Seq Atual: %d", sala.ID, evento.EventSeq, sala.EventSeq)
		return nil // Ignora o evento (idealmente, o handler da API retornaria 409 Conflict)
	}

	// Validação de turno (APENAS para jogadas de carta)
	if evento.EventType == "CARD_PLAYED" || evento.EventType == "JOGAR_CARTA" {
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

	case "PLAYER_READY":
		sala.Prontos[nomeJogador] = true
		log.Printf("[HOST] Jogador %s (%s) está PRONTO (evento recebido). Prontos: %d/%d", nomeJogador, evento.PlayerID, len(sala.Prontos), len(sala.Jogadores))
		// CORREÇÃO: A verificação NÃO acontece aqui para evitar deadlock.
		// Será feita após liberar o lock.

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
		go s.retransmitirChat(sala, jogador, texto)

	case "JOGAR_CARTA", "CARD_PLAYED": // Aceita ambos os tipos por compatibilidade
		// CORREÇÃO: Primeiro, faça a asserção de tipo do 'Data' para um map
		dadosDoEvento, ok := evento.Data.(map[string]interface{})
		if !ok {
			log.Printf("[EVENTO_HOST:%s] Evento %s com 'Data' mal formatado (esperado map[string]interface{}, obteve %T)", sala.ID, evento.EventType, evento.Data)
			return nil
		}

		// Agora sim, acesse o map 'dadosDoEvento'
		cartaID, ok := dadosDoEvento["carta_id"].(string)
		if !ok {
			log.Printf("[EVENTO_HOST:%s] Evento %s sem 'carta_id' no 'Data'", sala.ID, evento.EventType)
			return nil
		}

		if _, jaJogou := sala.CartasNaMesa[nomeJogador]; jaJogou {
			log.Printf("[HOST] Jogador %s já jogou nesta rodada", nomeJogador)
			return nil
		}

		// CORREÇÃO: Verificar se é jogador local ou remoto
		// Se o jogador está no mapa local de clientes, é local. Caso contrário, é remoto (Shadow).
		s.mutexClientes.RLock()
		_, jogadorLocal := s.Clientes[evento.PlayerID]
		s.mutexClientes.RUnlock()

		var carta Carta

		// CORREÇÃO: Apenas tenta remover do inventário local se o jogador for local
		if jogadorLocal {
			jogador.Mutex.Lock()
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
				log.Printf("[HOST] Carta %s não encontrada no inventário de %s (jogador local)", cartaID, nomeJogador)
				return nil
			}
			jogador.Inventario = append(jogador.Inventario[:cartaIndex], jogador.Inventario[cartaIndex+1:]...)
			jogador.Mutex.Unlock()
			log.Printf("[HOST] Jogador local %s jogou carta %s (Poder: %d) - eventSeq: %d", nomeJogador, carta.Nome, carta.Valor, currentEventSeq)
		} else {
			// Jogador remoto - apenas obtém os dados da carta do evento
			// O Shadow já validou e removeu a carta do inventário do jogador remoto
			cartaNome, _ := dadosDoEvento["carta_nome"].(string)
			cartaNaipe, _ := dadosDoEvento["carta_naipe"].(string)
			cartaValor, _ := dadosDoEvento["carta_valor"].(float64)
			cartaRaridade, _ := dadosDoEvento["carta_raridade"].(string)

			carta = Carta{
				ID:       cartaID,
				Nome:     cartaNome,
				Naipe:    cartaNaipe,
				Valor:    int(cartaValor),
				Raridade: cartaRaridade,
			}
			log.Printf("[HOST] Jogador remoto %s jogou carta %s (Poder: %d) - eventSeq: %d", nomeJogador, carta.Nome, carta.Valor, currentEventSeq)
		}

		sala.CartasNaMesa[nomeJogador] = carta

		if len(sala.CartasNaMesa) == len(sala.Jogadores) {
			vencedorJogada = s.resolverJogada(sala)
		} else {
			for _, j := range sala.Jogadores {
				if j.ID != evento.PlayerID {
					log.Printf("[TURNO:%s] Jogador %s jogou. Próximo a jogar: %s (%s)", sala.ID, evento.PlayerID, j.Nome, j.ID)
					s.mudarTurnoAtomicamente(sala, j.ID)
					break
				}
			}
			// CORREÇÃO: Chama em goroutine para não bloquear o lock da sala
			// A função notificarAguardandoOponente já não requer lock ativo
			go s.notificarAguardandoOponente(sala) // Notifica que a jogada foi feita, mas espera o outro
		}

	} // Fim do switch

	// --- REPLICAÇÃO ---
	estado := &tipos.EstadoPartida{
		SalaID:         sala.ID,
		Estado:         sala.Estado,
		CartasNaMesa:   sala.CartasNaMesa,
		PontosRodada:   sala.PontosRodada,
		PontosPartida:  sala.PontosPartida,
		NumeroRodada:   sala.NumeroRodada,
		Prontos:        sala.Prontos,
		EventSeq:       currentEventSeq,
		EventLog:       sala.EventLog,
		TurnoDe:        sala.TurnoDe,
		VencedorJogada: vencedorJogada, // Adiciona o vencedor ao estado retornado
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

	httpClient := &http.Client{Timeout: 15 * time.Second}

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
func (s *Servidor) resolverJogada(sala *tipos.Sala) string {
	// IMPORTANTE: Esta função assume que o `sala.Mutex` JÁ ESTÁ BLOQUEADO pela função que a chamou (ex: processarJogadaComoHost)
	log.Printf("[JOGADA_RESOLVER:%s] Resolvendo jogada...", sala.ID)

	// Garante que a operação seja atômica na sala
	// sala.Mutex.Lock() <--- REMOVIDO PARA EVITAR DEADLOCK
	// defer sala.Mutex.Unlock() <--- REMOVIDO

	if len(sala.CartasNaMesa) != 2 {
		log.Printf("[JOGO_ERRO:%s] Tentativa de resolver jogada com %d cartas na mesa.", sala.ID, len(sala.CartasNaMesa))
		return ""
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

	// CORREÇÃO: NOTA - notificarResultadoJogada será chamada APÓS liberar o lock da sala
	// pelo processarEventoComoHost, para evitar que a goroutine inicie com o lock ativo

	// CORREÇÃO: Verifica se acabaram as cartas DEPOIS de limpar a mesa
	// Busca jogadores do mapa global para contagem atualizada
	s.mutexClientes.RLock()
	j1Global := s.Clientes[j1.ID]
	j2Global := s.Clientes[j2.ID]
	s.mutexClientes.RUnlock()

	var j1Cartas, j2Cartas int
	if j1Global != nil {
		j1Global.Mutex.Lock()
		j1Cartas = len(j1Global.Inventario)
		j1Global.Mutex.Unlock()
	} else {
		j1.Mutex.Lock()
		j1Cartas = len(j1.Inventario)
		j1.Mutex.Unlock()
	}

	if j2Global != nil {
		j2Global.Mutex.Lock()
		j2Cartas = len(j2Global.Inventario)
		j2Global.Mutex.Unlock()
	} else {
		j2.Mutex.Lock()
		j2Cartas = len(j2.Inventario)
		j2.Mutex.Unlock()
	}

	log.Printf("[VERIFICACAO_CARTAS:%s] Após jogada: %s tem %d cartas, %s tem %d cartas", sala.ID, j1.Nome, j1Cartas, j2.Nome, j2Cartas)

	// CORREÇÃO: Só finalizar se AMBOS tiverem 0 cartas (ou se for realmente um caso de fim de jogo)
	// Em partidas cross-server, o inventário do jogador remoto pode estar vazio no Host
	// então não devemos finalizar baseado apenas em um jogador com 0 cartas
	if j1Cartas == 0 && j2Cartas == 0 {
		// Ambos sem cartas - fim definitivo
		log.Printf("[FINALIZACAO:%s] Ambos os jogadores ficaram sem cartas. Finalizando partida.", sala.ID)
		s.finalizarPartida(sala)
	} else if j1Cartas == 0 || j2Cartas == 0 {
		// Apenas um sem cartas - isso não deveria acontecer normalmente, mas vamos continuar
		log.Printf("[AVISO:%s] Um jogador tem 0 cartas (cross-server?). Continuando partida por segurança.", sala.ID)
		sala.NumeroRodada++
	} else {
		// Ambos com cartas - continuar
		log.Printf("[CONTINUA_JOGO:%s] Ambos os jogadores ainda têm cartas (%d e %d). Continuando a partida.", sala.ID, j1Cartas, j2Cartas)
		sala.NumeroRodada++
	}

	return vencedorJogada
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
	timestamp := time.Now().Format("15:04:05.000")
	log.Printf("[%s][NOTIFICACAO:%s] === INÍCIO NOTIFICAÇÃO AGUARDANDO OPONENTE ===", timestamp, sala.ID)
	log.Printf("[%s][NOTIFICACAO:%s] Publicando atualização de aguardo de jogada.", timestamp, sala.ID)

	// CORREÇÃO: Coleta dados SEM lock da sala para evitar contenção
	var proximoJogadorNome string
	var turnoDe string
	var numeroRodada int
	var cartasNaMesa map[string]Carta

	// Copia dados sob lock da sala (rápido)
	sala.Mutex.Lock()
	turnoDe = sala.TurnoDe
	numeroRodada = sala.NumeroRodada
	cartasNaMesa = make(map[string]Carta)
	for k, v := range sala.CartasNaMesa {
		cartasNaMesa[k] = v
	}
	jogadoresCopy := make([]*tipos.Cliente, len(sala.Jogadores))
	copy(jogadoresCopy, sala.Jogadores)
	sombraAddr := sala.ServidorSombra
	hostAddr := sala.ServidorHost
	sala.Mutex.Unlock()

	// Agora encontra o nome do jogador e conta cartas FORA do lock da sala
	for _, j := range jogadoresCopy {
		if j.ID == turnoDe {
			proximoJogadorNome = j.Nome
			break
		}
	}

	// Coleta contagem de cartas do mapa global (inventários atualizados)
	contagemCartas := make(map[string]int)
	for _, j := range jogadoresCopy {
		// Busca o jogador no mapa global para obter inventário atualizado
		s.mutexClientes.RLock()
		jogadorAtualizado := s.Clientes[j.ID]
		s.mutexClientes.RUnlock()

		if jogadorAtualizado != nil {
			jogadorAtualizado.Mutex.Lock()
			contagemCartas[j.Nome] = len(jogadorAtualizado.Inventario)
			jogadorAtualizado.Mutex.Unlock()
		}
	}

	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Aguardando jogada de %s...", proximoJogadorNome),
			NumeroRodada:    numeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    cartasNaMesa,
			TurnoDe:         turnoDe,
		}),
	}

	log.Printf("[%s][NOTIFICACAO:%s] Enviando mensagem para tópico partidas/%s/eventos", timestamp, sala.ID, sala.ID)
	log.Printf("[%s][NOTIFICACAO:%s] Conteúdo da mensagem: %+v", timestamp, sala.ID, msg)
	log.Printf("[%s][NOTIFICACAO_DEBUG] hostAddr='%s', MeuEndereco='%s', sombraAddr='%s'", timestamp, hostAddr, s.MeuEndereco, sombraAddr)

	// CORREÇÃO CRUCIAL: Em partidas cross-server, publica MQTT local E notifica jogadores remotos via HTTP
	if hostAddr == s.MeuEndereco && sombraAddr != "" {
		// Este servidor é o Host, partida é cross-server
		log.Printf("[%s][NOTIFICACAO_CROSS] Publicando no MQTT local e notificando Shadow via HTTP", timestamp)
		// Publica no MQTT local (vai chegar apenas aos clientes conectados a este servidor)
		s.publicarEventoPartida(sala.ID, msg)
		// Notifica jogadores remotos (do Shadow) via HTTP
		go s.notificarJogadoresRemotosDaPartida(sala.ID, sombraAddr, jogadoresCopy, msg)
	} else {
		// Partida local - apenas publica no MQTT
		s.publicarEventoPartida(sala.ID, msg)
	}

	log.Printf("[%s][NOTIFICACAO:%s] === FIM NOTIFICAÇÃO AGUARDANDO OPONENTE ===", timestamp, sala.ID)
}

// notificarResultadoJogada notifica o resultado de uma jogada
func (s *Servidor) notificarResultadoJogada(sala *tipos.Sala, vencedorJogada string) {
	log.Printf("[NOTIFICACAO:%s] Publicando resultado da jogada. Vencedor: %s", sala.ID, vencedorJogada)

	// CORREÇÃO: Coleta dados SEM lock da sala para evitar contenção
	var turnoDe string
	var numeroRodada int
	var cartasNaMesa map[string]Carta
	var salaID string

	// Copia dados sob lock da sala (rápido)
	sala.Mutex.Lock()
	turnoDe = sala.TurnoDe
	numeroRodada = sala.NumeroRodada
	salaID = sala.ID
	cartasNaMesa = make(map[string]Carta)
	for k, v := range sala.CartasNaMesa {
		cartasNaMesa[k] = v
	}
	jogadoresCopy := make([]*tipos.Cliente, len(sala.Jogadores))
	copy(jogadoresCopy, sala.Jogadores)
	sombraAddr := sala.ServidorSombra
	hostAddr := sala.ServidorHost
	sala.Mutex.Unlock()

	// Encontra o nome do próximo jogador e cria contagem de cartas FORA do lock da sala
	var proximoJogadorNome string
	for _, j := range jogadoresCopy {
		if j.ID == turnoDe {
			proximoJogadorNome = j.Nome
			break
		}
	}

	// Coleta contagem de cartas do mapa global (inventários atualizados)
	contagemCartas := make(map[string]int)
	for _, j := range jogadoresCopy {
		// Busca o jogador no mapa global para obter inventário atualizado
		s.mutexClientes.RLock()
		jogadorAtualizado := s.Clientes[j.ID]
		s.mutexClientes.RUnlock()

		if jogadorAtualizado != nil {
			jogadorAtualizado.Mutex.Lock()
			contagemCartas[j.Nome] = len(jogadorAtualizado.Inventario)
			jogadorAtualizado.Mutex.Unlock()
		}
	}

	// Cria a mensagem base
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados: seguranca.MustJSON(protocolo.DadosAtualizacaoJogo{
			MensagemDoTurno: fmt.Sprintf("Vencedor da jogada: %s. Próximo a jogar: %s", vencedorJogada, proximoJogadorNome),
			NumeroRodada:    numeroRodada,
			ContagemCartas:  contagemCartas,
			UltimaJogada:    cartasNaMesa,
			VencedorJogada:  vencedorJogada,
			SalaID:          salaID,
			TurnoDe:         turnoDe,
		}),
	}

	// CORREÇÃO CRUCIAL: Em partidas cross-server, publica MQTT local E notifica jogadores remotos via HTTP
	if hostAddr == s.MeuEndereco && sombraAddr != "" {
		// Este servidor é o Host, partida é cross-server
		log.Printf("[NOTIFICACAO_CROSS] Publicando no MQTT local e notificando Shadow via HTTP")
		// Publica no MQTT local (vai chegar apenas aos clientes conectados a este servidor)
		s.publicarEventoPartida(salaID, msg)
		// Notifica jogadores remotos (do Shadow) via HTTP
		go s.notificarJogadoresRemotosDaPartida(salaID, sombraAddr, jogadoresCopy, msg)
	} else {
		// Partida local - apenas publica no MQTT
		s.publicarEventoPartida(salaID, msg)
	}
}

// finalizarPartida finaliza uma partida e determina o vencedor
func (s *Servidor) finalizarPartida(sala *tipos.Sala) {
	// CORREÇÃO: Esta função assume que o lock da sala JÁ ESTÁ ATIVO
	// Quando chamada de dentro de resolverJogada (que está dentro de processarEventoComoHost com lock ativo)

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

	// CORREÇÃO: Não adquire lock - assume que já está ativo
	jogadores := make([]*tipos.Cliente, len(sala.Jogadores))
	copy(jogadores, sala.Jogadores)
	sombraAddr := sala.ServidorSombra

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

	// Tentar sincronização com retry
	maxRetries := 2
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := s.enviarRequestComToken("POST", url, jsonData)
		if err != nil {
			log.Printf("[SYNC_SOMBRA] Tentativa %d/%d falhou com Sombra %s: %v", attempt, maxRetries, sombra, err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return // Desiste após todas as tentativas
		}
		defer resp.Body.Close()

		// Verifica o status code
		if resp.StatusCode != http.StatusOK {
			log.Printf("[SYNC_SOMBRA] Tentativa %d/%d retornou status %d com Sombra %s", attempt, maxRetries, resp.StatusCode, sombra)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return // Desiste
		}

		log.Printf("[SYNC_SOMBRA] Sincronização com Sombra %s bem-sucedida (tentativa %d/%d)", sombra, attempt, maxRetries)
		return // Sucesso
	}
}

func (s *Servidor) enviarAtualizacaoParaSombra(sombraAddr string, msg protocolo.Mensagem) {
	log.Printf("[SYNC] Enviando atualização de jogo para a sombra %s", sombraAddr)
	jsonData, _ := json.Marshal(msg)
	url := fmt.Sprintf("http://%s/partida/atualizar_estado", sombraAddr)

	// Tentar envio com retry
	maxRetries := 2
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := s.enviarRequestComToken("POST", url, jsonData)
		if err != nil {
			log.Printf("[SYNC_SOMBRA] Tentativa %d/%d falhou ao enviar atualização para %s: %v", attempt, maxRetries, sombraAddr, err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			log.Printf("[SYNC_SOMBRA] Atualização enviada com sucesso para %s (tentativa %d/%d)", sombraAddr, attempt, maxRetries)
			return
		}

		log.Printf("[SYNC_SOMBRA] Tentativa %d/%d retornou status %d ao enviar atualização para %s", attempt, maxRetries, resp.StatusCode, sombraAddr)
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
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

func (s *Servidor) ProcessarTrocaDireta(sala *tipos.Sala, req *protocolo.TrocarCartasReq) {
	s.processarTrocaCartas(sala, req)
}

// AplicarTrocaLocal remove a carta desejada do cliente local e adiciona a carta oferecida
func (s *Servidor) BuscarCartaEmCliente(clienteID, cartaID string) tipos.Carta {
	s.mutexClientes.RLock()
	cliente := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()

	if cliente == nil {
		return tipos.Carta{}
	}

	cliente.Mutex.Lock()
	var cartaEncontrada tipos.Carta
	for _, c := range cliente.Inventario {
		if c.ID == cartaID {
			cartaEncontrada = c
			break
		}
	}
	cliente.Mutex.Unlock()

	return cartaEncontrada
}

func (s *Servidor) AplicarTrocaLocal(clienteID string, idCartaDesejada string, cartaOferecida tipos.Carta) (bool, tipos.Carta, []tipos.Carta) {
	log.Printf("[APLICAR_TROCA_LOCAL] Cliente: %s, removendo carta ID: %s, adicionando: %s", clienteID, idCartaDesejada, cartaOferecida.Nome)

	s.mutexClientes.RLock()
	cliente := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()
	if cliente == nil {
		log.Printf("[APLICAR_TROCA_LOCAL] Cliente %s não encontrado", clienteID)
		return false, tipos.Carta{}, nil
	}
	cliente.Mutex.Lock()
	defer cliente.Mutex.Unlock()

	log.Printf("[APLICAR_TROCA_LOCAL] Inventário do cliente tem %d cartas", len(cliente.Inventario))

	idx := -1
	var cartaRemovida tipos.Carta
	for i, c := range cliente.Inventario {
		if c.ID == idCartaDesejada {
			idx = i
			cartaRemovida = c
			log.Printf("[APLICAR_TROCA_LOCAL] Carta encontrada no índice %d: %s", i, c.Nome)
			break
		}
	}
	if idx == -1 {
		log.Printf("[APLICAR_TROCA_LOCAL] Carta %s não encontrada no inventário", idCartaDesejada)
		log.Printf("[APLICAR_TROCA_LOCAL] Inventário atual: %v", cliente.Inventario)
		return false, tipos.Carta{}, nil
	}
	// Remove a carta do inventário
	inv := make([]tipos.Carta, 0, len(cliente.Inventario))
	inv = append(inv, cliente.Inventario[:idx]...)
	inv = append(inv, cliente.Inventario[idx+1:]...)
	// Adiciona a nova carta
	inv = append(inv, cartaOferecida)
	cliente.Inventario = inv
	snapshot := make([]tipos.Carta, len(inv))
	copy(snapshot, inv)
	log.Printf("[APLICAR_TROCA_LOCAL] Inventário atualizado para %d cartas. Cartas atuais: %v", len(inv), inv)
	return true, cartaRemovida, snapshot
}

func (s *Servidor) processarTrocaCartas(sala *tipos.Sala, req *protocolo.TrocarCartasReq) {
	log.Printf("[TROCA] === INÍCIO PROCESSAMENTO TROCA FORÇADA ===")
	log.Printf("[TROCA] Sala: %s", sala.ID)
	log.Printf("[TROCA] Ofertante: %s (%s) -> carta: %s", req.NomeJogadorOferta, req.IDJogadorOferta, req.IDCartaOferecida)
	log.Printf("[TROCA] Desejado: %s (%s) -> carta: %s", req.NomeJogadorDesejado, req.IDJogadorDesejado, req.IDCartaDesejada)

	// NECESSÁRIO: Apenas o Host coordena a troca
	if sala.ServidorHost != s.MeuEndereco {
		log.Printf("[TROCA] Shadow (servidor %s) encaminhando troca para o Host %s", s.MeuEndereco, sala.ServidorHost)
		s.encaminharTrocaParaHost(sala.ServidorHost, sala.ID, req)
		return
	}

	// Processa a troca no Host
	log.Printf("[TROCA] Processando troca no Host")

	// Busca jogadores
	s.mutexClientes.RLock()
	jogadorOferta := s.Clientes[req.IDJogadorOferta]
	jogadorDesejado := s.Clientes[req.IDJogadorDesejado]
	s.mutexClientes.RUnlock()

	// Busca na sala se não encontrou no mapa global
	if jogadorOferta == nil {
		sala.Mutex.Lock()
		for _, j := range sala.Jogadores {
			if j.ID == req.IDJogadorOferta {
				jogadorOferta = j
				log.Printf("[TROCA] Jogador ofertante %s encontrado na cópia da sala", j.Nome)
				break
			}
		}
		sala.Mutex.Unlock()
	}

	if jogadorDesejado == nil {
		sala.Mutex.Lock()
		for _, j := range sala.Jogadores {
			if j.ID == req.IDJogadorDesejado {
				jogadorDesejado = j
				log.Printf("[TROCA] Jogador desejado %s encontrado na cópia da sala", j.Nome)
				break
			}
		}
		sala.Mutex.Unlock()
	}

	if jogadorOferta == nil || jogadorDesejado == nil {
		s.notificarErro(req.IDJogadorOferta, "Um dos jogadores não foi encontrado.")
		return
	}

	// Busca carta do ofertante
	var cartaOferta tipos.Carta
	idxOferta := -1

	// Tenta no mapa real primeiro
	s.mutexClientes.RLock()
	jogadorReal := s.Clientes[req.IDJogadorOferta]
	s.mutexClientes.RUnlock()

	if jogadorReal != nil {
		// Cliente local
		jogadorReal.Mutex.Lock()
		for i, c := range jogadorReal.Inventario {
			if c.ID == req.IDCartaOferecida {
				cartaOferta = c
				idxOferta = i
				break
			}
		}
		jogadorReal.Mutex.Unlock()
		log.Printf("[TROCA] Carta ofertante %s encontrada no inventário real (idx: %d)", cartaOferta.Nome, idxOferta)
	} else {
		// Cliente remoto - busca no servidor dele via HTTP
		log.Printf("[TROCA] Jogador ofertante %s está remoto. Buscando carta via HTTP...", req.NomeJogadorOferta)

		// Determina servidor do jogador ofertante
		var servidorJogadorOferta string
		if sala.ServidorHost == s.MeuEndereco {
			// Sou Host, jogador remoto está na Sombra
			servidorJogadorOferta = sala.ServidorSombra
		} else {
			// Sou Shadow, jogador remoto está no Host
			servidorJogadorOferta = sala.ServidorHost
		}

		log.Printf("[TROCA] Buscando no servidor %s a carta %s do jogador %s", servidorJogadorOferta, req.IDCartaOferecida, req.IDJogadorOferta)

		// Busca a carta no servidor remoto
		payload := map[string]interface{}{
			"cliente_id": req.IDJogadorOferta,
			"carta_id":   req.IDCartaOferecida,
		}
		body, _ := json.Marshal(payload)

		url := fmt.Sprintf("http://%s/partida/buscar_carta", servidorJogadorOferta)
		resp, err := s.enviarRequestComToken("POST", url, body)
		if err != nil {
			log.Printf("[TROCA] Erro ao buscar carta do ofertante no remoto: %v", err)
		} else if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var result struct {
					Encontrada bool        `json:"encontrada"`
					Carta      tipos.Carta `json:"carta"`
				}
				if json.NewDecoder(resp.Body).Decode(&result) == nil && result.Encontrada {
					cartaOferta = result.Carta
					idxOferta = 99 // Placeholder para indicar que foi encontrada remotamente
					log.Printf("[TROCA] Carta ofertante %s encontrada no servidor remoto", cartaOferta.Nome)
				}
			}
		}

		// Se ainda não encontrou, tenta na cópia da sala como fallback
		if cartaOferta.ID == "" {
			sala.Mutex.Lock()
			for _, j := range sala.Jogadores {
				if j.ID == req.IDJogadorOferta {
					for i, c := range j.Inventario {
						if c.ID == req.IDCartaOferecida {
							cartaOferta = c
							idxOferta = i
							log.Printf("[TROCA] Carta ofertante %s encontrada na cópia da sala (fallback)", cartaOferta.Nome)
							break
						}
					}
					break
				}
			}
			sala.Mutex.Unlock()
		}
	}

	if cartaOferta.ID == "" {
		s.notificarErro(req.IDJogadorOferta, "Você não possui esta carta.")
		return
	}

	// Busca carta desejada
	var cartaDesejada tipos.Carta
	s.mutexClientes.RLock()
	jogadorDesejadoLocal := s.Clientes[req.IDJogadorDesejado]
	s.mutexClientes.RUnlock()

	if jogadorDesejadoLocal != nil {
		// Jogador local - busca no inventário real
		jogadorDesejadoLocal.Mutex.Lock()
		for _, c := range jogadorDesejadoLocal.Inventario {
			if c.ID == req.IDCartaDesejada {
				cartaDesejada = c
				break
			}
		}
		jogadorDesejadoLocal.Mutex.Unlock()
		log.Printf("[TROCA] Carta desejada %s encontrada no inventário real", cartaDesejada.Nome)
	} else {
		// Jogador está em outro servidor - busca no servidor remoto via HTTP
		log.Printf("[TROCA] Jogador desejado %s está remoto. Buscando carta via HTTP no servidor dele...", req.NomeJogadorDesejado)

		// Determina servidor do jogador desejado
		var servidorJogadorDesejado string
		if sala.ServidorHost == s.MeuEndereco {
			// Sou Host, jogador remoto está na Sombra
			servidorJogadorDesejado = sala.ServidorSombra
		} else {
			// Sou Shadow, jogador remoto está no Host
			servidorJogadorDesejado = sala.ServidorHost
		}

		log.Printf("[TROCA] Buscando no servidor %s a carta %s do jogador %s", servidorJogadorDesejado, req.IDCartaDesejada, req.IDJogadorDesejado)

		// Busca a carta no servidor remoto
		payload := map[string]interface{}{
			"cliente_id": req.IDJogadorDesejado,
			"carta_id":   req.IDCartaDesejada,
		}
		body, _ := json.Marshal(payload)

		url := fmt.Sprintf("http://%s/partida/buscar_carta", servidorJogadorDesejado)
		resp, err := s.enviarRequestComToken("POST", url, body)
		if err != nil {
			log.Printf("[TROCA] Erro ao buscar carta no remoto: %v", err)
		} else if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var result struct {
					Encontrada bool        `json:"encontrada"`
					Carta      tipos.Carta `json:"carta"`
				}
				if json.NewDecoder(resp.Body).Decode(&result) == nil && result.Encontrada {
					cartaDesejada = result.Carta
					log.Printf("[TROCA] Carta desejada %s encontrada no servidor remoto", cartaDesejada.Nome)
				}
			}
		}

		// Se ainda não encontrou, tenta na cópia da sala como fallback
		if cartaDesejada.ID == "" {
			sala.Mutex.Lock()
			for _, j := range sala.Jogadores {
				if j.ID == req.IDJogadorDesejado {
					log.Printf("[TROCA] Fallback: buscando carta %s no inventário de %s na cópia. Total: %d", req.IDCartaDesejada, j.Nome, len(j.Inventario))
					for _, c := range j.Inventario {
						if c.ID == req.IDCartaDesejada {
							cartaDesejada = c
							log.Printf("[TROCA] Carta desejada %s encontrada na cópia da sala", cartaDesejada.Nome)
							break
						}
					}
					break
				}
			}
			sala.Mutex.Unlock()
		}
	}

	if cartaDesejada.ID == "" {
		s.notificarErro(req.IDJogadorOferta, fmt.Sprintf("%s não possui esta carta.", req.NomeJogadorDesejado))
		return
	}

	// EXECUTA A TROCA - Remove carta do ofertante e adiciona carta desejada
	var novoInventario []tipos.Carta

	if jogadorReal != nil && idxOferta != 99 {
		// Atualiza inventário real do jogador local
		jogadorReal.Mutex.Lock()
		inventario := jogadorReal.Inventario
		inventario = append(inventario[:idxOferta], inventario[idxOferta+1:]...)
		inventario = append(inventario, cartaDesejada)
		jogadorReal.Inventario = inventario
		novoInventario = make([]tipos.Carta, len(inventario))
		copy(novoInventario, inventario)
		jogadorReal.Mutex.Unlock()
		log.Printf("[TROCA] Inventário real do ofertante atualizado: %d cartas", len(inventario))
	} else if idxOferta == 99 {
		// Carta foi encontrada em servidor remoto - precisa aplicar remoto
		log.Printf("[TROCA] Carta do ofertante veio do servidor remoto. Aplicando troca remota...")

		// Determina servidor do jogador ofertante
		var servidorJogadorOferta string
		if sala.ServidorHost == s.MeuEndereco {
			servidorJogadorOferta = sala.ServidorSombra
		} else {
			servidorJogadorOferta = sala.ServidorHost
		}

		// Aplica troca no servidor remoto do ofertante
		// Remove carta oferecida e adiciona carta desejada
		payload := map[string]interface{}{
			"cliente_id":        req.IDJogadorOferta,
			"carta_desejada_id": req.IDCartaOferecida, // Carta que será REMOVIDA
			"carta_oferecida":   cartaDesejada,        // Carta que será ADICIONADA
		}
		log.Printf("[TROCA] Aplicando troca remota: removendo %s e adicionando %s", req.IDCartaOferecida, cartaDesejada.Nome)
		body, _ := json.Marshal(payload)

		url := fmt.Sprintf("http://%s/partida/aplicar_troca_local", servidorJogadorOferta)
		log.Printf("[TROCA] Chamando %s", url)
		resp, err := s.enviarRequestComToken("POST", url, body)
		if err != nil {
			log.Printf("[TROCA] Erro ao aplicar troca no ofertante remoto: %v", err)
		} else if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var result struct {
					Status     string        `json:"status"`
					Inventario []tipos.Carta `json:"inventario"`
				}
				decoder := json.NewDecoder(resp.Body)
				if err := decoder.Decode(&result); err == nil {
					novoInventario = result.Inventario
					log.Printf("[TROCA] Inventário atualizado recebido do ofertante remoto: %d cartas", len(novoInventario))
					log.Printf("[TROCA] Primeira carta: %+v", result.Inventario[0])
				} else {
					log.Printf("[TROCA] Erro ao decodificar resposta: %v", err)
					// Lê o body para debug
					bodyBytes, _ := io.ReadAll(resp.Body)
					log.Printf("[TROCA] Resposta raw: %s", string(bodyBytes))
				}
			} else {
				log.Printf("[TROCA] Status code: %d", resp.StatusCode)
			}
			log.Printf("[TROCA] Troca aplicada no ofertante remoto com sucesso")
		}
	}

	// Se jogador desejado está local, executa a troca reversa
	if jogadorDesejadoLocal != nil {
		// Remove carta desejada e adiciona carta oferecida
		jogadorDesejadoLocal.Mutex.Lock()
		inventario := jogadorDesejadoLocal.Inventario
		idxDesejado := -1
		for i, c := range inventario {
			if c.ID == req.IDCartaDesejada {
				idxDesejado = i
				break
			}
		}
		if idxDesejado != -1 {
			inventario = append(inventario[:idxDesejado], inventario[idxDesejado+1:]...)
			inventario = append(inventario, cartaOferta)
			jogadorDesejadoLocal.Inventario = inventario
		}
		inventarioDesejado := make([]tipos.Carta, len(inventario))
		copy(inventarioDesejado, inventario)
		jogadorDesejadoLocal.Mutex.Unlock()
		log.Printf("[TROCA] Inventário real do desejado atualizado: %d cartas", len(inventario))

		// Notifica ambos
		s.notificarSucessoTrocaComInventario(req.IDJogadorOferta, cartaOferta.Nome, cartaDesejada.Nome, novoInventario)
		s.notificarSucessoTrocaComInventario(req.IDJogadorDesejado, cartaDesejada.Nome, cartaOferta.Nome, inventarioDesejado)
	} else {
		// Jogador desejado é remoto - solicita ao servidor dele aplicar a troca
		log.Printf("[TROCA] Jogador desejado é remoto. Enviando para aplicar troca...")
		payload := map[string]interface{}{
			"cliente_id":        req.IDJogadorDesejado,
			"carta_desejada_id": req.IDCartaDesejada,
			"carta_oferecida":   cartaOferta,
		}
		body, _ := json.Marshal(payload)

		// Determina servidor destino - lógica corrigida:
		// Se sou Host, jogador remoto está na Sombra
		// Se sou Shadow, jogador remoto está no Host
		servidorDestino := sala.ServidorSombra
		if s.MeuEndereco == sala.ServidorHost {
			// Sou Host, remoto está na Sombra
			servidorDestino = sala.ServidorSombra
		} else {
			// Sou Shadow, remoto está no Host
			servidorDestino = sala.ServidorHost
		}

		url := fmt.Sprintf("http://%s/partida/aplicar_troca_local", servidorDestino)
		log.Printf("[TROCA] Enviando para %s", url)
		resp, err := s.enviarRequestComToken("POST", url, body)
		if err != nil {
			log.Printf("[TROCA] Falha ao aplicar troca no remoto %s: %v", servidorDestino, err)
		} else if resp != nil {
			defer resp.Body.Close()
			log.Printf("[TROCA] Troca aplicada com sucesso no remoto")
		}

		// Notifica ambos os jogadores (a notificação para o desejado já foi enviada pelo aplicador remoto)
		log.Printf("[TROCA] Notificando ofertante com inventário de %d cartas", len(novoInventario))
		s.notificarSucessoTrocaComInventario(req.IDJogadorOferta, cartaOferta.Nome, cartaDesejada.Nome, novoInventario)
	}

	// Atualiza a cópia da sala
	sala.Mutex.Lock()
	for i := range sala.Jogadores {
		if sala.Jogadores[i].ID == req.IDJogadorOferta {
			sala.Jogadores[i].Inventario = novoInventario
			log.Printf("[TROCA] Cópia da sala atualizada para %s", req.NomeJogadorOferta)
			break
		}
	}
	sala.Mutex.Unlock()

	// Se o ofertante é local, também atualiza o inventário real
	s.mutexClientes.RLock()
	clienteOferta := s.Clientes[req.IDJogadorOferta]
	s.mutexClientes.RUnlock()
	if clienteOferta != nil {
		clienteOferta.Mutex.Lock()
		clienteOferta.Inventario = novoInventario
		clienteOferta.Mutex.Unlock()
		log.Printf("[TROCA] Inventário real do ofertante local atualizado para %d cartas", len(novoInventario))
	}

	log.Printf("[TROCA] === FIM PROCESSAMENTO TROCA === %s trocou %s por %s com %s", req.NomeJogadorOferta, cartaOferta.Nome, cartaDesejada.Nome, req.NomeJogadorDesejado)
}

// encaminharTrocaParaHost envia uma requisição de troca de cartas do Shadow para o Host via HTTP
func (s *Servidor) encaminharTrocaParaHost(hostAddr, salaID string, req *protocolo.TrocarCartasReq) {
	log.Printf("[TROCA_SHADOW] Encaminhando requisição de troca para o Host %s na sala %s", hostAddr, salaID)

	// Cria a mensagem de comando para encaminhar
	comando := protocolo.Mensagem{
		Comando: "TROCAR_CARTAS",
		Dados:   seguranca.MustJSON(req),
	}

	// Cria o payload para o endpoint
	payload := map[string]interface{}{
		"sala_id": salaID,
		"comando": comando,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[TROCA_SHADOW] Erro ao serializar requisição: %v", err)
		return
	}

	url := fmt.Sprintf("http://%s/partida/encaminhar_comando", hostAddr)

	// Cria cliente HTTP com timeout reduzido
	token := seguranca.GenerateJWT(s.MeuEndereco)
	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[TROCA_SHADOW] Erro ao criar request: %v", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	// Timeout mais curto para evitar travamentos
	httpClient := &http.Client{Timeout: 5 * time.Second}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[TROCA_SHADOW] Erro ao encaminhar: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("[TROCA_SHADOW] Troca encaminhada com sucesso ao Host")
	} else {
		log.Printf("[TROCA_SHADOW] Host retornou status %d", resp.StatusCode)
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

func cartasIguais(a, b []tipos.Carta) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
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

func (s *Servidor) notificarSucessoTrocaComInventario(clienteID, cartaPerdida, cartaGanha string, inventario []tipos.Carta) {
	msg := fmt.Sprintf("Troca realizada! Você deu '%s' e recebeu '%s'.", cartaPerdida, cartaGanha)
	resp := protocolo.TrocarCartasResp{
		Sucesso:              true,
		Mensagem:             msg,
		InventarioAtualizado: inventario,
	}
	s.publicarParaCliente(clienteID, protocolo.Mensagem{Comando: "TROCA_CONCLUIDA", Dados: seguranca.MustJSON(resp)})
}

func (s *Servidor) notificarJogadorRemoto(servidor string, clienteID string, msg protocolo.Mensagem) {
	log.Printf("[NOTIFICACAO-REMOTA] Notificando cliente %s no servidor %s", clienteID, servidor)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"cliente_id": clienteID,
		"mensagem":   msg,
	})

	resp, err := s.enviarRequestComToken("POST", fmt.Sprintf("http://%s/partida/notificar_jogador", servidor), reqBody)
	if err != nil {
		log.Printf("[NOTIFICACAO-REMOTA] Erro ao notificar cliente %s no servidor %s: %v", clienteID, servidor, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[NOTIFICACAO-REMOTA] Servidor remoto %s retornou status %d", servidor, resp.StatusCode)
	}
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
	// CORREÇÃO: Esta função assume que o lock da sala JÁ ESTÁ ativo!
	// Remove o lock interno para evitar deadlock

	// Cria contagem de cartas atualizada
	contagemCartas := make(map[string]int)
	jogadoresEstado := make([]tipos.JogadorEstado, 0, len(sala.Jogadores))

	for _, j := range sala.Jogadores {
		j.Mutex.Lock()
		contagemCartas[j.Nome] = len(j.Inventario)
		// Adiciona jogador ao estado com seu inventário
		invCopy := make([]tipos.Carta, len(j.Inventario))
		copy(invCopy, j.Inventario)
		jogadoresEstado = append(jogadoresEstado, tipos.JogadorEstado{
			ID:         j.ID,
			Inventario: invCopy,
		})
		j.Mutex.Unlock()
	}

	// Cria estado completo da partida
	return &tipos.EstadoPartida{
		SalaID:        sala.ID,
		Estado:        sala.Estado,
		TurnoDe:       sala.TurnoDe,
		CartasNaMesa:  sala.CartasNaMesa,
		PontosRodada:  sala.PontosRodada,
		PontosPartida: sala.PontosPartida,
		NumeroRodada:  sala.NumeroRodada,
		Prontos:       sala.Prontos,
		EventSeq:      sala.EventSeq,
		EventLog:      sala.EventLog,
		Jogadores:     jogadoresEstado,
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
	client := &http.Client{Timeout: 15 * time.Second}
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

// PublicarChatRemoto é chamado pela API quando o Shadow recebe um chat do Host
func (s *Servidor) PublicarChatRemoto(salaID, nomeJogador, texto string) {
	dadosChat := gin.H{
		"nomeJogador": nomeJogador,
		"texto":       texto,
	}
	msg := protocolo.Mensagem{
		Comando: "CHAT_RECEBIDO",
		Dados:   seguranca.MustJSON(dadosChat),
	}
	s.publicarEventoPartida(salaID, msg)
}

// retransmitirChat envia uma mensagem de chat para todos os jogadores na sala.
func (s *Servidor) retransmitirChat(sala *tipos.Sala, cliente *tipos.Cliente, texto string) {
	isHost := sala.ServidorHost == s.MeuEndereco
	isCrossServer := sala.ServidorSombra != ""

	// Log detalhado
	log.Printf("[CHAT:%s] Retransmitindo de '%s'. Host: %t, Cross-Server: %t", sala.ID, cliente.Nome, isHost, isCrossServer)

	// Monta a mensagem de chat
	dadosChat := gin.H{
		"nomeJogador": cliente.Nome,
		"texto":       texto,
	}
	msg := protocolo.Mensagem{
		Comando: "CHAT_RECEBIDO",
		Dados:   seguranca.MustJSON(dadosChat),
	}

	// 1. O servidor sempre publica em seu broker local.
	// Em partidas locais, isso notifica ambos.
	// Em partidas cross-server, isso notifica o jogador local.
	s.publicarEventoPartida(sala.ID, msg)
	log.Printf("[CHAT:%s] Publicado localmente via MQTT.", sala.ID)

	// 2. Se for o Host de uma partida cross-server, encaminha para o Shadow.
	if isHost && isCrossServer {
		go s.encaminharChatParaSombra(sala.ServidorSombra, sala.ID, cliente.Nome, texto)
	}
}

// encaminharChatParaSombra envia a mensagem de chat para o servidor Sombra via REST.
func (s *Servidor) encaminharChatParaSombra(sombraAddr, salaID, nomeJogador, texto string) {
	log.Printf("[CHAT-TX:%s] Encaminhando chat para Sombra em %s", salaID, sombraAddr)
	url := fmt.Sprintf("http://%s/game/chat", sombraAddr)
	payload, _ := json.Marshal(map[string]string{
		"sala_id":      salaID,
		"nome_jogador": nomeJogador,
		"texto":        texto,
	})

	// Implementa retry logic com backoff exponencial
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
		if err != nil {
			log.Printf("[CHAT-TX:%s] ERRO ao criar requisição para Sombra (tentativa %d/%d): %v", salaID, attempt, maxRetries, err)
			if attempt < maxRetries {
				backoff := time.Duration(attempt) * time.Second
				log.Printf("[RETRY] Aguardando %v antes da próxima tentativa...", backoff)
				time.Sleep(backoff)
				continue
			}
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+seguranca.GenerateJWT(s.ServerID))

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[CHAT-TX:%s] ERRO ao enviar chat para Sombra (tentativa %d/%d): %v", salaID, attempt, maxRetries, err)
			if attempt < maxRetries {
				backoff := time.Duration(attempt) * time.Second
				log.Printf("[RETRY] Aguardando %v antes da próxima tentativa...", backoff)
				time.Sleep(backoff)
				continue
			}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			log.Printf("[CHAT-TX:%s] Chat retransmitido para Sombra com sucesso (tentativa %d/%d).", salaID, attempt, maxRetries)
			return
		} else {
			log.Printf("[CHAT-TX:%s] ERRO: Sombra retornou status %d ao retransmitir chat (tentativa %d/%d).", salaID, resp.StatusCode, attempt, maxRetries)
			if attempt < maxRetries {
				backoff := time.Duration(attempt) * time.Second
				log.Printf("[RETRY] Aguardando %v antes da próxima tentativa...", backoff)
				time.Sleep(backoff)
				continue
			}
		}
	}
}

func (s *Servidor) processarComandoMQTT(topic string, payload []byte) {
	// Extrai salaID do tópico, se aplicável
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || parts[0] != "partidas" {
		// log.Printf("[CMD_MQTT_DEBUG] Tópico MQTT ignorado (não é de comando de partida): %s", topic)
		return
	}
	salaID := parts[1]

	var mensagem protocolo.Mensagem
	if err := json.Unmarshal(payload, &mensagem); err != nil {
		log.Printf("Erro ao decodificar mensagem MQTT: %v", err)
		return
	}

	timestamp := time.Now().Format("15:04:05.000")
	log.Printf("[%s][COMANDO_DEBUG] === INÍCIO PROCESSAMENTO COMANDO ===", timestamp)
	log.Printf("[%s][COMANDO_DEBUG] Comando recebido no tópico: %s", timestamp, topic)
	log.Printf("[%s][COMANDO_DEBUG] Payload: %s", timestamp, string(payload))
	log.Printf("[%s][COMANDO_DEBUG] Comando decodificado: %s", timestamp, mensagem.Comando)

	s.mutexSalas.RLock()
	sala, ok := s.Salas[salaID]
	s.mutexSalas.RUnlock()

	if !ok {
		log.Printf("[%s][COMANDO_DEBUG] ERRO: Sala %s não encontrada para o comando %s", timestamp, salaID, mensagem.Comando)
		return
	}

	// Extrai o clienteID do payload genérico para identificar o remetente
	var dadosComClienteID struct {
		ClienteID string `json:"cliente_id"`
		Texto     string `json:"texto"` // Para o caso de CHAT
	}
	if err := json.Unmarshal(mensagem.Dados, &dadosComClienteID); err != nil {
		log.Printf("[%s][COMANDO_DEBUG] ERRO: Falha ao extrair cliente_id do payload para o comando %s", timestamp, mensagem.Comando)
		return
	}
	clienteID := dadosComClienteID.ClienteID

	s.mutexClientes.RLock()
	cliente, clienteOk := s.Clientes[clienteID]
	s.mutexClientes.RUnlock()

	if !clienteOk {
		log.Printf("[%s][COMANDO_DEBUG] ERRO: Cliente %s não encontrado para o comando %s", timestamp, clienteID, mensagem.Comando)
		return
	}

	// Lógica de encaminhamento ou processamento
	isHost := sala.ServidorHost == s.MeuEndereco
	isCrossServer := sala.ServidorSombra != ""

	// Se este servidor NÃO for o host, encaminha o comando
	if !isHost && isCrossServer {
		log.Printf("[%s][COMANDO_DEBUG] SHADOW: Encaminhando comando '%s' do cliente %s para o Host %s", timestamp, mensagem.Comando, clienteID, sala.ServidorHost)

		// CORREÇÃO: Decodifica o payload para um mapa genérico para encaminhar os dados corretamente,
		// em vez de aninhá-los em um novo mapa.
		var dataParaEncaminhar map[string]interface{}
		if err := json.Unmarshal(mensagem.Dados, &dataParaEncaminhar); err != nil {
			log.Printf("[SHADOW] Erro ao decodificar dados do evento '%s' para encaminhamento: %v", mensagem.Comando, err)
			return
		}

		s.encaminharEventoParaHost(sala, clienteID, mensagem.Comando, dataParaEncaminhar)
		return
	}

	// Se for o Host, processa o comando
	switch mensagem.Comando {
	case "COMPRAR_PACOTE":
		log.Printf("[COMPRAR_DEBUG] Processando compra para cliente %s, souLider: %t", clienteID, s.ClusterManager.SouLider())
		s.processarCompraPacote(clienteID, sala)
	case "JOGAR_CARTA":
		var dadosJogada struct {
			CartaID string `json:"carta_id"`
		}
		if err := json.Unmarshal(mensagem.Dados, &dadosJogada); err == nil {
			evento := &tipos.GameEventRequest{
				MatchID:   sala.ID,
				EventType: "JOGAR_CARTA",
				PlayerID:  clienteID,
				Data:      map[string]interface{}{"carta_id": dadosJogada.CartaID},
			}
			s.processarEventoComoHost(sala, evento)
		}
	case "CHAT":
		log.Printf("[HOST-CHAT] Recebido chat de %s. Fazendo broadcast.", cliente.Nome)
		s.retransmitirChat(sala, cliente, dadosComClienteID.Texto)
	default:
		log.Printf("[%s][COMANDO_DEBUG] AVISO: Comando '%s' não reconhecido ou não manipulado diretamente.", timestamp, mensagem.Comando)
	}
	log.Printf("[%s][COMANDO_DEBUG] === FIM PROCESSAMENTO COMANDO ===", timestamp)
}

func (s *Servidor) processarLogin(payload []byte, tempID string) {
	// ... existing code ...
}

// forcarSincronizacaoEstado força a sincronização do estado da partida
func (s *Servidor) forcarSincronizacaoEstado(salaID string) {
	sala := s.Salas[salaID]
	if sala == nil || sala.ServidorSombra == "" {
		return
	}

	log.Printf("[FORCE_SYNC] Forçando sincronização de estado para sala %s", salaID)

	// CORREÇÃO: Agora precisamos bloquear porque criarEstadoDaSala espera o lock ativo
	sala.Mutex.Lock()
	estado := s.criarEstadoDaSala(sala)
	sala.Mutex.Unlock()

	if estado == nil {
		log.Printf("[FORCE_SYNC] Erro ao criar estado da sala %s", salaID)
		return
	}

	// Enviar estado para a sombra
	s.sincronizarEstadoComSombra(sala.ServidorSombra, estado)

	// Enviar atualização de jogo
	msg := protocolo.Mensagem{
		Comando: "ATUALIZACAO_JOGO",
		Dados:   seguranca.MustJSON(estado),
	}
	s.enviarAtualizacaoParaSombra(sala.ServidorSombra, msg)
}

// enviarRequestComToken é um helper para enviar requisições HTTP autenticadas para outros servidores
func (s *Servidor) enviarRequestComToken(method, url string, body []byte) (*http.Response, error) {
	token := seguranca.GenerateJWT(s.MeuEndereco)

	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		log.Printf("[AUTH_DEBUG] Erro ao criar request: %v", err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[AUTH_DEBUG] Erro ao executar request: %v", err)
	} else {
		log.Printf("[AUTH_DEBUG] Response status: %d", resp.StatusCode)
	}
	return resp, err
}
