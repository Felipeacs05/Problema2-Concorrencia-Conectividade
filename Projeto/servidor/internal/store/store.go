package store

import (
	"jogodistribuido/servidor/internal/models"
	"log"
	"math/rand"
	"sync"
)

// StoreInterface define as operações que o Store de cartas expõe.
type StoreInterface interface {
	FormarPacote(tamanho int) []models.Carta
	GetStatusEstoque() (map[string]int, int)
}

// Store gerencia o estoque global de cartas.
type Store struct {
	mutex   sync.RWMutex
	Estoque map[string][]models.Carta
}

// NewStore cria e inicializa um novo Store.
func NewStore() *Store {
	s := &Store{
		Estoque: make(map[string][]models.Carta),
	}
	s.inicializarEstoque()
	return s
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func (s *Store) inicializarEstoque() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Estoque = map[string][]models.Carta{
		"C": make([]models.Carta, 0),
		"U": make([]models.Carta, 0),
		"R": make([]models.Carta, 0),
		"L": make([]models.Carta, 0),
	}

	modelsCartas := []string{
		"Dragão", "Guerreiro", "Mago", "Anjo", "Demônio", "Fênix", "Titan", "Sereia",
		"Lobo", "Águia", "Leão", "Tigre", "Cavaleiro", "Arqueiro", "Bárbaro", "Paladino",
	}
	naipes := []string{"♠", "♥", "♦", "♣"}

	for _, nome := range modelsCartas {
		for i := 0; i < 100; i++ { // Comuns
			s.Estoque["C"] = append(s.Estoque["C"], models.Carta{ID: randomString(5), Nome: nome, Naipe: naipes[rand.Intn(len(naipes))], Valor: 1 + rand.Intn(50), Raridade: "C"})
		}
		for i := 0; i < 50; i++ { // Incomuns
			s.Estoque["U"] = append(s.Estoque["U"], models.Carta{ID: randomString(5), Nome: nome, Naipe: naipes[rand.Intn(len(naipes))], Valor: 51 + rand.Intn(30), Raridade: "U"})
		}
		for i := 0; i < 20; i++ { // Raras
			s.Estoque["R"] = append(s.Estoque["R"], models.Carta{ID: randomString(5), Nome: nome, Naipe: naipes[rand.Intn(len(naipes))], Valor: 81 + rand.Intn(20), Raridade: "R"})
		}
		for i := 0; i < 5; i++ { // Lendárias
			s.Estoque["L"] = append(s.Estoque["L"], models.Carta{ID: randomString(5), Nome: nome, Naipe: naipes[rand.Intn(len(naipes))], Valor: 101 + rand.Intn(20), Raridade: "L"})
		}
	}

	log.Printf("Estoque inicializado: C=%d, U=%d, R=%d, L=%d",
		len(s.Estoque["C"]), len(s.Estoque["U"]), len(s.Estoque["R"]), len(s.Estoque["L"]))
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

func (s *Store) FormarPacote(tamanho int) []models.Carta {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	cartas := make([]models.Carta, 0, tamanho)
	for i := 0; i < tamanho; i++ {
		raridade := sampleRaridade()
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

		var carta models.Carta
		encontrou := false
		for j := start; j < len(ordem); j++ {
			r := ordem[j]
			if len(s.Estoque[r]) > 0 {
				idx := len(s.Estoque[r]) - 1
				carta = s.Estoque[r][idx]
				s.Estoque[r] = s.Estoque[r][:idx]
				encontrou = true
				break
			}
		}
		if !encontrou {
			carta = gerarCartaComum()
		}
		cartas = append(cartas, carta)
	}
	return cartas
}

func (s *Store) GetStatusEstoque() (map[string]int, int) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	status := make(map[string]int)
	total := 0
	for raridade, cartas := range s.Estoque {
		status[raridade] = len(cartas)
		total += len(cartas)
	}
	return status, total
}

func gerarCartaComum() models.Carta {
	nomes := []string{"Guerreiro", "Arqueiro", "Mago", "Cavaleiro", "Ladrão"}
	naipes := []string{"♠", "♥", "♦", "♣"}
	return models.Carta{
		ID:       randomString(5),
		Nome:     nomes[rand.Intn(len(nomes))],
		Naipe:    naipes[rand.Intn(len(naipes))],
		Valor:    1 + rand.Intn(50),
		Raridade: "C",
	}
}
