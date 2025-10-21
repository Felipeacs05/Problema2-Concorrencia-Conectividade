package mqtt

import (
	"encoding/json"
	"fmt"
	"jogodistribuido/protocolo"
	"jogodistribuido/servidor/internal/seguranca"
	"jogodistribuido/servidor/internal/models"
	"log"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTManagerInterface defines the interface for MQTT management
type MQTTManagerInterface interface {
	GetClientes() map[string]*models.Cliente
	GetSalas() map[string]*models.Sala
	GetFilaDeEspera() []*models.Cliente
	GetComandosPartida() map[string]chan protocolo.Comando
	GetMeuEndereco() string
	GetMeuEnderecoHTTP() string
	GetBrokerMQTT() string
	GetMQTTClient() mqtt.Client
	GetClusterManager() MQTTClusterManagerInterface
	GetStore() MQTTStoreInterface
	GetGameManager() MQTTGameManagerInterface
	PublicarParaCliente(string, protocolo.Mensagem)
	PublicarEventoPartida(string, protocolo.Mensagem)
	NotificarCompraSucesso(string, []models.Carta)
	GetStatusEstoque() (map[string]int, int)
}

// MQTTClusterManagerInterface defines the interface for cluster management in MQTT context
type MQTTClusterManagerInterface interface {
	SouLider() bool
	GetLider() string
	GetServidores() map[string]*models.InfoServidor
}

// MQTTStoreInterface defines the interface for store management in MQTT context
type MQTTStoreInterface interface {
	FormarPacote(int) []models.Carta
	GetStatusEstoque() (map[string]int, int)
}

// MQTTGameManagerInterface defines the interface for game management in MQTT context
type MQTTGameManagerInterface interface {
	EntrarFila(*models.Cliente)
	CriarSala(*models.Cliente, *models.Cliente)
	ProcessarCompraPacote(string, *models.Sala)
	VerificarEIniciarPartidaSeProntos(*models.Sala)
	IniciarPartida(*models.Sala)
	BroadcastChat(*models.Sala, string, string)
}

// Manager handles all MQTT-related logic
type Manager struct {
	mqttInterface MQTTManagerInterface
	mutex         sync.RWMutex
}

// NewManager creates a new MQTT manager
func NewManager(mi MQTTManagerInterface) *Manager {
	return &Manager{
		mqttInterface: mi,
	}
}

// ConectarMQTT establishes MQTT connection
func (m *Manager) ConectarMQTT() error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(m.mqttInterface.GetBrokerMQTT())
	opts.SetClientID(fmt.Sprintf("servidor_%s", m.mqttInterface.GetMeuEndereco()))
	opts.SetKeepAlive(60 * time.Second)
	opts.SetPingTimeout(1 * time.Second)
	opts.SetAutoReconnect(true)
	opts.SetOnConnectHandler(m.onConnect)
	opts.SetConnectionLostHandler(m.onConnectionLost)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("erro ao conectar MQTT: %v", token.Error())
	}

	// Store client reference (this would need to be handled by main server)
	log.Printf("Conectado ao broker MQTT: %s", m.mqttInterface.GetBrokerMQTT())
	return nil
}

// onConnect handles MQTT connection
func (m *Manager) onConnect(client mqtt.Client) {
	log.Println("Conectado ao broker MQTT")

	// Subscribe to server discovery topic
	serverTopic := "servidores/descoberta"
	if token := client.Subscribe(serverTopic, 0, m.handleDescobertaServidor); token.Wait() && token.Error() != nil {
		log.Printf("Erro ao subscrever tópico %s: %v", serverTopic, token.Error())
	}

	// Subscribe to client login topic
	loginTopic := "clientes/+/login"
	if token := client.Subscribe(loginTopic, 0, m.handleLogin); token.Wait() && token.Error() != nil {
		log.Printf("Erro ao subscrever tópico %s: %v", loginTopic, token.Error())
	}

	// Subscribe to game command topics
	comandoTopic := "partidas/+/comandos"
	if token := client.Subscribe(comandoTopic, 0, m.handleComandoPartida); token.Wait() && token.Error() != nil {
		log.Printf("Erro ao subscrever tópico %s: %v", comandoTopic, token.Error())
	}
}

// onConnectionLost handles MQTT disconnection
func (m *Manager) onConnectionLost(client mqtt.Client, err error) {
	log.Printf("Conexão MQTT perdida: %v", err)
}

