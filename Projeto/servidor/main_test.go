package main

import (
	"testing"
)

// ==================== TESTES UNITÁRIOS ====================

// TestCompararCartas testa a comparação de cartas
func TestCompararCartas(t *testing.T) {
	tests := []struct {
		name     string
		carta1   Carta
		carta2   Carta
		expected int // > 0 se carta1 vence, < 0 se carta2 vence, 0 empate
	}{
		{
			name:     "Carta1 maior valor",
			carta1:   Carta{Nome: "Dragão", Valor: 100, Naipe: "♠"},
			carta2:   Carta{Nome: "Guerreiro", Valor: 50, Naipe: "♥"},
			expected: 50, // valor1 - valor2 = 100 - 50
		},
		{
			name:     "Carta2 maior valor",
			carta1:   Carta{Nome: "Arqueiro", Valor: 30, Naipe: "♦"},
			carta2:   Carta{Nome: "Mago", Valor: 80, Naipe: "♣"},
			expected: -50, // 30 - 80
		},
		{
			name:     "Mesmo valor, desempate por naipe (Espadas vence)",
			carta1:   Carta{Nome: "Cavaleiro", Valor: 60, Naipe: "♠"},
			carta2:   Carta{Nome: "Paladino", Valor: 60, Naipe: "♥"},
			expected: 1, // ♠(4) > ♥(3)
		},
		{
			name:     "Mesmo valor, desempate por naipe (Corações vence Ouros)",
			carta1:   Carta{Nome: "Bárbaro", Valor: 45, Naipe: "♥"},
			carta2:   Carta{Nome: "Ladino", Valor: 45, Naipe: "♦"},
			expected: 1, // ♥(3) > ♦(2)
		},
		{
			name:     "Cartas idênticas",
			carta1:   Carta{Nome: "Anjo", Valor: 90, Naipe: "♠"},
			carta2:   Carta{Nome: "Demônio", Valor: 90, Naipe: "♠"},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultado := compararCartas(tt.carta1, tt.carta2)
			
			if tt.expected > 0 && resultado <= 0 {
				t.Errorf("Esperado carta1 vencer, mas resultado foi %d", resultado)
			} else if tt.expected < 0 && resultado >= 0 {
				t.Errorf("Esperado carta2 vencer, mas resultado foi %d", resultado)
			} else if tt.expected == 0 && resultado != 0 {
				t.Errorf("Esperado empate, mas resultado foi %d", resultado)
			}
		})
	}
}

// TestSampleRaridade testa a distribuição de raridades
func TestSampleRaridade(t *testing.T) {
	// Executa 10000 amostras e verifica distribuição aproximada
	contagem := map[string]int{
		"C": 0,
		"U": 0,
		"R": 0,
		"L": 0,
	}

	iteracoes := 10000
	for i := 0; i < iteracoes; i++ {
		r := sampleRaridade()
		contagem[r]++
	}

	// Verifica distribuição aproximada (com margem de erro de 5%)
	esperado := map[string]float64{
		"C": 0.70, // 70%
		"U": 0.20, // 20%
		"R": 0.09, // 9%
		"L": 0.01, // 1%
	}

	for raridade, percentual := range esperado {
		obtido := float64(contagem[raridade]) / float64(iteracoes)
		diferenca := obtido - percentual

		if diferenca < -0.05 || diferenca > 0.05 {
			t.Errorf("Distribuição de %s fora do esperado: esperado %.2f±0.05, obtido %.2f",
				raridade, percentual, obtido)
		}
	}

	t.Logf("Distribuição obtida: C=%d (%.1f%%), U=%d (%.1f%%), R=%d (%.1f%%), L=%d (%.1f%%)",
		contagem["C"], float64(contagem["C"])/float64(iteracoes)*100,
		contagem["U"], float64(contagem["U"])/float64(iteracoes)*100,
		contagem["R"], float64(contagem["R"])/float64(iteracoes)*100,
		contagem["L"], float64(contagem["L"])/float64(iteracoes)*100)
}

