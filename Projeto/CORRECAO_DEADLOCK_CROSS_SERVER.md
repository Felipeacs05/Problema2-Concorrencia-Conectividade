# Correção de Deadlock em Partidas Cross-Server

## Problema Identificado

### Sintomas
- Chat funcionava normalmente antes de ambos os jogadores comprarem cartas
- Após ambos os jogadores executarem `/comprar`, a partida não iniciava
- Chat parava de funcionar após as compras
- Logs mostravam: `TIMEOUT NO LOCK DA SALA - IGNORANDO EVENTO`

### Causa Raiz

**DEADLOCK por concorrência na gestão de locks da sala:**

1. **Jogador 1 (Host) compra** → `processarCompraPacote` marca como pronto → chama `verificarEIniciarPartidaSeProntos` (goroutine)
2. **Jogador 2 (Shadow) compra** → `processarCompraPacote` marca como pronto → envia evento `PLAYER_READY` para o Host
3. **Host recebe PLAYER_READY** → `processarEventoComoHost` tenta obter lock da sala com timeout de 2s
4. **PROBLEMA**: Se alguma operação estiver mantendo o lock (sincronização, verificação prematura, etc.), o evento `PLAYER_READY` dá timeout e é **ignorado**
5. **Resultado**: A partida nunca inicia porque o Host não processa o `PLAYER_READY` do Shadow

### Fluxo Problemático (ANTES)

```
SERVIDOR HOST (servidor1):
├─ Jogador1 compra
│  ├─ processarCompraPacote()
│  │  ├─ Marca Jogador1 como pronto
│  │  ├─ notificarJogadorRemoto() → Shadow
│  │  └─ forcarSincronizacaoEstado() → Shadow
│  └─ go verificarEIniciarPartidaSeProntos() ← CHAMADA PREMATURA
│     └─ Obtém lock da sala, verifica... (prontos=1/2, não inicia)

SERVIDOR SHADOW (servidor2):
├─ Jogador2 compra
│  ├─ processarCompraPacote()
│  │  └─ Marca Jogador2 como pronto
│  └─ go encaminharEventoParaHost("PLAYER_READY") → HOST

SERVIDOR HOST (servidor1):
├─ Recebe PLAYER_READY do Shadow
│  └─ processarEventoComoHost()
│     ├─ Tenta obter lock da sala (timeout 2s)
│     └─ ❌ TIMEOUT! Lock ocupado por:
│        - verificarEIniciarPartidaSeProntos() ainda rodando
│        - forcarSincronizacaoEstado() sincronizando
│        - Outras operações concorrentes
└─ RESULTADO: PLAYER_READY ignorado, partida não inicia
```

## Solução Implementada

### Mudanças em `processarCompraPacote` (servidor/main.go:1169-1191)

**Estratégia**: Eliminar chamadas prematuras a `verificarEIniciarPartidaSeProntos` no Host de partidas cross-server.

```go
// Marca como pronto na sala
sala.Mutex.Lock()
sala.Prontos[cliente.Nome] = true
isHost := sala.ServidorHost == s.MeuEndereco
isShadow := sala.ServidorSombra == s.MeuEndereco
hostAddr = sala.ServidorHost
sombraAddr = sala.ServidorSombra
sala.Mutex.Unlock()

// CORREÇÃO DEADLOCK: Diferentes estratégias dependendo do tipo de partida
if isShadow && hostAddr != "" {
    // Shadow: Envia PLAYER_READY para o Host após compra local
    log.Printf("[SHADOW] Jogador %s pronto. Notificando Host %s via PLAYER_READY.", cliente.Nome, hostAddr)
    go s.encaminharEventoParaHost(sala, cliente.ID, "PLAYER_READY", nil)
} else if isHost && sombraAddr == "" {
    // Host de partida LOCAL (ambos jogadores no mesmo servidor): Verifica imediatamente
    log.Printf("[HOST-LOCAL] Jogador %s pronto. Verificando se pode iniciar (partida local).", cliente.Nome)
    go s.verificarEIniciarPartidaSeProntos(sala)
} else if isHost && sombraAddr != "" {
    // Host de partida CROSS-SERVER: NÃO verifica aqui.
    // A verificação ocorrerá apenas quando receber PLAYER_READY do Shadow.
    log.Printf("[HOST-CROSS] Jogador %s pronto. Aguardando PLAYER_READY do Shadow (%s).", cliente.Nome, sombraAddr)
}
```

