package models

import (
	"jogodistribuido/protocolo"
	"sync"
	"time"
)

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
	Inventario []protocolo.Carta
	Sala       *Sala
	Mutex      sync.Mutex
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
	ServidorHost   string      // Servidor responsável pela lógica da partida
	ServidorSombra string      // Servidor backup
	EventSeq       int64       // Sequência de eventos para ordenação
	EventLog       []GameEvent // Log append-only de eventos da partida
	Mutex          sync.Mutex
	TurnoDe        string // ID do jogador que deve jogar
}

// GameEvent representa um evento no log da partida
type GameEvent struct {
	EventSeq  int64       `json:"eventSeq"`  // Número sequencial do evento
	MatchID   string      `json:"matchId"`   // ID da partida
	Timestamp time.Time   `json:"timestamp"` // Quando o evento ocorreu
	EventType string      `json:"eventType"` // Tipo do evento (CARD_PLAYED, ROUND_END, etc.)
	PlayerID  string      `json:"playerId"`  // ID do jogador que gerou o evento
	Data      interface{} `json:"data"`      // Dados específicos do evento
	Signature string      `json:"signature"` // Assinatura HMAC do evento
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
	EventSeq      int64            `json:"eventSeq"` // Sequência de eventos
	EventLog      []GameEvent      `json:"eventLog"` // Log de eventos
	TurnoDe       string           `json:"turnoDe"`  // ID do jogador que deve jogar
}

// GameStartRequest representa a requisição para iniciar uma partida
type GameStartRequest struct {
	MatchID    string   `json:"matchId"`    // ID único da partida
	HostServer string   `json:"hostServer"` // Servidor Host
	Players    []Player `json:"players"`    // Lista de jogadores
	Token      string   `json:"token"`      // Token JWT para autenticação
}

// Player representa um jogador na partida
type Player struct {
	ID     string `json:"id"`     // ID do jogador
	Nome   string `json:"nome"`   // Nome do jogador
	Server string `json:"server"` // Servidor ao qual o jogador está conectado
}

// GameEventRequest representa um evento de jogo
type GameEventRequest struct {
	MatchID   string      `json:"matchId"`   // ID da partida
	EventSeq  int64       `json:"eventSeq"`  // Número sequencial do evento
	EventType string      `json:"eventType"` // Tipo do evento
	PlayerID  string      `json:"playerId"`  // ID do jogador
	Data      interface{} `json:"data"`      // Dados do evento
	Token     string      `json:"token"`     // Token JWT
	Signature string      `json:"signature"` // Assinatura HMAC
}

// GameReplicateRequest representa uma replicação de estado
type GameReplicateRequest struct {
	MatchID   string        `json:"matchId"`   // ID da partida
	EventSeq  int64         `json:"eventSeq"`  // Sequência do evento
	State     EstadoPartida `json:"state"`     // Estado completo
	Token     string        `json:"token"`     // Token JWT
	Signature string        `json:"signature"` // Assinatura HMAC
}
