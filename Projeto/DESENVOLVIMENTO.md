# Guia de Desenvolvimento

Este documento fornece informações para desenvolvedores que desejam contribuir, modificar ou estender o projeto.

## Configuração do Ambiente de Desenvolvimento

### Pré-requisitos

```bash
# Go 1.25+
go version

# Docker e Docker Compose
docker --version
docker-compose --version

# Ferramentas úteis (opcional)
jq --version        # Para parsear JSON
curl --version      # Para testar API
```

### Clonando e Preparando

```bash
# Clone o repositório
git clone <url-do-repositorio>
cd Projeto

# Baixe as dependências
go mod download
go mod tidy

# Verifique a compilação
cd servidor && go build
cd ../cliente && go build
```

## Estrutura do Projeto

```
Projeto/
├── servidor/
│   ├── main.go              # Código principal do servidor (~1.380 linhas)
│   └── Dockerfile           # Container do servidor
├── cliente/
│   ├── main.go              # Código do cliente interativo (~460 linhas)
│   └── Dockerfile           # Container do cliente
├── protocolo/
│   └── protocolo.go         # Definições de mensagens e structs (~110 linhas)
├── mosquitto/
│   ├── config/
│   │   └── mosquitto.conf   # Configuração dos brokers MQTT
│   ├── data/                # Dados persistentes dos brokers
│   └── log/                 # Logs dos brokers
├── docker-compose.yml       # Orquestração de todos os serviços
├── go.mod                   # Dependências Go
├── Makefile                 # Comandos úteis
└── docs/
    ├── README.md            # Documentação geral
    ├── ARQUITETURA.md       # Detalhes da arquitetura
    ├── IMPLEMENTACAO.md     # Resumo da implementação
    ├── QUICKSTART.md        # Guia rápido
    └── DESENVOLVIMENTO.md   # Este arquivo
```

## Fluxo de Desenvolvimento

### 1. Desenvolvimento Local (sem Docker)

Para desenvolvimento rápido sem Docker:

```bash
# Terminal 1 - Mosquitto local (se disponível)
mosquitto -c mosquitto/config/mosquitto.conf

# Terminal 2 - Servidor 1
cd servidor
go run main.go -addr localhost:8080 -broker tcp://localhost:1883

# Terminal 3 - Cliente
cd cliente
go run main.go
# Escolha servidor 1
# Use tcp://localhost:1883
```

### 2. Desenvolvimento com Docker

Para testar com a infraestrutura completa:

```bash
# Build e start
docker-compose up --build

# Logs em tempo real
docker-compose logs -f servidor1

# Rebuild apenas um serviço
docker-compose build servidor1
docker-compose up -d servidor1
```

### 3. Ciclo de Desenvolvimento Típico

```bash
# 1. Edite o código (ex: servidor/main.go)

# 2. Teste localmente (opcional)
cd servidor
go run main.go -addr localhost:8080 -broker tcp://localhost:1883

# 3. Verifique erros de compilação
go build

# 4. Teste com Docker
docker-compose build servidor1
docker-compose up -d servidor1

# 5. Monitore logs
docker-compose logs -f servidor1

# 6. Teste funcionalidade com cliente
docker-compose run --rm cliente
```

## Adicionando Novas Funcionalidades

### Exemplo 1: Adicionar Novo Comando do Cliente

#### 1. Atualizar Protocolo (`protocolo/protocolo.go`)

```go
// Adicione a estrutura de dados
type DadosNovoComando struct {
    Campo1 string `json:"campo1"`
    Campo2 int    `json:"campo2"`
}
```

#### 2. Atualizar Cliente (`cliente/main.go`)

```go
// Adicione o comando no processador
func processarComando(entrada string) {
    switch comando {
    // ... outros comandos ...
    case "/novo":
        executarNovoComando()
    }
}

// Implemente a função
func executarNovoComando() {
    dados := protocolo.DadosNovoComando{
        Campo1: "valor",
        Campo2: 123,
    }
    
    mensagem := protocolo.Mensagem{
        Comando: "NOVO_COMANDO",
        Dados:   mustJSON(dados),
    }
    
    payload, _ := json.Marshal(mensagem)
    topico := fmt.Sprintf("partidas/%s/comandos", salaAtual)
    mqttClient.Publish(topico, 0, false, payload)
}
```