### Mudanças em `handleComandoPartida` (servidor/main.go:478-485)

**Simplificação**: Remover lógica duplicada de envio de `PLAYER_READY`.

```go
case "COMPRAR_PACOTE":
    var dados map[string]string
    json.Unmarshal(mensagem.Dados, &dados)
    clienteID := dados["cliente_id"]

    // CORREÇÃO: processarCompraPacote agora cuida de enviar PLAYER_READY se necessário
    log.Printf("[COMPRAR_DEBUG] Processando compra para cliente %s", clienteID)
    s.processarCompraPacote(clienteID, sala)
```

### Fluxo Corrigido (DEPOIS)

```
PARTIDA LOCAL (mesmo servidor):
├─ Jogador compra
│  └─ go verificarEIniciarPartidaSeProntos() ✓
│     └─ Verifica imediatamente (sem concorrência)

PARTIDA CROSS-SERVER:

SERVIDOR HOST (servidor1):
├─ Jogador1 compra
│  ├─ processarCompraPacote()
│  │  ├─ Marca Jogador1 como pronto
│  │  ├─ notificarJogadorRemoto() → Shadow
│  │  └─ forcarSincronizacaoEstado() → Shadow
│  └─ ⚠️  NÃO chama verificarEIniciarPartidaSeProntos()
│     (aguarda receber PLAYER_READY do Shadow)

SERVIDOR SHADOW (servidor2):
├─ Jogador2 compra
│  ├─ processarCompraPacote()
│  │  └─ Marca Jogador2 como pronto
│  └─ go encaminharEventoParaHost("PLAYER_READY") → HOST

SERVIDOR HOST (servidor1):
├─ Recebe PLAYER_READY do Shadow
│  └─ processarEventoComoHost()
│     ├─ Obtém lock da sala ✓ (sem concorrência)
│     ├─ Marca Jogador2 como pronto
│     ├─ go verificarEIniciarPartidaSeProntos()
│     │  └─ Verifica: prontos=2/2 → iniciarPartida() ✓
│     └─ Replica estado para Shadow
└─ RESULTADO: Partida inicia corretamente! 🎉
```

## Benefícios da Solução

1. **Elimina Deadlock**: O Host não tenta verificar o início prematuramente em partidas cross-server
2. **Sincronização Atômica**: Apenas o evento `PLAYER_READY` do Shadow aciona a verificação de início
3. **Mantém Compatibilidade**: Partidas locais (mesmo servidor) continuam funcionando normalmente
4. **Logs Claros**: Diferentes mensagens para partidas locais vs. cross-server facilitam debug

## Como Testar

### Teste Manual

1. **Inicie os servidores**:
   ```bash
   docker-compose up -d broker1 broker2 broker3 servidor1 servidor2 servidor3
   ```

2. **Terminal 1 - Jogador 1**:
   ```bash
   docker-compose run --rm cliente
   # Digite: Felipe
   # Escolha: servidor 1
   ```

3. **Terminal 2 - Jogador 2**:
   ```bash
   docker-compose run --rm cliente
   # Digite: Davi
   # Escolha: servidor 2
   ```

4. **Ambos executam** `/comprar` após a partida ser encontrada

5. **Resultado Esperado**:
   - ✓ Ambos recebem suas cartas
   - ✓ Chat continua funcionando
   - ✓ Mensagem aparece: `"Partida iniciada! É a vez de..."`
   - ✓ A partida inicia normalmente

### Script Automatizado

Execute o script de teste:
```bash
./test_fix_deadlock.sh
```

## Logs de Sucesso

Quando funcionando corretamente, você verá:

```
[HOST-CROSS] Jogador Felipe pronto. Aguardando PLAYER_READY do Shadow (servidor2:8080).
[SHADOW] Jogador Davi pronto. Notificando Host servidor1:8080 via PLAYER_READY.
[HOST] Jogador Davi está PRONTO (evento recebido). Prontos: 2/2
[INICIAR_PARTIDA:servidor1] Todos os jogadores da sala estão prontos. Iniciando.
[JOGO_DEBUG] Partida iniciada. Jogador inicial: Felipe
```

## Arquivos Modificados

- `servidor/main.go`:
  - Função `processarCompraPacote` (linhas 1169-1191)
  - Função `handleComandoPartida` (linhas 478-485)

## Data da Correção

26 de Outubro de 2025