// handleDescobertaServidor handles server discovery messages
func (m *Manager) handleDescobertaServidor(client mqtt.Client, msg mqtt.Message) {
	var info models.InfoServidor
	if err := json.Unmarshal(msg.Payload(), &info); err != nil {
		log.Printf("Erro ao decodificar descoberta de servidor: %v", err)
		return
	}

	// Process server discovery (this would need to be handled by cluster manager)
	log.Printf("Servidor descoberto: %s", info.Endereco)
}

// handleLogin handles client login messages
func (m *Manager) handleLogin(client mqtt.Client, msg mqtt.Message) {
	var loginReq protocolo.DadosLogin
	if err := json.Unmarshal(msg.Payload(), &loginReq); err != nil {
		log.Printf("Erro ao decodificar login: %v", err)
		return
	}

	// Extract client ID from topic
	topicParts := strings.Split(msg.Topic(), "/")
	if len(topicParts) < 3 {
		log.Printf("Tópico de login inválido: %s", msg.Topic())
		return
	}
	clienteID := topicParts[1]

	// Create client
	cliente := &models.Cliente{
		ID:         clienteID,
		Nome:       loginReq.Nome,
		Inventario: make([]models.Carta, 0),
		Mutex:      sync.Mutex{},
	}

	// Add client to server (this would need to be handled by main server)
	log.Printf("Cliente %s (%s) fez login", cliente.Nome, cliente.ID)

	// Send login response
	resp := protocolo.Mensagem{
		Comando: "LOGIN_OK",
		Dados:   seguranca.MustJSON(map[string]string{"cliente_id": clienteID}),
	}
	m.mqttInterface.PublicarParaCliente(clienteID, resp)

	// Add to matchmaking queue
	m.mqttInterface.GetGameManager().EntrarFila(cliente)
}

// handleComandoPartida handles game command messages
func (m *Manager) handleComandoPartida(client mqtt.Client, msg mqtt.Message) {
	var comando protocolo.Mensagem
	if err := json.Unmarshal(msg.Payload(), &comando); err != nil {
		log.Printf("Erro ao decodificar comando: %v", err)
		return
	}

	// Extract room ID from topic
	topicParts := strings.Split(msg.Topic(), "/")
	if len(topicParts) < 3 {
		log.Printf("Tópico de comando inválido: %s", msg.Topic())
		return
	}
	salaID := topicParts[1]

	// Get room
	salas := m.mqttInterface.GetSalas()
	sala, exists := salas[salaID]
	if !exists {
		log.Printf("Sala %s não encontrada", salaID)
		return
	}

	// Process command based on type
	switch comando.Comando {
	case "COMPRAR_PACOTE":
		var dados protocolo.ComprarPacoteReq
		if err := json.Unmarshal(comando.Dados, &dados); err != nil {
			log.Printf("Erro ao decodificar dados de compra: %v", err)
			return
		}
		// Extract client ID from topic or use a default approach
		// This would need to be handled properly by the main server
		m.mqttInterface.GetGameManager().ProcessarCompraPacote("", sala)

	case "ENVIAR_CHAT":
		var dados protocolo.DadosEnviarChat
		if err := json.Unmarshal(comando.Dados, &dados); err != nil {
			log.Printf("Erro ao decodificar dados de chat: %v", err)
			return
		}
		// Get client name
		clientes := m.mqttInterface.GetClientes()
		cliente, exists := clientes[dados.ClienteID]
		if !exists {
			log.Printf("Cliente %s não encontrado", dados.ClienteID)
			return
		}
		m.mqttInterface.GetGameManager().BroadcastChat(sala, dados.Texto, cliente.Nome)

	default:
		log.Printf("Comando não reconhecido: %s", comando.Comando)
	}
}

// PublicarParaCliente publishes a message to a specific client
func (m *Manager) PublicarParaCliente(clienteID string, msg protocolo.Mensagem) {
	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("clientes/%s/eventos", clienteID)
	m.mqttInterface.GetMQTTClient().Publish(topico, 0, false, payload)
}

// PublicarEventoPartida publishes a message to a game room
func (m *Manager) PublicarEventoPartida(salaID string, msg protocolo.Mensagem) {
	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("partidas/%s/eventos", salaID)
	m.mqttInterface.GetMQTTClient().Publish(topico, 0, false, payload)
}
