# Correções Finais: Deadlock e Notificação Cross-Server

## 🔍 Problemas Identificados e Corrigidos

### 1. **Notificação de Início de Partida Cross-Server**
**Problema:** Quando a partida inicia em partidas cross-server, apenas os jogadores no servidor Host recebiam a notificação de início. Os jogadores no servidor Shadow não recebiam porque o evento era publicado apenas no MQTT do servidor Host.

**Solução:** Modificada a função `iniciarPartida` para que, em partidas cross-server, ela:
- Publica o evento no MQTT local (para jogadores no servidor Host)
- Envia notificação HTTP para jogadores remotos (no servidor Shadow)

**Código alterado:**
```go
func (s *Servidor) iniciarPartida(sala *tipos.Sala) {
    // ... código existente ...
    
    // CORREÇÃO CRUCIAL: Em partidas cross-server, publica MQTT local E notifica jogadores remotos via HTTP
    if hostAddr == s.MeuEndereco && sombraAddr != "" {
        // Este servidor é o Host, partida é cross-server
        log.Printf("[INICIAR_PARTIDA_CROSS] Publicando no MQTT local e notificando Shadow via HTTP")
        // Publica no MQTT local (vai chegar apenas aos clientes conectados a este servidor)
        s.publicarEventoPartida(sala.ID, msg)
        // Notifica jogadores remotos (do Shadow) via HTTP
        go s.notificarJogadoresRemotosDaPartida(sala.ID, sombraAddr, jogadores, msg)
    } else {
        // Partida local - apenas publica no MQTT
        s.publicarEventoPartida(sala.ID, msg)
    }
}
```

### 2. **Deadlock na Função `forcarSincronizacaoEstado`**
**Problema:** A função `forcarSincronizacaoEstado` estava tentando pegar um lock da sala e, dentro desse lock, chamava `criarEstadoDaSala` que **também** tentava pegar o mesmo lock. Isso causava um **deadlock eterno** que travava toda a partida.

**Problema específico:**
```go
// ANTES (DEADLOCK):
func (s *Servidor) forcarSincronizacaoEstado(salaID string) {
    // ...
    sala.Mutex.Lock()              // <-- Lock 1
    estado := s.criarEstadoDaSala(sala)  // <-- criaEstadoDaSala tenta pegar Lock novamente = DEADLOCK!
    sala.Mutex.Unlock()
    // ...
}

func (s *Servidor) criarEstadoDaSala(sala *tipos.Sala) *tipos.EstadoPartida {
    sala.Mutex.Lock()              // <-- Lock 2 (deadlock!)
    defer sala.Mutex.Unlock()
    // ...
}
```

**Solução:** Remover o lock duplo. A função `criarEstadoDaSala` já faz o lock internamente, então não precisamos pegar o lock antes de chamá-la.

**Código corrigido:**
```go
func (s *Servidor) forcarSincronizacaoEstado(salaID string) {
    sala := s.Salas[salaID]
    if sala == nil || sala.ServidorSombra == "" {
        return
    }

    log.Printf("[FORCE_SYNC] Forçando sincronização de estado para sala %s", salaID)

    // CORREÇÃO: Não bloquear aqui! A função criarEstadoDaSala já faz o lock internamente.
    // Se pegarmos o lock aqui, causamos deadlock porque criarEstadoDaSala tenta pegar o mesmo lock.
    estado := s.criarEstadoDaSala(sala)

    if estado == nil {
        log.Printf("[FORCE_SYNC] Erro ao criar estado da sala %s", salaID)
        return
    }

    // ... resto do código ...
}
```

## 🎯 Impacto das Correções

### Antes das Correções:
- ❌ Partida não iniciava para jogadores em servidores diferentes
- ❌ Chat ficava instável após `/comprar`
- ❌ Deadlock constante causando timeout em todos os eventos
- ❌ Logs mostravam: `TIMEOUT NO LOCK DA SALA - IGNORANDO EVENTO`

### Depois das Correções:
- ✅ Partida inicia corretamente para TODOS os jogadores
- ✅ Chat funciona normalmente antes e depois da compra
- ✅ Sem deadlocks - locks são gerenciados corretamente
- ✅ Logs mostram início correto: `[INICIAR_PARTIDA_CROSS] Publicando no MQTT local e notificando Shadow via HTTP`

## 📊 Fluxo Corrigido

### Início de Partida Cross-Server:
1. Ambos jogadores compram cartas (`/comprar`)
2. Host detecta que todos estão prontos
3. Host chama `iniciarPartida(sala)`
4. `iniciarPartida` publica evento MQTT localmente (jogador no Host recebe)
5. `iniciarPartida` chama `notificarJogadoresRemotosDaPartida`
6. Função envia notificação HTTP para jogador remoto no Shadow
7. Shadow processa e envia mensagem para cliente via MQTT
8. **Ambos jogadores recebem a notificação de início da partida!** ✅

### Processamento de Eventos (CHAT, JOGAR, etc):
1. Shadow recebe evento do cliente
2. Shadow envia para Host via HTTP
3. Host processa evento (sem deadlock!)
4. Host retorna estado atualizado
5. Ambos jogadores são notificados

## 🧪 Como Testar

**Terminal 1:**
```bash
docker-compose run --rm cliente
# Nome: Felipe
# Servidor: 1
```

**Terminal 2:**
```bash
docker-compose run --rm cliente
# Nome: Davi
# Servidor: 2
```

**Depois de ambos executarem `/comprar`:**
- ✅ Ambos recebem: "Partida iniciada! É a vez de ..."
- ✅ Chat funciona normalmente
- ✅ Podem jogar cartas normalmente

## 📝 Arquivos Modificados

- `servidor/main.go`
  - `iniciarPartida()`: Adicionada lógica para notificar jogadores remotos via HTTP
  - `notificarJogadoresRemotosDaPartida()`: Nova função para enviar eventos para jogadores remotos
  - `forcarSincronizacaoEstado()`: Corrigido deadlock removendo lock duplo



