package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"jogodistribuido/servidor/tipos"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ELEICAO_TIMEOUT     = 10 * time.Second
	HEARTBEAT_INTERVALO = 3 * time.Second
)

type ServidorInterface interface {
	GetMeuEndereco() string
}

// ClusterManagerInterface define as operações que o manager do cluster expõe
// para outras partes do sistema, como a API.
type ClusterManagerInterface interface {
	GetServidores() map[string]*tipos.InfoServidor
	ProcessarHeartbeat(string, map[string]interface{})
	ProcessarVoto(string, int64) (bool, int64)
	DeclararLider(string, int64)
	RegistrarServidor(*tipos.InfoServidor) map[string]*tipos.InfoServidor
	GetLider() string
	SouLider() bool
	Run()
}

// Manager gere o estado do cluster, descoberta e eleições.
type Manager struct {
	servidor ServidorInterface
	mutex    sync.RWMutex
	// Campos relacionados com o cluster que estavam no Servidor
	Servidores      map[string]*tipos.InfoServidor
	souLider        bool
	LiderAtual      string
	TermoAtual      int64
	UltimoHeartbeat time.Time
}

func NewManager(s ServidorInterface) *Manager {
	return &Manager{
		servidor:   s,
		Servidores: make(map[string]*tipos.InfoServidor),
	}
}

func (m *Manager) Run() {
	go m.descobrirServidores()
	go m.enviarHeartbeats()
	go m.processoEleicao()
}

// descobrirServidores tenta se conectar a peers conhecidos para se registrar.
func (m *Manager) descobrirServidores() {
	peersStr := os.Getenv("PEERS")
	if peersStr == "" {
		log.Println("Nenhum peer inicial fornecido. Assumindo ser o primeiro nó.")
		return
	}
	peers := strings.Split(peersStr, ",")
	for _, peerAddr := range peers {
		peerAddr = strings.TrimSpace(peerAddr)
		if peerAddr != "" && peerAddr != m.servidor.GetMeuEndereco() {
			go m.registrarComPeer(peerAddr)
		}
	}
}

func (m *Manager) registrarComPeer(peerAddr string) {
	endpoint := fmt.Sprintf("http://%s/register", peerAddr)
	meuInfo := tipos.InfoServidor{
		Endereco:   m.servidor.GetMeuEndereco(),
		UltimoPing: time.Now(),
		Ativo:      true,
	}
	body, _ := json.Marshal(meuInfo)

	for i := 0; i < 5; i++ { // Tenta 5 vezes
		resp, err := http.Post(endpoint, "application/json", bytes.NewBuffer(body))
		if err != nil {
			log.Printf("Falha ao registrar com o peer %s: %v. Tentando novamente em 5s...", peerAddr, err)
			time.Sleep(5 * time.Second)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var peersRecebidos map[string]*tipos.InfoServidor
			if err := json.NewDecoder(resp.Body).Decode(&peersRecebidos); err == nil {
				m.mutex.Lock()
				for addr, info := range peersRecebidos {
					if _, existe := m.Servidores[addr]; !existe {
						m.Servidores[addr] = info
						log.Printf("Peer descoberto via registro: %s", addr)
					}
				}
				// Garante que o peer que respondeu e eu mesmo estamos na lista
				m.Servidores[peerAddr] = &tipos.InfoServidor{Endereco: peerAddr, Ativo: true, UltimoPing: time.Now()}
				m.Servidores[m.servidor.GetMeuEndereco()] = &meuInfo
				m.mutex.Unlock()
				log.Printf("Registrado com sucesso no peer %s e lista de servidores atualizada.", peerAddr)
				return // Sucesso
			}
		}
		log.Printf("Peer %s respondeu com status %d. Tentando novamente em 5s...", peerAddr, resp.StatusCode)
		time.Sleep(5 * time.Second)
	}
	log.Printf("Não foi possível registrar com o peer %s após várias tentativas.", peerAddr)
}