// TestGerarCartaComum testa geração de cartas comuns
func TestGerarCartaComum(t *testing.T) {
	s := &Servidor{}

	for i := 0; i < 100; i++ {
		carta := s.gerarCartaComum()

		// Verifica campos obrigatórios
		if carta.ID == "" {
			t.Error("Carta gerada sem ID")
		}
		if carta.Nome == "" {
			t.Error("Carta gerada sem Nome")
		}
		if carta.Naipe == "" {
			t.Error("Carta gerada sem Naipe")
		}
		if carta.Valor < 1 || carta.Valor > 50 {
			t.Errorf("Valor da carta fora do esperado: %d (esperado 1-50)", carta.Valor)
		}
		if carta.Raridade != "C" {
			t.Errorf("Raridade incorreta: %s (esperado C)", carta.Raridade)
		}

		// Verifica naipes válidos
		naipesValidos := map[string]bool{"♠": true, "♥": true, "♦": true, "♣": true}
		if !naipesValidos[carta.Naipe] {
			t.Errorf("Naipe inválido: %s", carta.Naipe)
		}
	}
}

// TestEstoqueInicial testa inicialização do estoque
func TestEstoqueInicial(t *testing.T) {
	s := &Servidor{}
	s.Estoque = make(map[string][]Carta)
	s.inicializarEstoque()

	// Verifica se todas as raridades foram inicializadas
	if len(s.Estoque["C"]) == 0 {
		t.Error("Estoque de cartas Comuns vazio")
	}
	if len(s.Estoque["U"]) == 0 {
		t.Error("Estoque de cartas Incomuns vazio")
	}
	if len(s.Estoque["R"]) == 0 {
		t.Error("Estoque de cartas Raras vazio")
	}
	if len(s.Estoque["L"]) == 0 {
		t.Error("Estoque de cartas Lendárias vazio")
	}

	// Verifica proporções esperadas (aproximadamente)
	totalC := len(s.Estoque["C"])
	totalU := len(s.Estoque["U"])
	totalR := len(s.Estoque["R"])
	totalL := len(s.Estoque["L"])

	t.Logf("Estoque inicializado: C=%d, U=%d, R=%d, L=%d (Total=%d)",
		totalC, totalU, totalR, totalL, totalC+totalU+totalR+totalL)

	// Comuns devem ser maioria
	if totalC <= totalU {
		t.Error("Deveria haver mais cartas Comuns do que Incomuns")
	}
	if totalU <= totalR {
		t.Error("Deveria haver mais cartas Incomuns do que Raras")
	}
	if totalR <= totalL {
		t.Error("Deveria haver mais cartas Raras do que Lendárias")
	}
}

// TestRetirarCartasDoEstoque testa retirada de cartas do estoque
func TestRetirarCartasDoEstoque(t *testing.T) {
	s := &Servidor{}
	s.Estoque = make(map[string][]Carta)
	s.inicializarEstoque()

	estoqueInicial := s.contarEstoque()

	// Retira 10 cartas
	cartas := s.retirarCartasDoEstoque(10)

	if len(cartas) != 10 {
		t.Errorf("Esperado 10 cartas, obtido %d", len(cartas))
	}

	estoqueFinal := s.contarEstoque()
	if estoqueFinal != estoqueInicial-10 {
		t.Errorf("Estoque não foi decrementado corretamente: inicial=%d, final=%d",
			estoqueInicial, estoqueFinal)
	}

	// Verifica que cartas têm IDs únicos
	ids := make(map[string]bool)
	for _, carta := range cartas {
		if ids[carta.ID] {
			t.Errorf("Carta duplicada encontrada: %s", carta.ID)
		}
		ids[carta.ID] = true
	}
}

// BenchmarkCompararCartas benchmark para comparação de cartas
func BenchmarkCompararCartas(b *testing.B) {
	c1 := Carta{Nome: "Dragão", Valor: 100, Naipe: "♠"}
	c2 := Carta{Nome: "Guerreiro", Valor: 50, Naipe: "♥"}

	for i := 0; i < b.N; i++ {
		compararCartas(c1, c2)
	}
}

// BenchmarkSampleRaridade benchmark para amostragem de raridade
func BenchmarkSampleRaridade(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sampleRaridade()
	}
}

