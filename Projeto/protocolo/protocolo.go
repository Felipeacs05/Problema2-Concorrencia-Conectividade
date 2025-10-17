package protocolo

import "encoding/json"

// Envelope base para todas as mensagens do protocolo
type Mensagem struct {
	Comando string          `json:"comando"` // Tipo da operação (LOGIN, JOGAR_CARTA, etc.)
	Dados   json.RawMessage `json:"dados"`   // Payload específico de cada comando
}

/* ===================== Cartas / Inventário ===================== */

// Estrutura de dados para cartas do jogo
type Carta struct {
	ID       string `json:"id"`                 // Identificador único da carta no estoque global
	Nome     string `json:"nome"`               // Nome da carta para exibição
	Naipe    string `json:"naipe"`              // "♠", "♥", "♦", "♣" - usado para desempate
	Valor    int    `json:"valor"`              // Poder da carta (1..13, onde Ás=1, Rei=13)
	Raridade string `json:"raridade,omitempty"` // C=Comum, U=Incomum, R=Rara, L=Lendária
}

// Estrutura para solicitação de compra de pacotes
type ComprarPacoteReq struct {
	Quantidade int `json:"quantidade"` // Quantidade de pacotes desejados (padrão: 1)
}

// Resposta do servidor com as cartas adquiridas
type ComprarPacoteResp struct {
	Cartas          []Carta `json:"cartas"`          // Cartas recebidas no pacote
	EstoqueRestante int     `json:"estoqueRestante"` // Quantidade de cartas restantes no estoque global
}

// Estrutura para a nova funcionalidade de troca de cartas
type TrocarCartasReq struct {
	IDJogadorOferta     string `json:"id_jogador_oferta"`
	NomeJogadorOferta   string `json:"nome_jogador_oferta"`
	IDJogadorDesejado   string `json:"id_jogador_desejado"`
	NomeJogadorDesejado string `json:"nome_jogador_desejado"`
	IDCartaOferecida    string `json:"id_carta_oferecida"`
	IDCartaDesejada     string `json:"id_carta_desejada"`
}

type TrocarCartasResp struct {
	Sucesso  bool   `json:"sucesso"`
	Mensagem string `json:"mensagem"`
}

/* ===================== Login / Match / Chat ===================== */

// Dados para autenticação do jogador
type DadosLogin struct {
	Nome string `json:"nome"` // Nome único do jogador no sistema
}

// Notificação de que uma partida foi encontrada
type DadosPartidaEncontrada struct {
	SalaID       string `json:"salaID"`       // ID único da sala de jogo criada
	OponenteID   string `json:"oponenteID"`   // ID do oponente para referência
	OponenteNome string `json:"oponenteNome"` // Nome do oponente encontrado
}

// Dados para envio de mensagens de chat
type DadosEnviarChat struct {
	Texto string `json:"texto"` // Conteúdo da mensagem de chat
}

// Dados para jogada de carta
type DadosJogarCarta struct {
	CartaID string `json:"cartaID"` // ID da carta a ser jogada
}

// Dados para recebimento de mensagens de chat
type DadosReceberChat struct {
	NomeJogador string `json:"nomeJogador"` // Nome do jogador que enviou a mensagem
	Texto       string `json:"texto"`       // Conteúdo da mensagem
}

/* ===================== Atualizações de jogo ===================== */

// Estrutura principal para atualizações do estado do jogo
type DadosAtualizacaoJogo struct {
	MensagemDoTurno string           `json:"mensagemDoTurno"` // Mensagem descritiva do que aconteceu
	ContagemCartas  map[string]int   `json:"contagemCartas"`  // nome -> cartas restantes no inventário
	UltimaJogada    map[string]Carta `json:"ultimaJogada"`    // nome -> carta recém jogada na mesa
	VencedorJogada  string           `json:"vencedorJogada"`  // nome do vencedor da jogada atual / "EMPATE" / ""
	VencedorRodada  string           `json:"vencedorRodada"`  // nome do vencedor da rodada / "EMPATE" / ""
	NumeroRodada    int              `json:"numeroRodada"`    // Número da rodada atual (1, 2, 3...)
	PontosRodada    map[string]int   `json:"pontosRodada"`    // nome -> pontos na rodada atual
	PontosPartida   map[string]int   `json:"pontosPartida"`   // nome -> rodadas ganhas na partida
	SalaID          string           `json:"sala_id"`         // ID da sala para roteamento na sombra
}

// Notificação de fim de partida
type DadosFimDeJogo struct {
	VencedorNome string `json:"vencedorNome"` // Nome do vencedor final / "EMPATE" em caso de empate
	SalaID       string `json:"sala_id"`      // ID da sala para roteamento na sombra
}

// Comando representa uma ação de um jogador em uma partida
type Comando struct {
	ClienteID string
	Tipo      string // Ex: "JOGAR_CARTA"
	Payload   json.RawMessage
}

/* ===================== Erro ===================== */

// Estrutura para mensagens de erro
type DadosErro struct {
	Mensagem string `json:"mensagem"` // Descrição do erro ocorrido
}

/* ===================== Ping ===================== */

// Estrutura para medição de latência
type DadosPing struct {
	Timestamp int64 `json:"timestamp"` // Timestamp em milissegundos para cálculo de latência
}

// Resposta do servidor para medição de latência
type DadosPong struct {
	Timestamp int64 `json:"timestamp"` // Timestamp ecoado do ping original
}