#### 3. Atualizar Servidor (`servidor/main.go`)

```go
// No handler de comandos MQTT
func (s *Servidor) handleComandoPartida(client mqtt.Client, msg mqtt.Message) {
    // ... código existente ...
    
    switch mensagem.Comando {
    // ... outros comandos ...
    case "NOVO_COMANDO":
        var dados protocolo.DadosNovoComando
        json.Unmarshal(mensagem.Dados, &dados)
        s.processarNovoComando(sala, dados)
    }
}

// Implemente o processamento
func (s *Servidor) processarNovoComando(sala *Sala, dados protocolo.DadosNovoComando) {
    // Lógica do novo comando
    log.Printf("Processando novo comando: %+v", dados)
    
    // Notifique os clientes
    resposta := protocolo.Mensagem{
        Comando: "NOVO_COMANDO_RESULTADO",
        Dados:   mustJSON(map[string]string{"status": "ok"}),
    }
    s.publicarEventoPartida(sala.ID, resposta)
}
```

### Exemplo 2: Adicionar Novo Endpoint REST

#### No Servidor (`servidor/main.go`)

```go
// 1. Registre a rota na API
func (s *Servidor) iniciarAPI() {
    router := gin.Default()
    
    // ... rotas existentes ...
    router.GET("/novo/endpoint", s.handleNovoEndpoint)
    
    router.Run(s.MeuEndereco)
}

// 2. Implemente o handler
func (s *Servidor) handleNovoEndpoint(c *gin.Context) {
    // Valide entrada
    var dados map[string]interface{}
    if err := c.ShouldBindJSON(&dados); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    
    // Processe a requisição
    resultado := s.processarNovaOperacao(dados)
    
    // Retorne resposta
    c.JSON(http.StatusOK, gin.H{
        "status": "success",
        "data":   resultado,
    })
}

func (s *Servidor) processarNovaOperacao(dados map[string]interface{}) interface{} {
    // Implemente a lógica
    return map[string]string{"resultado": "ok"}
}
```

## Debugging

### Logs Estruturados

O projeto usa `log.Printf` padrão. Para melhor debugging, adicione prefixos contextuais:

```go
// Bom: contexto claro
log.Printf("[ELEICAO] Servidor %s iniciando eleição termo %d", s.MeuEndereco, termo)

// Ruim: sem contexto
log.Printf("Iniciando eleição %d", termo)
```

### Debug com Delve

```bash
# Instale delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Execute servidor com debugger
cd servidor
dlv debug main.go -- -addr localhost:8080 -broker tcp://localhost:1883

# Comandos úteis do delve:
# break main.go:100   - Define breakpoint
# continue            - Continua execução
# next                - Próxima linha
# print variavel      - Imprime variável
# quit                - Sai
```

### Debugging com Logs do Docker

```bash
# Logs de um serviço específico
docker-compose logs -f --tail=100 servidor1

# Logs de múltiplos serviços
docker-compose logs -f servidor1 servidor2

# Grep em logs
docker-compose logs servidor1 | grep "ELEICAO"

# Logs com timestamps
docker-compose logs -f -t servidor1
```

### Debugging MQTT

```bash
# Subscribe a todos os tópicos (em container)
docker-compose exec broker1 mosquitto_sub -v -t "#"

# Subscribe a tópico específico
docker-compose exec broker1 mosquitto_sub -v -t "partidas/+/eventos"

# Publish manual (teste)
docker-compose exec broker1 mosquitto_pub -t "test/topic" -m '{"msg":"hello"}'
```

## Testes

### Testes Unitários (a implementar)

