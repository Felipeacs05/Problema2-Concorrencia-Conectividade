package game

import (
	"fmt"
	"jogodistribuido/protocolo"
	"jogodistribuido/servidor/seguranca"
	"jogodistribuido/servidor/tipos"
	"log"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	PACOTE_SIZE = 5
)

// GameManagerInterface defines the interface for game management
type GameManagerInterface interface {
	GetClientes() map[string]*tipos.Cliente
	GetSalas() map[string]*tipos.Sala
	GetFilaDeEspera() []*tipos.Cliente
	GetComandosPartida() map[string]chan protocolo.Comando
	GetMeuEndereco() string
	GetMeuEnderecoHTTP() string
	GetBrokerMQTT() string
	GetMQTTClient() mqtt.Client
	GetClusterManager() GameClusterManagerInterface
	GetStore() GameStoreInterface
	PublicarParaCliente(string, protocolo.Mensagem)
	PublicarEventoPartida(string, protocolo.Mensagem)
	NotificarCompraSucesso(string, []tipos.Carta)
	GetStatusEstoque() (map[string]int, int)
}

// GameClusterManagerInterface defines the interface for cluster management in game context
type GameClusterManagerInterface interface {
	SouLider() bool
	GetLider() string
	GetServidores() map[string]*tipos.InfoServidor
}

// GameStoreInterface defines the interface for store management in game context
type GameStoreInterface interface {
	FormarPacote(int) []tipos.Carta
	GetStatusEstoque() (map[string]int, int)
}

// Manager handles all game-related logic
type Manager struct {
	gameInterface GameManagerInterface
	mutex         sync.RWMutex
}

// NewManager creates a new game manager
func NewManager(gi GameManagerInterface) *Manager {
	return &Manager{
		gameInterface: gi,
	}
}

// EntrarFila adds a client to the matchmaking queue
func (m *Manager) EntrarFila(cliente *tipos.Cliente) {
	fila := m.gameInterface.GetFilaDeEspera()

	// Try to find opponent in local queue first
	if len(fila) > 0 {
		oponente := fila[0]
		// Remove from queue (this would need to be handled by the main server)
		// For now, we'll just create the room
		m.CriarSala(oponente, cliente)
		return
	}

	// If no opponent found, add to local queue
	// This would need to be handled by the main server
	log.Printf("Cliente %s (%s) entrou na fila de espera.", cliente.Nome, cliente.ID)
	m.gameInterface.PublicarParaCliente(cliente.ID, protocolo.Mensagem{
		Comando: "AGUARDANDO_OPONENTE",
		Dados:   seguranca.MustJSON(map[string]string{"mensagem": "Procurando oponente em todos os servidores..."}),
	})
}

// CriarSala creates a new game room with two players
func (m *Manager) CriarSala(j1, j2 *tipos.Cliente) {
	salaID := fmt.Sprintf("%d", time.Now().UnixNano())

	sala := &tipos.Sala{
		ID:             salaID,
		Jogadores:      []*tipos.Cliente{j1, j2},
		Prontos:        make(map[string]bool),
		Estado:         "AGUARDANDO_COMPRAS",
		ServidorHost:   j1.ID,
		ServidorSombra: j2.ID,
		Mutex:          sync.Mutex{},
	}

	// Add room to server (this would need to be handled by the main server)
	log.Printf("Sala criada: %s com jogadores %s e %s", salaID, j1.Nome, j2.Nome)

	// Store the room (this would need to be handled by the main server)
	_ = sala // Placeholder to avoid unused variable warning

	// Notify both players
	m.gameInterface.PublicarParaCliente(j1.ID, protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   seguranca.MustJSON(map[string]string{"sala_id": salaID, "oponente": j2.Nome}),
	})

	m.gameInterface.PublicarParaCliente(j2.ID, protocolo.Mensagem{
		Comando: "PARTIDA_ENCONTRADA",
		Dados:   seguranca.MustJSON(map[string]string{"sala_id": salaID, "oponente": j1.Nome}),
	})
}

