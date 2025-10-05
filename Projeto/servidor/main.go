package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Servidor representa um nó no nosso sistema distribuído.
type Servidor struct {
	Endereco   string    `json:"endereco"`
	UltimoPing time.Time `json:"ultimo_ping"`
}

var (
	// Lista de servidores conhecidos. A chave é o endereço do servidor.
	servidores      = make(map[string]*Servidor)
	mutexServidores = &sync.RWMutex{}
	meuEndereco     string
	bootstrapPeer   string
)

func main() {
	// Parâmetros da linha de comando
	flag.StringVar(&meuEndereco, "addr", "localhost:8080", "Endereço e porta para este servidor (ex: localhost:8080)")
	flag.StringVar(&bootstrapPeer, "peer", "", "Endereço de um peer para se conectar (ex: localhost:8081)")
	flag.Parse()

	log.Printf("Iniciando servidor em %s\n", meuEndereco)

	// Adiciona o próprio endereço à lista de servidores
	mutexServidores.Lock()
	servidores[meuEndereco] = &Servidor{
		Endereco:   meuEndereco,
		UltimoPing: time.Now(),
	}
	mutexServidores.Unlock()

	// Inicia o servidor da API REST
	go iniciarServidorAPI()

	// Se um peer foi especificado, tenta se registrar
	if bootstrapPeer != "" {
		go registrarComPeer()
	}

	// Inicia o envio de heartbeats para outros servidores
	go iniciarHeartbeats()

	// Inicia a verificação de peers inativos
	go verificarPeers()

	// Mantém a aplicação principal rodando
	log.Println("Servidor pronto.")
	select {}
}

func iniciarServidorAPI() {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// Endpoint para registrar um novo servidor
	router.POST("/register", func(c *gin.Context) {
		var novoServidor Servidor
		if err := c.ShouldBindJSON(&novoServidor); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		mutexServidores.Lock()
		novoServidor.UltimoPing = time.Now()
		servidores[novoServidor.Endereco] = &novoServidor
		mutexServidores.Unlock()

		c.JSON(http.StatusOK, servidores)
	})

	// Endpoint para obter a lista de todos os servidores
	router.GET("/servers", func(c *gin.Context) {
		mutexServidores.RLock()
		defer mutexServidores.RUnlock()
		c.JSON(http.StatusOK, servidores)
	})

	// Endpoint para heartbeat
	router.POST("/heartbeat", func(c *gin.Context) {
		var s Servidor
		if err := c.ShouldBindJSON(&s); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		mutexServidores.Lock()
		if peer, ok := servidores[s.Endereco]; ok {
			peer.UltimoPing = time.Now()
		} else {
			// Se o peer não é conhecido, adiciona à lista
			s.UltimoPing = time.Now()
			servidores[s.Endereco] = &s
			log.Printf("Novo peer detectado via heartbeat: %s\n", s.Endereco)
		}
		mutexServidores.Unlock()

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// A API vai rodar no endereço especificado
	log.Printf("Servidor API escutando em %s\n", meuEndereco)
	if err := router.Run(meuEndereco); err != nil {
		log.Fatalf("Falha ao iniciar servidor API: %v", err)
	}
}

func registrarComPeer() {
	url := fmt.Sprintf("http://%s/register", bootstrapPeer)
	log.Printf("Registrando com o peer: %s\n", url)

	requestBody, err := json.Marshal(Servidor{Endereco: meuEndereco})
	if err != nil {
		log.Printf("Erro ao criar corpo da requisição: %v", err)
		return
	}

	resp, err := http.Post(url, "application/json", strings.NewReader(string(requestBody)))
	if err != nil {
		log.Printf("Falha ao registrar com o peer: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var peersRecebidos map[string]*Servidor
		if err := json.NewDecoder(resp.Body).Decode(&peersRecebidos); err != nil {
			log.Printf("Erro ao decodificar a lista de peers: %v", err)
			return
		}

		mutexServidores.Lock()
		for addr, peer := range peersRecebidos {
			if _, ok := servidores[addr]; !ok {
				servidores[addr] = peer
			}
		}
		mutexServidores.Unlock()
		log.Printf("Registrado com sucesso. Peers conhecidos: %v\n", obterListaDeEnderecos())
	} else {
		log.Printf("Falha ao registrar. Status: %s\n", resp.Status)
	}
}

func iniciarHeartbeats() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		mutexServidores.RLock()
		peers := make([]*Servidor, 0, len(servidores))
		for _, peer := range servidores {
			if peer.Endereco != meuEndereco {
				peers = append(peers, peer)
			}
		}
		mutexServidores.RUnlock()

		for _, peer := range peers {
			go enviarHeartbeat(peer.Endereco)
		}
	}
}

func enviarHeartbeat(endereco string) {
	url := fmt.Sprintf("http://%s/heartbeat", endereco)
	requestBody, _ := json.Marshal(Servidor{Endereco: meuEndereco})

	resp, err := http.Post(url, "application/json", strings.NewReader(string(requestBody)))
	if err != nil {
		//log.Printf("Falha ao enviar heartbeat para %s: %v\n", endereco, err)
		return
	}
	defer resp.Body.Close()
}

func verificarPeers() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		mutexServidores.Lock()
		for addr, peer := range servidores {
			if addr != meuEndereco && time.Since(peer.UltimoPing) > 15*time.Second {
				log.Printf("Peer inativo detectado, removendo: %s\n", addr)
				delete(servidores, addr)
			}
		}
		mutexServidores.Unlock()
		log.Printf("Peers ativos: %v\n", obterListaDeEnderecos())
	}
}

func obterListaDeEnderecos() []string {
	mutexServidores.RLock()
	defer mutexServidores.RUnlock()
	enderecos := make([]string, 0, len(servidores))
	for addr := range servidores {
		enderecos = append(enderecos, addr)
	}
	return enderecos
}