// enviarHeartbeats envia pings para outros servidores para mantê-los informados.
func (m *Manager) enviarHeartbeats() {
	ticker := time.NewTicker(HEARTBEAT_INTERVALO)
	defer ticker.Stop()
	for range ticker.C {
		m.mutex.RLock()
		peers := make([]string, 0, len(m.Servidores))
		for addr := range m.Servidores {
			if addr != m.servidor.GetMeuEndereco() {
				peers = append(peers, addr)
			}
		}
		m.mutex.RUnlock()

		payload := map[string]interface{}{
			"remetente": m.servidor.GetMeuEndereco(),
		}
		// Somente o líder anexa seu status ao heartbeat
		m.mutex.RLock()
		if m.souLider {
			payload["lider"] = m.LiderAtual
		}
		m.mutex.RUnlock()

		jsonData, _ := json.Marshal(payload)

		for _, addr := range peers {
			go func(addr string) {
				url := fmt.Sprintf("http://%s/heartbeat", addr)
				http.Post(url, "application/json", bytes.NewBuffer(jsonData))
			}(addr)
		}
	}
}

// processoEleicao verifica a necessidade de uma nova eleição.
func (m *Manager) processoEleicao() {
	// Atraso inicial aleatório para evitar eleições simultâneas no início
	time.Sleep(time.Duration(5+rand.Intn(5)) * time.Second)

	for {
		m.mutex.RLock()
		ultimoPingLider := m.UltimoHeartbeat
		liderExiste := m.LiderAtual != ""
		m.mutex.RUnlock()

		// Se não há líder ou o último heartbeat do líder foi há muito tempo, inicia a eleição
		if !liderExiste || time.Since(ultimoPingLider) > ELEICAO_TIMEOUT {
			log.Printf("Líder inativo ou inexistente. Iniciando nova eleição.")
			m.iniciarEleicao()
		}
		// Verifica a cada X segundos
		time.Sleep(ELEICAO_TIMEOUT / 2)
	}
}

func (m *Manager) iniciarEleicao() {
	m.mutex.Lock()
	m.TermoAtual++
	termoCandidato := m.TermoAtual
	// Vota em si mesmo
	votos := 1
	m.mutex.Unlock()

	log.Printf("Iniciando eleição para o termo %d", termoCandidato)

	m.mutex.RLock()
	totalServidores := len(m.Servidores)
	peers := make([]string, 0, totalServidores)
	for addr, srv := range m.Servidores {
		// Envia pedido de voto para todos os outros servidores ativos
		if addr != m.servidor.GetMeuEndereco() && srv.Ativo {
			peers = append(peers, addr)
		}
	}
	m.mutex.RUnlock()

	// Se for o único servidor, torna-se líder imediatamente
	if totalServidores <= 1 {
		log.Println("Servidor único, tornando-se líder.")
		m.tornarLider()
		return
	}

	maioriaNecessaria := (totalServidores / 2) + 1
	votosRecebidos := make(chan bool, len(peers))
	ctx, cancel := context.WithTimeout(context.Background(), ELEICAO_TIMEOUT)
	defer cancel()

	// Envia pedidos de voto em paralelo
	for _, addr := range peers {
		go func(addr string) {
			url := fmt.Sprintf("http://%s/election/vote", addr)
			reqBody, _ := json.Marshal(map[string]interface{}{
				"candidato": m.servidor.GetMeuEndereco(),
				"termo":     termoCandidato,
			})

			httpClient := &http.Client{Timeout: 2 * time.Second}
			req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqBody))
			req.Header.Set("Content-Type", "application/json")

			resp, err := httpClient.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				var res map[string]interface{}
				if json.NewDecoder(resp.Body).Decode(&res) == nil {
					if voto, ok := res["voto_concedido"].(bool); ok && voto {
						votosRecebidos <- true
					}
				}
				resp.Body.Close()
			}
		}(addr)
	}

	// Coleta de votos
	for {
		select {
		case <-votosRecebidos:
			votos++
			log.Printf("Voto recebido. Total de votos: %d/%d", votos, maioriaNecessaria)
			if votos >= maioriaNecessaria {
				log.Printf("Maioria alcançada. Tornando-se líder para o termo %d.", termoCandidato)
				m.tornarLider()
				return
			}
		case <-ctx.Done():
			log.Printf("Timeout da eleição para o termo %d. Votos obtidos: %d.", termoCandidato, votos)
			return // Fim da eleição por timeout
		}
	}
}