// ProcessarCompraPacote handles card package purchase
func (m *Manager) ProcessarCompraPacote(clienteID string, sala *tipos.Sala) {
	souLider := m.gameInterface.GetClusterManager().SouLider()

	log.Printf("[COMPRAR_DEBUG] Processando compra para cliente %s, souLider: %v", clienteID, souLider)

	cartas := make([]tipos.Carta, 0)

	if souLider {
		cartas = m.gameInterface.GetStore().FormarPacote(PACOTE_SIZE)
		log.Printf("[COMPRAR_DEBUG] Líder retirou %d cartas do estoque", len(cartas))
	} else {
		// Make HTTP request to leader
		// This would need to be implemented with HTTP client
		log.Printf("[COMPRAR_DEBUG] Encaminhando compra para líder")
	}

	// Add cards to client inventory
	clientes := m.gameInterface.GetClientes()
	cliente, exists := clientes[clienteID]
	if !exists {
		log.Printf("Cliente %s não encontrado", clienteID)
		return
	}

	cliente.Mutex.Lock()
	cliente.Inventario = append(cliente.Inventario, cartas...)
	cliente.Mutex.Unlock()

	// Notify client
	_, total := m.gameInterface.GetStatusEstoque()
	msg := protocolo.Mensagem{
		Comando: "PACOTE_RESULTADO",
		Dados: seguranca.MustJSON(protocolo.ComprarPacoteResp{
			Cartas:          cartas,
			EstoqueRestante: total,
		}),
	}

	// Notify locally via MQTT or remotely via API
	if m.getClienteLocal(clienteID) != nil {
		m.gameInterface.PublicarParaCliente(clienteID, msg)
	} else {
		sala.Mutex.Lock()
		if sala.ServidorHost == clienteID {
			// Notify shadow server
			m.notificarSombra(sala, msg)
		} else {
			// Notify host server
			m.notificarHost(sala, msg)
		}
		sala.Mutex.Unlock()
	}

	// Mark as ready and check if both players are ready
	sala.Mutex.Lock()
	sala.Prontos[clienteID] = true
	sala.Mutex.Unlock()

	m.VerificarEIniciarPartidaSeProntos(sala)
}

// VerificarEIniciarPartidaSeProntos checks if both players are ready and starts the game
func (m *Manager) VerificarEIniciarPartidaSeProntos(sala *tipos.Sala) {
	sala.Mutex.Lock()
	prontos := len(sala.Prontos)
	totalJogadores := len(sala.Jogadores)
	sala.Mutex.Unlock()

	if prontos == totalJogadores {
		log.Printf("Ambos os jogadores prontos, iniciando partida na sala %s", sala.ID)
		m.IniciarPartida(sala)
	} else {
		log.Printf("Aguardando jogadores ficarem prontos: %d/%d", prontos, totalJogadores)
	}
}

// IniciarPartida starts the game
func (m *Manager) IniciarPartida(sala *tipos.Sala) {
	sala.Mutex.Lock()
	sala.Estado = "EM_JOGO"
	sala.Mutex.Unlock()

	// Notify both players
	msg := protocolo.Mensagem{
		Comando: "PARTIDA_INICIADA",
		Dados:   seguranca.MustJSON(map[string]string{"sala_id": sala.ID}),
	}

	for _, jogador := range sala.Jogadores {
		m.gameInterface.PublicarParaCliente(jogador.ID, msg)
	}

	// Broadcast to room
	m.gameInterface.PublicarEventoPartida(sala.ID, msg)
}

// BroadcastChat handles chat messages
func (m *Manager) BroadcastChat(sala *tipos.Sala, texto, remetenteNome string) {
	msg := protocolo.Mensagem{
		Comando: "CHAT_RECEBIDO",
		Dados: seguranca.MustJSON(protocolo.DadosReceberChat{
			NomeJogador: remetenteNome,
			Texto:       texto,
		}),
	}

	// Send to all players in the room
	for _, jogador := range sala.Jogadores {
		m.gameInterface.PublicarParaCliente(jogador.ID, msg)
	}

	// Broadcast to room
	m.gameInterface.PublicarEventoPartida(sala.ID, msg)
}

// Helper functions
func (m *Manager) getClienteLocal(clienteID string) *tipos.Cliente {
	clientes := m.gameInterface.GetClientes()
	return clientes[clienteID]
}

func (m *Manager) notificarSombra(sala *tipos.Sala, msg protocolo.Mensagem) {
	// Implementation would send HTTP request to shadow server
	log.Printf("Notificando servidor sombra para sala %s", sala.ID)
}

func (m *Manager) notificarHost(sala *tipos.Sala, msg protocolo.Mensagem) {
	// Implementation would send HTTP request to host server
	log.Printf("Notificando servidor host para sala %s", sala.ID)
}
