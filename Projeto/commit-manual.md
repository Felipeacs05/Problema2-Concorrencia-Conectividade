# Guia para Commits Graduais Manuais

Se preferir fazer os commits manualmente (mais controle), siga esta sequência:

## Dia 1 - Estrutura Base

### Manhã (09:00)
```bash
git add go.mod go.sum .gitignore
git commit -m "feat: configura módulo Go e dependências iniciais"
```

### Tarde (14:30)
```bash
git add protocolo/protocolo.go
git commit -m "feat: define estruturas de mensagens e protocolo de comunicação"
```

### Noite (20:00)
```bash
git add mosquitto/ servidor/Dockerfile cliente/Dockerfile
git commit -m "feat: adiciona configuração Docker e Mosquitto MQTT"
```

---

## Dia 2 - Servidor Básico

### Manhã (10:00)
```bash
# Adicione apenas as estruturas de dados (tipos)
git add servidor/main.go
git commit -m "feat: define estruturas básicas do servidor (Cliente, Sala, Servidor)"
```

### Tarde (15:00)
```bash
git add servidor/main.go
git commit --amend -m "feat: adiciona conexão MQTT e handlers de mensagens"
```

### Noite (21:30)
```bash
git add servidor/main.go
git commit --amend -m "feat: implementa API REST para comunicação entre servidores"
```

---

## Dia 3 - Consenso e Estoque

### Manhã (09:30)
```bash
git add servidor/main.go
git commit --amend -m "feat: implementa eleição de líder (algoritmo Raft-like)"
```

### Tarde (16:00)
```bash
git add servidor/main.go
git commit --amend -m "feat: adiciona gerenciamento distribuído de estoque de cartas"
```

---

## Dia 4 - Lógica de Jogo

### Manhã (10:30)
```bash
git add servidor/main.go
git commit --amend -m "feat: implementa matchmaking e criação de salas"
```

### Tarde (14:00)
```bash
git add servidor/main.go
git commit --amend -m "feat: adiciona lógica de jogo e padrão Host-Sombra"
```

### Noite (20:30)
```bash
git add servidor/main.go
git commit --amend -m "feat: implementa sincronização de estado entre servidores"
```

---

## Dia 5 - Cliente e Finalização

### Manhã (11:00)
```bash
git add cliente/main.go
git commit -m "feat: implementa cliente interativo com suporte a MQTT e comandos"
```

### Tarde (15:30)
```bash
git add docker-compose.yml
git commit -m "feat: adiciona docker-compose orquestrando 3 servidores + 3 brokers"
```

### Noite (19:00)
```bash
git add README.md ARQUITETURA.md IMPLEMENTACAO.md QUICKSTART.md DESENVOLVIMENTO.md Makefile
git commit -m "docs: adiciona documentação completa do projeto"
```

---

## Dicas Importantes

### ✅ Boas Práticas

1. **Espaçamento Natural**: Deixe algumas horas entre commits
2. **Horários Realistas**: Commits durante horário comercial/noite (evite madrugada)
3. **Mensagens Claras**: Use conventional commits (feat:, fix:, docs:)
4. **Iteração**: Use `--amend` para simular trabalho incremental no mesmo arquivo

### ⚠️ Evite

- ❌ Commits a cada minuto (não natural)
- ❌ Commits às 3h da manhã todos os dias
- ❌ Commits gigantes com tudo de uma vez
- ❌ Mensagens genéricas como "update" ou "changes"

### 📝 Exemplos de Mensagens Boas

```
feat: implementa algoritmo de eleição de líder
fix: corrige sincronização de estado entre servidores
refactor: melhora estrutura de dados do servidor
docs: adiciona documentação da arquitetura
chore: configura Docker Compose
```

### 🔧 Comandos Úteis

```bash
# Ver histórico de commits
git log --oneline --graph

# Ver diferenças antes de commitar
git diff

# Adicionar arquivos específicos
git add <arquivo>

# Corrigir último commit (mensagem ou arquivos)
git commit --amend

# Ver status
git status
```

### 🎯 Estratégia Recomendada

Se ainda não commitou nada:

1. **Opção A - Automático**: Execute o script `commit-gradual.ps1`
2. **Opção B - Semi-automático**: Execute `commit-simples.ps1`  
3. **Opção C - Manual**: Siga este guia acima

Se já fez um commit:

```bash
# Desfazer último commit (mantém alterações)
git reset --soft HEAD~1

# Depois siga este guia para commits graduais
```

---

## Verificação Final

Antes de fazer push:

```bash
# 1. Verifique os commits
git log --oneline --graph --all

# 2. Veja as datas
git log --pretty=format:"%h - %an, %ar : %s"

# 3. Se estiver OK, faça push
git push origin main
```

Se precisar refazer:

```bash
# Desfazer N commits (mantém arquivos)
git reset --soft HEAD~N

# Exemplo: desfazer últimos 5 commits
git reset --soft HEAD~5
```

---

**Lembre-se**: O mais importante é que os commits façam sentido lógico e estejam distribuídos de forma natural ao longo de vários dias. Não precisa ser perfeito!