```go
// servidor/main_test.go
package main

import (
    "testing"
)

func TestCompararCartas(t *testing.T) {
    c1 := Carta{Nome: "Dragão", Valor: 100, Naipe: "♠"}
    c2 := Carta{Nome: "Guerreiro", Valor: 50, Naipe: "♥"}
    
    resultado := compararCartas(c1, c2)
    
    if resultado <= 0 {
        t.Errorf("Esperado c1 > c2, got resultado = %d", resultado)
    }
}

func TestSampleRaridade(t *testing.T) {
    // Teste distribuição estatística
    contagem := map[string]int{"C": 0, "U": 0, "R": 0, "L": 0}
    
    for i := 0; i < 10000; i++ {
        r := sampleRaridade()
        contagem[r]++
    }
    
    // Verifica aproximadamente: C=70%, U=20%, R=9%, L=1%
    if contagem["C"] < 6500 || contagem["C"] > 7500 {
        t.Errorf("Distribuição de C fora do esperado: %d", contagem["C"])
    }
}
```

### Testes de Integração (a implementar)

```bash
# teste/integracao_test.go
#!/bin/bash

# Inicia sistema
docker-compose up -d

# Aguarda inicialização
sleep 10

# Testa API
curl -f http://localhost:8080/servers || exit 1
curl -f http://localhost:8081/estoque/status || exit 1

# Testa eleição de líder
docker-compose stop servidor1
sleep 15
curl -f http://localhost:8081/estoque/status | jq '.lider' | grep true || exit 1

# Limpa
docker-compose down
```

## Otimizações

### Performance

#### 1. Pooling de Objetos

```go
// Use sync.Pool para objetos frequentemente alocados
var mensagemPool = sync.Pool{
    New: func() interface{} {
        return &protocolo.Mensagem{}
    },
}

func (s *Servidor) publicar(msg *protocolo.Mensagem) {
    // ... usa mensagem ...
    
    // Devolve ao pool
    mensagemPool.Put(msg)
}
```

#### 2. Buffered Channels

```go
// Bom: channel com buffer para evitar bloqueios
eventsChan := make(chan Evento, 100)

// Ruim: channel sem buffer pode bloquear
eventsChan := make(chan Evento)
```

#### 3. Context para Timeouts

```go
// Adicione timeout em requisições HTTP
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
resp, err := http.DefaultClient.Do(req)
```

### Memória

#### 1. Reutilização de Slices

```go
// Bom: reutiliza capacidade
cartas = cartas[:0]  // Mantém capacidade, zera length

// Ruim: cria novo slice
cartas = []Carta{}
```

#### 2. Strings vs Bytes

```go
// Bom: reutiliza buffer de bytes
var buf bytes.Buffer
json.NewEncoder(&buf).Encode(data)
payload := buf.Bytes()

// Ruim: cria múltiplas strings
payload := []byte(string(jsonBytes))
```

## Padrões de Código

### Nomenclatura

```go
// Variáveis: camelCase
var clienteAtual *Cliente
var numeroRodada int

// Constantes: UPPER_CASE ou PascalCase
const TIMEOUT_ELEICAO = 10 * time.Second
const PackSize = 5

// Funções públicas: PascalCase
func ProcessarJogada() {}

// Funções privadas: camelCase
func validarCarta() {}

// Tipos: PascalCase
type ServidorInfo struct {}
```

### Tratamento de Erros

```go
// Bom: propaga erro
func operacao() error {
    if err := outraOperacao(); err != nil {
        return fmt.Errorf("falha em operacao: %w", err)
    }
    return nil
}

// Bom: log e continua (quando apropriado)
if err := enviarNotificacao(); err != nil {
    log.Printf("[AVISO] Falha ao notificar: %v", err)
    // Continua execução
}

// Ruim: ignora erro
_ = operacaoPerigosa()
```

### Locks

```go
// Bom: defer unlock imediatamente
mutex.Lock()
defer mutex.Unlock()
// ... código ...

// Bom: lock de escopo mínimo
func atualizarCampo() {
    mutex.Lock()
    campo = novoValor
    mutex.Unlock()
    
    // Operações pesadas fora do lock
    processarDados()
}

// Ruim: lock por muito tempo
mutex.Lock()
operacaoPesada()  // Segura lock desnecessariamente
mutex.Unlock()
```

