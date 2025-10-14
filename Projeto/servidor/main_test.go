package main

import (
	"testing"
)

// ==================== TESTES UNITÁRIOS ====================

// TestCompararCartas testa a lógica principal de comparação de cartas.
func TestCompararCartas(t *testing.T) {
	// Estrutura de casos de teste
	testCases := []struct {
		nome      string
		carta1    Carta
		carta2    Carta
		resultado int // >0 se c1 ganha, <0 se c2 ganha, 0 se empate
	}{
		{"Valor Maior Ganha", Carta{Valor: 10, Naipe: "♦"}, Carta{Valor: 5, Naipe: "♠"}, 5},
		{"Valor Menor Perde", Carta{Valor: 3, Naipe: "♠"}, Carta{Valor: 8, Naipe: "♥"}, -5},
		{"Empate no Valor, Naipe Maior Ganha", Carta{Valor: 7, Naipe: "♠"}, Carta{Valor: 7, Naipe: "♥"}, 1},
		{"Empate no Valor, Naipe Menor Perde", Carta{Valor: 7, Naipe: "♦"}, Carta{Valor: 7, Naipe: "♣"}, -1},
		{"Empate Total", Carta{Valor: 9, Naipe: "♣"}, Carta{Valor: 9, Naipe: "♣"}, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.nome, func(t *testing.T) {
			res := compararCartas(tc.carta1, tc.carta2)

			// Verifica se o sinal do resultado está correto
			if (res > 0 && tc.resultado <= 0) || (res < 0 && tc.resultado >= 0) || (res == 0 && tc.resultado != 0) {
				t.Errorf("Resultado inesperado. Esperado: %d, Recebido: %d", tc.resultado, res)
			}
		})
	}
}

// TestSampleRaridade verifica se a distribuição de raridades está dentro de uma margem aceitável.
func TestSampleRaridade(t *testing.T) {
	contagem := map[string]int{"C": 0, "U": 0, "R": 0, "L": 0}
	totalAmostras := 10000

	for i := 0; i < totalAmostras; i++ {
		raridade := sampleRaridade()
		contagem[raridade]++
	}

	// Verifica se as porcentagens estão próximas do esperado (com margem de erro)
	margemErro := 0.05
	esperado := map[string]float64{"C": 0.70, "U": 0.20, "R": 0.09, "L": 0.01}

	for raridade, p := range esperado {
		percentualObtido := float64(contagem[raridade]) / float64(totalAmostras)
		if percentualObtido < p-margemErro || percentualObtido > p+margemErro {
			t.Errorf("Distribuição da raridade '%s' fora do esperado. Esperado: %.2f, Obtido: %.2f",
				raridade, p, percentualObtido)
		}
	}
}

// TestGerarCartaComum verifica se as cartas comuns são geradas com os atributos corretos.
func TestGerarCartaComum(t *testing.T) {
	s := &Servidor{} // Instância vazia para chamar o método
	carta := s.gerarCartaComum()

	if carta.ID == "" || carta.Nome == "" || carta.Naipe == "" {
		t.Error("Carta comum gerada com campos vazios.")
	}
	if carta.Raridade != "C" {
		t.Errorf("Raridade incorreta. Esperado 'C', recebido '%s'", carta.Raridade)
	}
	if carta.Valor < 1 || carta.Valor > 50 {
		t.Errorf("Valor da carta comum fora do intervalo [1, 50]. Recebido: %d", carta.Valor)
	}
}

// TestInicializarEstoque garante que o estoque é populado com todas as raridades.
func TestInicializarEstoque(t *testing.T) {
	s := &Servidor{Estoque: make(map[string][]Carta)}
	s.inicializarEstoque()

	if len(s.Estoque["C"]) == 0 {
		t.Error("Estoque de cartas comuns não foi inicializado.")
	}
	if len(s.Estoque["U"]) == 0 {
		t.Error("Estoque de cartas incomuns não foi inicializado.")
	}
	if len(s.Estoque["R"]) == 0 {
		t.Error("Estoque de cartas raras não foi inicializado.")
	}
	if len(s.Estoque["L"]) == 0 {
		t.Error("Estoque de cartas lendárias não foi inicializado.")
	}
}

// TestRetirarCartasDoEstoque verifica a lógica de retirada e o decremento do estoque.
func TestRetirarCartasDoEstoque(t *testing.T) {
	s := &Servidor{Estoque: make(map[string][]Carta)}
	s.inicializarEstoque()

	totalAntes := s.contarEstoque()
	cartasRetiradas := s.retirarCartasDoEstoque(5)
	totalDepois := s.contarEstoque()

	if len(cartasRetiradas) != 5 {
		t.Errorf("Número incorreto de cartas retiradas. Esperado 5, recebido %d", len(cartasRetiradas))
	}
	if totalDepois != totalAntes-5 {
		t.Errorf("Estoque não foi decrementado corretamente. Antes: %d, Depois: %d", totalAntes, totalDepois)
	}
}