func (m *Manager) tornarLider() {
	m.mutex.Lock()
	m.souLider = true
	m.LiderAtual = m.servidor.GetMeuEndereco()
	termoAtual := m.TermoAtual
	m.mutex.Unlock()

	log.Printf("================ SOU O LÍDER (Termo: %d) ================", termoAtual)

	// Notifica todos os outros servidores sobre a liderança
	m.mutex.RLock()
	peers := make([]string, 0, len(m.Servidores))
	for addr := range m.Servidores {
		if addr != m.servidor.GetMeuEndereco() {
			peers = append(peers, addr)
		}
	}
	m.mutex.RUnlock()

	reqBody, _ := json.Marshal(map[string]interface{}{
		"novo_lider": m.servidor.GetMeuEndereco(),
		"termo":      termoAtual,
	})

	for _, addr := range peers {
		go func(addr string) {
			url := fmt.Sprintf("http://%s/election/leader", addr)
			http.Post(url, "application/json", bytes.NewBuffer(reqBody))
		}(addr)
	}
}

// Implementação da ClusterManagerInterface

func (m *Manager) GetServidores() map[string]*tipos.InfoServidor {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	// Retorna uma cópia para segurança
	servidores := make(map[string]*tipos.InfoServidor)
	for k, v := range m.Servidores {
		servidores[k] = v
	}
	return servidores
}

func (m *Manager) ProcessarHeartbeat(endereco string, dados map[string]interface{}) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if servidor, existe := m.Servidores[endereco]; existe {
		servidor.UltimoPing = time.Now()
		servidor.Ativo = true
	} else {
		m.Servidores[endereco] = &tipos.InfoServidor{
			Endereco:   endereco,
			UltimoPing: time.Now(),
			Ativo:      true,
		}
		log.Printf("Novo servidor descoberto via heartbeat: %s", endereco)
	}

	if lider, ok := dados["lider"].(string); ok && lider != "" {
		if m.LiderAtual != lider {
			log.Printf("Heartbeat recebido de %s, que reporta o líder como %s", endereco, lider)
		}
		m.LiderAtual = lider
		m.souLider = (lider == m.servidor.GetMeuEndereco())
		m.UltimoHeartbeat = time.Now()
	}
}

func (m *Manager) ProcessarVoto(candidato string, termo int64) (bool, int64) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if termo > m.TermoAtual {
		m.TermoAtual = termo
		m.LiderAtual = "" // Anula o líder ao votar em um novo termo
		log.Printf("Votando em %s para termo %d", candidato, termo)
		return true, m.TermoAtual
	}
	return false, m.TermoAtual
}

func (m *Manager) DeclararLider(novoLider string, termo int64) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if termo >= m.TermoAtual {
		m.TermoAtual = termo
		m.LiderAtual = novoLider
		m.souLider = (novoLider == m.servidor.GetMeuEndereco())
		m.UltimoHeartbeat = time.Now()
		log.Printf("Novo líder reconhecido via declaração: %s (termo %d)", novoLider, termo)
	}
}

func (m *Manager) RegistrarServidor(novoServidor *tipos.InfoServidor) map[string]*tipos.InfoServidor {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	log.Printf("Registrando novo servidor: %s", novoServidor.Endereco)

	// Retorna a lista atual ANTES de adicionar o novo, como no original
	servidoresAtuais := make(map[string]*tipos.InfoServidor)
	for k, v := range m.Servidores {
		servidoresAtuais[k] = v
	}

	m.Servidores[novoServidor.Endereco] = novoServidor

	return servidoresAtuais
}

func (m *Manager) GetLider() string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.LiderAtual
}

func (m *Manager) SouLider() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.souLider
}