## Monitoramento e Observabilidade

### Métricas (a implementar)

```go
// Exemplo com Prometheus
import "github.com/prometheus/client_golang/prometheus"

var (
    partidasAtivas = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "partidas_ativas_total",
        Help: "Número de partidas ativas",
    })
    
    comprasEstoque = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "compras_estoque_total",
        Help: "Total de compras do estoque",
    })
    
    latenciaEleicao = prometheus.NewHistogram(prometheus.HistogramOpts{
        Name: "eleicao_duracao_segundos",
        Help: "Duração da eleição de líder",
        Buckets: prometheus.DefBuckets,
    })
)

func init() {
    prometheus.MustRegister(partidasAtivas, comprasEstoque, latenciaEleicao)
}

// Use as métricas
func (s *Servidor) criarSala() {
    // ...
    partidasAtivas.Inc()
}

func (s *Servidor) finalizarPartida() {
    // ...
    partidasAtivas.Dec()
}
```

### Health Checks

```go
// Adicione endpoint de health
router.GET("/health", func(c *gin.Context) {
    health := gin.H{
        "status": "up",
        "timestamp": time.Now(),
        "lider": s.SouLider,
        "clientes_ativos": len(s.Clientes),
        "partidas_ativas": len(s.Salas),
    }
    
    // Verifica conexão MQTT
    if !s.MQTTClient.IsConnected() {
        health["status"] = "degraded"
        health["mqtt"] = "disconnected"
    }
    
    c.JSON(http.StatusOK, health)
})
```

## Contribuindo

### Processo de Contribuição

1. **Fork** o repositório
2. **Clone** seu fork: `git clone <seu-fork>`
3. **Branch** para feature: `git checkout -b feature/minha-feature`
4. **Desenvolva** e teste localmente
5. **Commit**: `git commit -am 'Adiciona minha feature'`
6. **Push**: `git push origin feature/minha-feature`
7. **Pull Request** para o repositório principal

### Padrão de Commits

```
tipo(escopo): descrição curta

Descrição mais detalhada do que foi alterado e por quê.

Fixes #123
```

**Tipos**:
- `feat`: Nova funcionalidade
- `fix`: Correção de bug
- `docs`: Documentação
- `style`: Formatação (sem mudança de lógica)
- `refactor`: Refatoração de código
- `perf`: Melhoria de performance
- `test`: Adiciona ou corrige testes
- `chore`: Tarefas de manutenção

**Exemplos**:
```
feat(servidor): adiciona endpoint de metrics

Implementa endpoint /metrics para exportar métricas Prometheus.
Inclui métricas de partidas ativas, compras de estoque e latência.

Closes #42
```

```
fix(cliente): corrige reconexão ao broker MQTT

Cliente agora tenta reconectar automaticamente quando perde conexão.
Adiciona retry exponencial com backoff de até 30 segundos.

Fixes #56
```

## Recursos Úteis

### Documentação

- **Go**: https://go.dev/doc/
- **Gin**: https://gin-gonic.com/docs/
- **Paho MQTT**: https://pkg.go.dev/github.com/eclipse/paho.mqtt.golang
- **Docker Compose**: https://docs.docker.com/compose/
- **Mosquitto**: https://mosquitto.org/documentation/

### Ferramentas

- **MQTT Explorer**: Cliente MQTT gráfico para debug
- **Postman**: Testar endpoints REST
- **jq**: Processar JSON na linha de comando
- **hey**: Teste de carga HTTP: `go install github.com/rakyll/hey@latest`

### Livros e Artigos

- *Designing Data-Intensive Applications* - Martin Kleppmann
- *Distributed Systems* - Maarten van Steen
- *The Raft Consensus Algorithm* - Diego Ongaro

---

**Dúvidas?** Consulte os outros documentos ou abra uma issue!

